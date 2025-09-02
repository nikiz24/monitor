package monitor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/eryajf/promwrite"
	"github.com/miekg/dns"
	"go.uber.org/zap"
)

// Config defines the configuration for the metrics system
type Config struct {
	// Service identification
	Namespace   string
	Subsystem   string
	ServiceName string

	// Remote write configuration
	RemoteWriteURL      string
	RemoteWriteInterval time.Duration

	// Instance information
	InstanceIP   string
	Version      string
	BuildCommit  string
	BuildTime    string
	CustomLabels map[string]string

	// Optional logger
	Logger *zap.Logger

	// DNS resolver options (optional, for advanced use cases)
	DNSEnable          bool
	DNSCacheTTL        time.Duration
	DNSRefreshInterval time.Duration
	DNSTimeout         time.Duration
	DNSUDPServers      []string // e.g. ["1.1.1.1:53", "8.8.8.8:53"]
	DNSTLSServers      []string // e.g. ["1.1.1.1:853", "9.9.9.9:853"]
	DNSDoHEndpoints    []string // e.g. ["https://cloudflare-dns.com/dns-query"]
}

// DefaultConfig returns a default configuration
func DefaultConfig() Config {
	ip, _ := GetOutboundIPv4()
	return Config{
		Namespace:           "app",
		Subsystem:           "prod",
		ServiceName:         "service",
		RemoteWriteInterval: 15 * time.Second,
		InstanceIP:          ip,
		CustomLabels:        make(map[string]string),
	}
}

// Manager is the main interface for metrics collection and reporting
type Manager interface {
	Start() error
	Stop()
	RegisterCollector(collector Collector)
	GetMetrics() []Metric
}

// Collector defines a metrics collector that can provide multiple metrics
type Collector interface {
	Collect() []Metric
	Name() string
}

// Metric represents a single metric data point
type Metric struct {
	Name       string
	Value      float64
	Labels     map[string]string
	MetricType MetricType
	Timestamp  time.Time
}

// MetricType represents the type of a metric
type MetricType int

const (
	Counter MetricType = iota
	Gauge
	Histogram
	Summary
)

// managerImpl is the implementation of Manager
type managerImpl struct {
	config     Config
	collectors []Collector
	client     *promwrite.Client
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	mutex      sync.RWMutex

	// DNS functionality
	targetHost  string
	resolvedIPs []string
	lastResolve time.Time
	dnsCfg      dnsConfig
	dnsCache    map[string]dnsCacheEntry
}

type dnsConfig struct {
	enabled         bool
	cacheTTL        time.Duration
	refreshInterval time.Duration
	timeout         time.Duration
	udpServers      []string
	tlsServers      []string
	dohEndpoints    []string
}

type dnsCacheEntry struct {
	ips []string
	ttl time.Time
}

// NewManager creates a new metrics manager
func NewManager(config Config) (Manager, error) {
	if config.ServiceName == "" {
		return nil, fmt.Errorf("service name cannot be empty")
	}

	if config.InstanceIP == "" {
		ip, err := GetOutboundIPv4()
		if err != nil {
			return nil, fmt.Errorf("failed to get outbound IPv4: %w", err)
		}
		config.InstanceIP = ip
	}

	ctx, cancel := context.WithCancel(context.Background())

	var client *promwrite.Client
	if config.RemoteWriteURL != "" {
		client = promwrite.NewClient(config.RemoteWriteURL)
	}

	// Parse target host for DNS refresh
	var host string
	if config.RemoteWriteURL != "" {
		if u, err := url.Parse(config.RemoteWriteURL); err == nil {
			host = u.Hostname()
		}
	}

	mgr := &managerImpl{
		config:     config,
		collectors: []Collector{},
		client:     client,
		ctx:        ctx,
		cancel:     cancel,
		targetHost: host,
		dnsCfg: dnsConfig{
			enabled:         config.DNSEnable,
			cacheTTL:        pickDuration(config.DNSCacheTTL, 10*time.Minute),
			refreshInterval: pickDuration(config.DNSRefreshInterval, 5*time.Minute),
			timeout:         pickDuration(config.DNSTimeout, 800*time.Millisecond),
			udpServers:      append([]string(nil), config.DNSUDPServers...),
			tlsServers:      append([]string(nil), config.DNSTLSServers...),
			dohEndpoints:    append([]string(nil), config.DNSDoHEndpoints...),
		},
		dnsCache: make(map[string]dnsCacheEntry),
	}
	return mgr, nil
}

func pickDuration(v time.Duration, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}

// RegisterCollector implements Manager interface
func (m *managerImpl) RegisterCollector(collector Collector) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.collectors = append(m.collectors, collector)

	if m.config.Logger != nil {
		m.config.Logger.Debug("Registered metrics collector",
			zap.String("collector", collector.Name()))
	}
}

// Start implements Manager interface
func (m *managerImpl) Start() error {
	if m.client == nil && m.config.RemoteWriteURL != "" {
		m.client = promwrite.NewClient(m.config.RemoteWriteURL)
	}

	if m.client == nil {
		if m.config.Logger != nil {
			m.config.Logger.Warn("Starting metrics manager without remote write URL")
		}
		return nil
	}

	// Periodic write loop
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		interval := m.config.RemoteWriteInterval
		if interval <= 0 {
			interval = 15 * time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := m.writeMetrics(); err != nil {
					if m.config.Logger != nil {
						m.config.Logger.Error("Failed to write metrics", zap.Error(err))
					}
				}
			case <-m.ctx.Done():
				return
			}
		}
	}()

	// DNS refresh loop
	if m.dnsCfg.enabled && m.targetHost != "" && net.ParseIP(m.targetHost) == nil {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			ticker := time.NewTicker(m.dnsCfg.refreshInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					m.refreshDNS(false)
				case <-m.ctx.Done():
					return
				}
			}
		}()
	}

	return nil
}

// Stop implements Manager interface
func (m *managerImpl) Stop() {
	m.cancel()
	m.wg.Wait()
}

// GetMetrics implements Manager interface
func (m *managerImpl) GetMetrics() []Metric {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	var metrics []Metric
	for _, collector := range m.collectors {
		metrics = append(metrics, collector.Collect()...)
	}

	return metrics
}

// writeMetrics sends collected metrics to remote write endpoint
func (m *managerImpl) writeMetrics() error {
	// Check if client is available
	if m.client == nil {
		return fmt.Errorf("no remote write client configured")
	}

	metrics := m.GetMetrics()
	if len(metrics) == 0 {
		return nil
	}

	tsList := m.convertToTimeSeries(metrics)

	ctx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
	defer cancel()

	req := &promwrite.WriteRequest{
		TimeSeries: tsList,
	}

	_, err := m.client.Write(ctx, req)
	if err != nil {
		// On DNS-related failures, try a forced DNS refresh once
		if m.refreshDNS(true) {
			_, retryErr := m.client.Write(ctx, req)
			if retryErr == nil {
				return nil
			}
			return fmt.Errorf("writing time series failed after dns refresh: %w", retryErr)
		}
		return fmt.Errorf("writing time series failed: %w", err)
	}

	return nil
}

// RefreshDNS exposes DNS refresh functionality for external use
func (m *managerImpl) RefreshDNS(force bool) bool {
	return m.refreshDNS(force)
}

// refreshDNS resolves the target host and recreates the client if IP set changed
func (m *managerImpl) refreshDNS(force bool) bool {
	if m.targetHost == "" {
		return false
	}

	// Throttle resolves
	if !force && time.Since(m.lastResolve) < 1*time.Minute {
		return false
	}

	// Try cache first
	if ce, ok := m.dnsCache[m.targetHost]; ok && time.Now().Before(ce.ttl) && !force {
		if !stringSlicesEqual(ce.ips, m.resolvedIPs) {
			m.resolvedIPs = ce.ips
			m.lastResolve = time.Now()
			if m.config.RemoteWriteURL != "" {
				m.client = promwrite.NewClient(m.config.RemoteWriteURL)
			}
			if m.config.Logger != nil {
				m.config.Logger.Info("DNS cache hit, refreshed client",
					zap.String("host", m.targetHost), zap.Strings("ips", ce.ips))
			}
			return true
		}
		m.lastResolve = time.Now()
		return false
	}

	var (
		newSet []string
		err    error
	)
	if m.dnsCfg.enabled {
		newSet, err = m.resolveFastest(m.targetHost)
	} else {
		sysIPs, e := net.LookupIP(m.targetHost)
		err = e
		for _, ip := range sysIPs {
			newSet = append(newSet, ip.String())
		}
	}

	if err != nil || len(newSet) == 0 {
		if m.config.Logger != nil {
			m.config.Logger.Warn("DNS lookup failed", zap.String("host", m.targetHost), zap.Error(err))
		}
		m.lastResolve = time.Now()
		return false
	}

	changed := !stringSlicesEqual(newSet, m.resolvedIPs)
	m.resolvedIPs = newSet
	m.lastResolve = time.Now()

	// Cache the result
	if m.dnsCfg.enabled {
		m.dnsCache[m.targetHost] = dnsCacheEntry{ips: newSet, ttl: time.Now().Add(m.dnsCfg.cacheTTL)}
	}

	if changed || force {
		// Recreate client to force new connections
		if m.config.RemoteWriteURL != "" {
			m.client = promwrite.NewClient(m.config.RemoteWriteURL)
			if m.config.Logger != nil {
				m.config.Logger.Info("Refreshed remote write client after DNS update",
					zap.String("host", m.targetHost), zap.Strings("ips", m.resolvedIPs))
			}
		}
		return true
	}
	return false
}

// resolveFastest queries all configured resolvers concurrently and returns first success
func (m *managerImpl) resolveFastest(host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(m.ctx, m.dnsCfg.timeout)
	defer cancel()

	type result struct {
		ips []string
		err error
	}
	ch := make(chan result, 1)
	var wg sync.WaitGroup

	// UDP servers
	for _, srv := range m.dnsCfg.udpServers {
		s := srv
		wg.Add(1)
		go func() {
			defer wg.Done()
			ips, err := resolveUDP(ctx, host, s)
			select {
			case ch <- result{ips, err}:
			default:
			}
		}()
	}

	// TLS servers (DoT)
	for _, srv := range m.dnsCfg.tlsServers {
		s := srv
		wg.Add(1)
		go func() {
			defer wg.Done()
			ips, err := resolveTLS(ctx, host, s)
			select {
			case ch <- result{ips, err}:
			default:
			}
		}()
	}

	// DoH endpoints
	for _, ep := range m.dnsCfg.dohEndpoints {
		e := ep
		wg.Add(1)
		go func() {
			defer wg.Done()
			ips, err := resolveDoH(ctx, host, e)
			select {
			case ch <- result{ips, err}:
			default:
			}
		}()
	}

	// System resolver as fallback
	wg.Add(1)
	go func() {
		defer wg.Done()
		netIPs, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		ips := make([]string, 0, len(netIPs))
		for _, ip := range netIPs {
			ips = append(ips, ip.String())
		}
		select {
		case ch <- result{ips, err}:
		default:
		}
	}()

	var firstErr error
	attempts := 1 + len(m.dnsCfg.udpServers) + len(m.dnsCfg.tlsServers) + len(m.dnsCfg.dohEndpoints)
	for i := 0; i < attempts; i++ {
		select {
		case r := <-ch:
			if r.err == nil && len(r.ips) > 0 {
				return r.ips, nil
			}
			if firstErr == nil {
				firstErr = r.err
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	wg.Wait()
	if firstErr == nil {
		firstErr = fmt.Errorf("no dns result")
	}
	return nil, firstErr
}

func resolveUDP(ctx context.Context, host, server string) ([]string, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(host), dns.TypeA)
	c := &dns.Client{Net: "udp", Timeout: 800 * time.Millisecond}
	r, _, err := c.ExchangeContext(ctx, m, server)
	if err != nil || r == nil || r.Rcode != dns.RcodeSuccess {
		return nil, fmt.Errorf("udp dns failed: %v", err)
	}
	ips := make([]string, 0, len(r.Answer))
	for _, ans := range r.Answer {
		if a, ok := ans.(*dns.A); ok {
			ips = append(ips, a.A.String())
		}
	}
	return ips, nil
}

func resolveTLS(ctx context.Context, host, server string) ([]string, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(host), dns.TypeA)
	c := &dns.Client{Net: "tcp-tls", Timeout: 800 * time.Millisecond}
	r, _, err := c.ExchangeContext(ctx, m, server)
	if err != nil || r == nil || r.Rcode != dns.RcodeSuccess {
		return nil, fmt.Errorf("tls dns failed: %v", err)
	}
	ips := make([]string, 0, len(r.Answer))
	for _, ans := range r.Answer {
		if a, ok := ans.(*dns.A); ok {
			ips = append(ips, a.A.String())
		}
	}
	return ips, nil
}

func resolveDoH(ctx context.Context, host, endpoint string) ([]string, error) {
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(host), dns.TypeA)
	payload, err := q.Pack()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("doh status: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var r dns.Msg
	if err := r.Unpack(body); err != nil {
		return nil, err
	}
	if r.Rcode != dns.RcodeSuccess {
		return nil, fmt.Errorf("doh rcode: %d", r.Rcode)
	}
	ips := make([]string, 0, len(r.Answer))
	for _, ans := range r.Answer {
		if a, ok := ans.(*dns.A); ok {
			ips = append(ips, a.A.String())
		}
	}
	return ips, nil
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// convertToTimeSeries converts generic metrics to promwrite time series format
func (m *managerImpl) convertToTimeSeries(metrics []Metric) []promwrite.TimeSeries {
	var result []promwrite.TimeSeries

	prefix := fmt.Sprintf("%s_%s", m.config.Namespace, m.config.Subsystem)

	for _, metric := range metrics {
		expectedCapacity := 4 + len(m.config.CustomLabels) + len(metric.Labels)

		metricName := fmt.Sprintf("%s_%s", prefix, metric.Name)

		labels := make([]promwrite.Label, 0, expectedCapacity)

		labels = append(labels, []promwrite.Label{
			{Name: "__name__", Value: metricName},
			{Name: "_instance_", Value: m.config.InstanceIP},
			{Name: "instance", Value: m.config.InstanceIP},
			{Name: "_target_", Value: m.config.ServiceName},
		}...)

		for k, v := range m.config.CustomLabels {
			labels = append(labels, promwrite.Label{Name: k, Value: v})
		}

		for k, v := range metric.Labels {
			labels = append(labels, promwrite.Label{Name: k, Value: v})
		}

		ts := promwrite.TimeSeries{
			Labels: labels,
			Sample: promwrite.Sample{
				Time:  metric.Timestamp,
				Value: metric.Value,
			},
		}

		result = append(result, ts)
	}

	return result
}

// GetOutboundIPv4 gets the outbound IPv4 address of the local machine
func GetOutboundIPv4() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String(), nil
}
