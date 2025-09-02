package monitor

import (
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Global monitor instance
var (
	globalManager         Manager
	globalCounters        *CounterCollector
	globalHistograms      *HistogramCollector
	globalLabeledCounters *LabeledCounterCollector
	initOnce              sync.Once
)

// Init initializes the global monitoring system
func Init(config Config) error {
	var initErr error

	initOnce.Do(func() {
		mgr, err := NewManager(config)
		if err != nil {
			initErr = err
			return
		}

		globalCounters = NewCounterCollector("counters", config.Logger)
		mgr.RegisterCollector(globalCounters)

		globalHistograms = NewHistogramCollector("histograms", config.Logger)
		mgr.RegisterCollector(globalHistograms)

		globalLabeledCounters = NewLabeledCounterCollector("labeled_counters", config.Logger)
		globalLabeledCounters.SetTTL(60 * time.Minute)
		mgr.RegisterCollector(globalLabeledCounters)

		if err := mgr.Start(); err != nil {
			initErr = err
			return
		}

		globalManager = mgr

		if config.Logger != nil {
			config.Logger.Info("monitor system initialized",
				zap.String("namespace", config.Namespace),
				zap.String("subsystem", config.Subsystem),
				zap.String("service", config.ServiceName))
		}
	})

	return initErr
}

// Counter functions

// IncrementCounter increments a counter by 1
func IncrementCounter(name string) {
	if globalCounters != nil {
		globalCounters.Inc(name)
	}
}

// DecrementCounter decrements a counter by 1
func DecrementCounter(name string) {
	if globalCounters != nil {
		globalCounters.Add(name, -1)
	}
}

// AddCounter adds a specific value to a counter
func AddCounter(name string, delta int64) {
	if globalCounters != nil {
		globalCounters.Add(name, delta)
	}
}

// SetCounter sets a counter to a specific value
func SetCounter(name string, value int64) {
	if globalCounters != nil {
		globalCounters.Set(name, value)
	}
}

// GetCounter gets the current value of a counter
func GetCounter(name string) int64 {
	if globalCounters != nil {
		return globalCounters.Get(name)
	}
	return 0
}

// Labeled counter functions

// IncrementLabeledCounter increments a labeled counter
// labels should be provided as [key1, value1, key2, value2, ...]
func IncrementLabeledCounter(name string, labels ...string) {
	if globalLabeledCounters != nil {
		globalLabeledCounters.Inc(name, labels...)
	}
}

// DecrementLabeledCounter decrements a labeled counter
func DecrementLabeledCounter(name string, labels ...string) {
	if globalLabeledCounters != nil {
		globalLabeledCounters.Dec(name, labels...)
	}
}

// SetLabeledCounter sets a labeled counter to a specific value
func SetLabeledCounter(name string, value float64, labels ...string) {
	if globalLabeledCounters != nil {
		globalLabeledCounters.Set(name, value, labels...)
	}
}

// GetLabeledCounter gets the current value of a labeled counter
func GetLabeledCounter(name string, labels ...string) int64 {
	if globalLabeledCounters != nil {
		return globalLabeledCounters.Get(name, labels...)
	}
	return 0
}

// DeleteLabeledCounter deletes a specific labeled counter
func DeleteLabeledCounter(name string, labels ...string) {
	if globalLabeledCounters != nil {
		globalLabeledCounters.Delete(name, labels...)
	}
}

// Histogram functions

// ObserveHistogram records a value in a histogram
func ObserveHistogram(name string, value float64) {
	if globalHistograms != nil {
		globalHistograms.Observe(name, value)
	}
}

// RegisterHistogramBuckets registers custom histogram buckets
func RegisterHistogramBuckets(name string, buckets []float64) {
	if globalHistograms != nil {
		globalHistograms.RegisterHistogram(name, buckets)
	}
}

// Collector registration

// RegisterCollector registers a custom metrics collector
func RegisterCollector(collector Collector) error {
	if globalManager == nil {
		return fmt.Errorf("global monitor is not initialized")
	}

	globalManager.RegisterCollector(collector)
	return nil
}

// Shutdown shuts down the global monitoring system
func Shutdown() {
	if globalManager != nil {
		globalManager.Stop()
		globalManager = nil
		globalCounters = nil
		globalHistograms = nil
		globalLabeledCounters = nil
	}
}

// HealthCheck performs a health check on the monitoring system
func HealthCheck() error {
	if globalManager == nil {
		return fmt.Errorf("monitor system not initialized")
	}

	// Try to get a simple metric to verify the system is working
	_ = GetCounter("monitor_heartbeat")
	return nil
}

// RefreshConnection attempts to refresh the remote write connection
// This is useful for DNS changes or network connectivity issues
func RefreshConnection() error {
	if globalManager == nil {
		return fmt.Errorf("monitor system not initialized")
	}

	// Get the underlying implementation to access refresh functionality
	if impl, ok := globalManager.(*managerImpl); ok {
		// Force a DNS refresh if DNS is enabled
		refreshed := impl.RefreshDNS(true)
		if refreshed {
			return nil
		}
		// If no refresh happened, it might be because DNS is not enabled or no target host
		// This is not necessarily an error
		return nil
	}

	return fmt.Errorf("unable to refresh connection: implementation not available")
}

// GetStatus returns the current status of the monitoring system
func GetStatus() map[string]interface{} {
	status := make(map[string]interface{})

	if globalManager == nil {
		status["initialized"] = false
		status["error"] = "monitor system not initialized"
		return status
	}

	status["initialized"] = true
	status["counters_available"] = globalCounters != nil
	status["histograms_available"] = globalHistograms != nil
	status["labeled_counters_available"] = globalLabeledCounters != nil

	// Get some basic metrics for health check
	if globalCounters != nil {
		status["total_connections"] = GetCounter("total_connections")
		status["heartbeat"] = GetCounter("monitor_heartbeat")
	}

	return status
}

// ForceWrite immediately writes all current metrics to the remote endpoint
// This is useful for health checks and testing
func ForceWrite() error {
	if globalManager == nil {
		return fmt.Errorf("monitor system not initialized")
	}

	// Get the underlying implementation to access write functionality
	if impl, ok := globalManager.(*managerImpl); ok {
		return impl.writeMetrics()
	}

	return fmt.Errorf("unable to force write: implementation not available")
}

// Factory functions for creating application-specific collectors

// NewApplicationCollector creates an application-specific collector
func NewApplicationCollector(name string, logger *zap.Logger) *LabeledCounterCollector {
	collector := NewLabeledCounterCollector(name, logger)
	if globalManager != nil {
		globalManager.RegisterCollector(collector)
	}
	return collector
}
