package monitor

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// SystemMetricsCollector collects basic system metrics
type SystemMetricsCollector struct {
	BaseCollector
}

// NewSystemMetricsCollector creates a new system metrics collector
func NewSystemMetricsCollector(logger *zap.Logger) *SystemMetricsCollector {
	return &SystemMetricsCollector{
		BaseCollector: NewBaseCollector("system", logger),
	}
}

// Collect implements Collector interface
func (s *SystemMetricsCollector) Collect() []Metric {
	now := time.Now()
	var metrics []Metric

	// Memory stats
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	metrics = append(metrics, []Metric{
		{
			Name:       "memory_alloc_bytes",
			Value:      float64(ms.Alloc),
			Labels:     map[string]string{},
			MetricType: Gauge,
			Timestamp:  now,
		},
		{
			Name:       "memory_sys_bytes",
			Value:      float64(ms.Sys),
			Labels:     map[string]string{},
			MetricType: Gauge,
			Timestamp:  now,
		},
		{
			Name:       "memory_heap_alloc_bytes",
			Value:      float64(ms.HeapAlloc),
			Labels:     map[string]string{},
			MetricType: Gauge,
			Timestamp:  now,
		},
		{
			Name:       "memory_heap_inuse_bytes",
			Value:      float64(ms.HeapInuse),
			Labels:     map[string]string{},
			MetricType: Gauge,
			Timestamp:  now,
		},
		{
			Name:       "memory_heap_sys_bytes",
			Value:      float64(ms.HeapSys),
			Labels:     map[string]string{},
			MetricType: Gauge,
			Timestamp:  now,
		},
		{
			Name:       "memory_stack_inuse_bytes",
			Value:      float64(ms.StackInuse),
			Labels:     map[string]string{},
			MetricType: Gauge,
			Timestamp:  now,
		},
		{
			Name:       "memory_stack_sys_bytes",
			Value:      float64(ms.StackSys),
			Labels:     map[string]string{},
			MetricType: Gauge,
			Timestamp:  now,
		},
		{
			Name:       "goroutines_num",
			Value:      float64(runtime.NumGoroutine()),
			Labels:     map[string]string{},
			MetricType: Gauge,
			Timestamp:  now,
		},
		{
			Name:       "gc_runs_total",
			Value:      float64(ms.NumGC),
			Labels:     map[string]string{},
			MetricType: Counter,
			Timestamp:  now,
		},
		{
			Name:       "gc_pause_total_ns",
			Value:      float64(ms.PauseTotalNs),
			Labels:     map[string]string{},
			MetricType: Counter,
			Timestamp:  now,
		},
	}...)

	// Add RSS memory usage
	if rss := getProcessRSS(); rss > 0 {
		metrics = append(metrics, Metric{
			Name:       "memory_rss_bytes",
			Value:      float64(rss),
			Labels:     map[string]string{},
			MetricType: Gauge,
			Timestamp:  now,
		})
	}

	// Add file descriptor count
	if fdCount := getOpenFileDescriptors(); fdCount > 0 {
		metrics = append(metrics, Metric{
			Name:       "file_descriptors_num",
			Value:      float64(fdCount),
			Labels:     map[string]string{},
			MetricType: Gauge,
			Timestamp:  now,
		})
	}

	return metrics
}

// RegisterSystemMetricsCollector registers the system metrics collector with the global monitor
func RegisterSystemMetricsCollector(logger *zap.Logger) error {
	collector := NewSystemMetricsCollector(logger)
	return RegisterCollector(collector)
}

// GCStats represents garbage collection statistics
type GCStats struct {
	LastGC     time.Time
	NumGC      int64
	PauseTotal time.Duration
}

// ReadGCStats reads garbage collection statistics
func ReadGCStats() GCStats {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	return GCStats{
		LastGC:     time.Unix(0, int64(ms.LastGC)),
		NumGC:      int64(ms.NumGC),
		PauseTotal: time.Duration(ms.PauseTotalNs),
	}
}

// GetProcessMemory returns current process memory usage
func GetProcessMemory() (alloc, sys, heapInUse uint64) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.Alloc, ms.Sys, ms.HeapInuse
}

// TriggerGC triggers garbage collection
func TriggerGC() {
	runtime.GC()
}

// getProcessRSS returns the RSS (Resident Set Size) memory usage in bytes
func getProcessRSS() uint64 {
	// Try to read from /proc/self/status on Linux
	if data, err := os.ReadFile("/proc/self/status"); err == nil {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "VmRSS:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if kb, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
						return kb * 1024 // Convert KB to bytes
					}
				}
			}
		}
	}
	return 0
}

// getOpenFileDescriptors returns the number of open file descriptors
func getOpenFileDescriptors() uint64 {
	// Try to count files in /proc/self/fd on Linux
	if entries, err := os.ReadDir("/proc/self/fd"); err == nil {
		return uint64(len(entries))
	}
	return 0
}
