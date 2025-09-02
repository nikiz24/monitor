package monitor

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// BaseCollector provides basic collector functionality
type BaseCollector struct {
	name   string
	logger *zap.Logger
	mutex  sync.RWMutex
}

// Name implements Collector interface
func (b *BaseCollector) Name() string {
	return b.name
}

// NewBaseCollector creates a base collector
func NewBaseCollector(name string, logger *zap.Logger) BaseCollector {
	return BaseCollector{
		name:   name,
		logger: logger,
	}
}

// CounterCollector provides simple counter metrics
type CounterCollector struct {
	BaseCollector
	counters map[string]*atomic.Int64
	types    map[string]MetricType
	mutex    sync.RWMutex
}

// NewCounterCollector creates a new counter collector
func NewCounterCollector(name string, logger *zap.Logger) *CounterCollector {
	return &CounterCollector{
		BaseCollector: NewBaseCollector(name, logger),
		counters:      make(map[string]*atomic.Int64),
		types:         make(map[string]MetricType),
	}
}

// Inc increments a counter by 1
func (c *CounterCollector) Inc(name string) {
	c.mutex.RLock()
	counter, exists := c.counters[name]
	c.mutex.RUnlock()

	if !exists {
		c.mutex.Lock()
		if counter, exists = c.counters[name]; !exists {
			counter = &atomic.Int64{}
			c.counters[name] = counter
		}
		c.types[name] = Counter
		c.mutex.Unlock()
	}

	counter.Add(1)
}

// Add adds a specific value to a counter
func (c *CounterCollector) Add(name string, delta int64) {
	c.mutex.RLock()
	counter, exists := c.counters[name]
	c.mutex.RUnlock()

	if !exists {
		c.mutex.Lock()
		if counter, exists = c.counters[name]; !exists {
			counter = &atomic.Int64{}
			c.counters[name] = counter
		}
		c.types[name] = Counter
		c.mutex.Unlock()
	}

	counter.Add(delta)
}

// Set sets a counter to a specific value (makes it a gauge)
func (c *CounterCollector) Set(name string, value int64) {
	c.mutex.RLock()
	counter, exists := c.counters[name]
	c.mutex.RUnlock()

	if !exists {
		c.mutex.Lock()
		if counter, exists = c.counters[name]; !exists {
			counter = &atomic.Int64{}
			c.counters[name] = counter
		}
		c.types[name] = Gauge
		c.mutex.Unlock()
	}

	counter.Store(value)
	c.mutex.Lock()
	c.types[name] = Gauge
	c.mutex.Unlock()
}

// Get gets the current value of a counter
func (c *CounterCollector) Get(name string) int64 {
	c.mutex.RLock()
	counter, exists := c.counters[name]
	c.mutex.RUnlock()

	if !exists {
		return 0
	}
	return counter.Load()
}

// Collect implements Collector interface
func (c *CounterCollector) Collect() []Metric {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	now := time.Now()
	metrics := make([]Metric, 0, len(c.counters))

	for name, counter := range c.counters {
		metrics = append(metrics, Metric{
			Name:       name,
			Value:      float64(counter.Load()),
			Labels:     map[string]string{},
			MetricType: c.types[name],
			Timestamp:  now,
		})
	}

	return metrics
}

// LabeledCounterCollector provides labeled counter metrics
type LabeledCounterCollector struct {
	BaseCollector
	values          map[string]*labeledCounterValue
	mutex           sync.RWMutex
	seriesTTL       time.Duration
	maxSeries       int
	lastCleanup     time.Time
	cleanupInterval time.Duration
}

type labeledCounterValue struct {
	counter     *atomic.Int64
	labelMap    map[string]string
	lastUpdated atomic.Int64
	metricType  MetricType
}

// NewLabeledCounterCollector creates a new labeled counter collector
func NewLabeledCounterCollector(name string, logger *zap.Logger) *LabeledCounterCollector {
	return &LabeledCounterCollector{
		BaseCollector:   NewBaseCollector(name, logger),
		values:          make(map[string]*labeledCounterValue),
		seriesTTL:       60 * time.Minute,
		maxSeries:       0, // 0 means no limit
		cleanupInterval: 5 * time.Minute,
		lastCleanup:     time.Time{},
	}
}

// SetTTL sets the TTL for time series
func (c *LabeledCounterCollector) SetTTL(ttl time.Duration) {
	c.mutex.Lock()
	c.seriesTTL = ttl
	c.mutex.Unlock()
}

// SetMaxSeries sets the maximum number of time series (0 means no limit)
func (c *LabeledCounterCollector) SetMaxSeries(n int) {
	c.mutex.Lock()
	c.maxSeries = n
	c.mutex.Unlock()
}

// Inc increments a labeled counter
func (c *LabeledCounterCollector) Inc(metricName string, labels ...string) {
	key := formatKey(metricName, labels)
	c.mutex.RLock()
	valueEntry, exists := c.values[key]
	c.mutex.RUnlock()

	now := time.Now().UnixNano()
	if !exists {
		c.mutex.Lock()
		if valueEntry, exists = c.values[key]; !exists {
			labelMap := make(map[string]string, len(labels)/2)
			for i := 0; i < len(labels); i += 2 {
				if i+1 < len(labels) {
					labelMap[labels[i]] = labels[i+1]
				}
			}
			valueEntry = &labeledCounterValue{
				counter:    &atomic.Int64{},
				labelMap:   labelMap,
				metricType: Counter,
			}
			valueEntry.lastUpdated.Store(now)
			c.values[key] = valueEntry
		}
		c.mutex.Unlock()
	}

	valueEntry.counter.Add(1)
	valueEntry.lastUpdated.Store(now)
	valueEntry.metricType = Counter
}

// Dec decrements a labeled counter
func (c *LabeledCounterCollector) Dec(metricName string, labels ...string) {
	key := formatKey(metricName, labels)
	c.mutex.RLock()
	valueEntry, exists := c.values[key]
	c.mutex.RUnlock()

	if exists {
		valueEntry.counter.Add(-1)
		valueEntry.lastUpdated.Store(time.Now().UnixNano())
		valueEntry.metricType = Counter
	}
}

// Set sets a labeled counter to a specific value
func (c *LabeledCounterCollector) Set(metricName string, value float64, labels ...string) {
	key := formatKey(metricName, labels)
	c.mutex.RLock()
	valueEntry, exists := c.values[key]
	c.mutex.RUnlock()

	now := time.Now().UnixNano()
	if !exists {
		c.mutex.Lock()
		if valueEntry, exists = c.values[key]; !exists {
			labelMap := make(map[string]string, len(labels)/2)
			for i := 0; i < len(labels); i += 2 {
				if i+1 < len(labels) {
					labelMap[labels[i]] = labels[i+1]
				}
			}
			valueEntry = &labeledCounterValue{
				counter:    &atomic.Int64{},
				labelMap:   labelMap,
				metricType: Gauge,
			}
			valueEntry.lastUpdated.Store(now)
			c.values[key] = valueEntry
		}
		c.mutex.Unlock()
	}

	valueEntry.counter.Store(int64(value))
	valueEntry.lastUpdated.Store(now)
	valueEntry.metricType = Gauge
}

// Get gets the current value of a labeled counter
func (c *LabeledCounterCollector) Get(metricName string, labels ...string) int64 {
	key := formatKey(metricName, labels)
	c.mutex.RLock()
	valueEntry, exists := c.values[key]
	c.mutex.RUnlock()

	if !exists {
		return 0
	}
	return valueEntry.counter.Load()
}

// Delete removes a specific labeled counter entry
func (c *LabeledCounterCollector) Delete(metricName string, labels ...string) {
	key := formatKey(metricName, labels)
	c.mutex.Lock()
	delete(c.values, key)
	c.mutex.Unlock()
}

// Collect implements Collector interface
func (c *LabeledCounterCollector) Collect() []Metric {
	c.mutex.RLock()
	now := time.Now()

	metrics := make([]Metric, 0, len(c.values))

	for key, valueEntry := range c.values {
		metricName, _ := parseKey(key)

		metrics = append(metrics, Metric{
			Name:       metricName,
			Value:      float64(valueEntry.counter.Load()),
			Labels:     valueEntry.labelMap,
			MetricType: valueEntry.metricType,
			Timestamp:  now,
		})
	}
	c.mutex.RUnlock()

	// Periodic cleanup
	if (c.seriesTTL > 0 || c.maxSeries > 0) && time.Since(c.lastCleanup) >= c.cleanupInterval {
		c.cleanup(now)
	}

	return metrics
}

func (c *LabeledCounterCollector) cleanup(now time.Time) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.lastCleanup = now

	// TTL cleanup
	if c.seriesTTL > 0 {
		cutoff := now.Add(-c.seriesTTL).UnixNano()
		for k, v := range c.values {
			if v.lastUpdated.Load() < cutoff {
				delete(c.values, k)
			}
		}
	}

	// Max series cleanup (LRU eviction)
	if c.maxSeries > 0 && len(c.values) > c.maxSeries {
		pairs := make([]struct {
			key  string
			last int64
		}, 0, len(c.values))
		for k, v := range c.values {
			pairs = append(pairs, struct {
				key  string
				last int64
			}{key: k, last: v.lastUpdated.Load()})
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].last < pairs[j].last })
		excess := len(c.values) - c.maxSeries
		for i := 0; i < excess; i++ {
			delete(c.values, pairs[i].key)
		}
	}
}

// HistogramCollector provides histogram metrics
type HistogramCollector struct {
	BaseCollector
	histograms map[string]*histogram
	mutex      sync.RWMutex
}

type histogram struct {
	buckets []float64
	counts  []atomic.Int64
	sum     float64
	count   atomic.Int64
	mutex   sync.RWMutex
}

// NewHistogramCollector creates a new histogram collector
func NewHistogramCollector(name string, logger *zap.Logger) *HistogramCollector {
	return &HistogramCollector{
		BaseCollector: NewBaseCollector(name, logger),
		histograms:    make(map[string]*histogram),
	}
}

// RegisterHistogram registers a histogram with specified buckets
func (h *HistogramCollector) RegisterHistogram(name string, buckets []float64) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	h.histograms[name] = &histogram{
		buckets: buckets,
		counts:  make([]atomic.Int64, len(buckets)+1), // +1 for infinity bucket
	}
}

// Observe records a value in a histogram
func (h *HistogramCollector) Observe(name string, value float64) {
	h.mutex.RLock()
	hist, exists := h.histograms[name]
	h.mutex.RUnlock()

	if !exists {
		h.mutex.Lock()
		if hist, exists = h.histograms[name]; !exists {
			// Default buckets for response times (microseconds)
			defaultBuckets := []float64{
				0.1, 0.25, 0.5, 0.75, 1.0, 1.5, 2.0, 2.5, 3.0, 3.5, 4.0, 4.5, 5.0, 7.5,
				10, 15, 22.5, 33.75, 50.625, 75.9375, 113.90625, 170.859375,
				256.2890625, 384.43359375, 576.650390625, 864.9755859375, 1297.46337890625,
				1946.195068359375, 2919.2926025390625, 4378.93890380859375, 6568.408355712891,
				9852.612533569336, 14778.918800354004, 22168.378200531006, 33252.5673007965,
				49878.85095119475, 74818.27642679212, 112227.41464018819, 168341.12196028227,
				252511.6829404234, 378767.5244106351, 568151.2866159527, 852226.9299239291,
				1278340.3948858936, 1917510.5923288405, 2876265.888493261, 4314398.832739891,
				6471598.249109836, 9707397.373664754, 14561096.060497131, 21841644.090745698,
				32762466.136118546, 49143699.20417782, 73715548.80626673,
			}
			hist = &histogram{
				buckets: defaultBuckets,
				counts:  make([]atomic.Int64, len(defaultBuckets)+1),
			}
			h.histograms[name] = hist
		}
		h.mutex.Unlock()
	}

	hist.mutex.Lock()
	hist.sum += value
	hist.mutex.Unlock()

	hist.count.Add(1)

	// Find appropriate bucket
	i := 0
	for i < len(hist.buckets) && value > hist.buckets[i] {
		i++
	}
	hist.counts[i].Add(1)
}

// Collect implements Collector interface
func (h *HistogramCollector) Collect() []Metric {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	now := time.Now()
	var metrics []Metric

	for name, hist := range h.histograms {
		// Sum metric
		metrics = append(metrics, Metric{
			Name:       name + "_sum",
			Value:      getSumSafely(hist),
			Labels:     map[string]string{},
			MetricType: Histogram,
			Timestamp:  now,
		})

		// Count metric
		metrics = append(metrics, Metric{
			Name:       name + "_count",
			Value:      float64(hist.count.Load()),
			Labels:     map[string]string{},
			MetricType: Histogram,
			Timestamp:  now,
		})

		// Bucket metrics
		buckets, counts := getBucketsAndCountsSafely(hist)
		cumulativeSum := int64(0)

		for i := 0; i < len(buckets); i++ {
			cumulativeSum += counts[i]

			le := "+Inf"
			if i < len(buckets)-1 {
				le = formatBucketLabel(buckets[i])
			}

			metrics = append(metrics, Metric{
				Name:       name + "_bucket",
				Value:      float64(cumulativeSum),
				Labels:     map[string]string{"le": le},
				MetricType: Histogram,
				Timestamp:  now,
			})
		}
	}

	return metrics
}

// Helper functions

// formatKey combines metric name and labels into a key
func formatKey(metricName string, labels []string) string {
	return metricName + "|" + strings.Join(labels, "|")
}

// parseKey parses a key back into metric name and labels
func parseKey(key string) (string, []string) {
	parts := strings.Split(key, "|")
	if len(parts) == 0 {
		return "", []string{}
	}
	return parts[0], parts[1:]
}

// getSumSafely safely gets histogram sum
func getSumSafely(h *histogram) float64 {
	h.mutex.RLock()
	defer h.mutex.RUnlock()
	return h.sum
}

// getBucketsAndCountsSafely safely gets histogram buckets and counts
func getBucketsAndCountsSafely(h *histogram) ([]float64, []int64) {
	counts := make([]int64, len(h.counts))
	for i := range h.counts {
		counts[i] = h.counts[i].Load()
	}
	return h.buckets, counts
}

// formatBucketLabel formats bucket label
func formatBucketLabel(value float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6g", value), "0"), ".")
}
