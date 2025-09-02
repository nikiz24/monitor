# Monitor SDK

A lightweight, high-performance metrics collection SDK for Go applications with Prometheus Remote Write support.

## Features

- **Multiple metric types**: Counters, Gauges, Histograms with labeled variants
- **Thread-safe design**: Built with atomic operations and minimal locking
- **Memory bounded**: TTL and max-series limits prevent unbounded growth
- **Prometheus compatible**: Standard `__name__`, `job`, `instance` labels
- **Advanced DNS**: Custom resolvers (UDP, DoT, DoH) with caching and failover
- **System metrics**: Built-in collectors for memory, GC, and goroutine stats

## Installation

```bash
go get github.com/yourorg/yourrepo/monitor
```

## Quick Start

```go
package main

import (
    "log"
    "time"
    
    "github.com/yourorg/yourrepo/monitor"
    "go.uber.org/zap"
)

func main() {
    logger, _ := zap.NewProduction()
    defer logger.Sync()

    config := monitor.Config{
        Namespace:           "myapp",
        Subsystem:           "prod",
        ServiceName:         "my_service",
        RemoteWriteURL:      "http://prometheus:9090/api/v1/write",
        RemoteWriteInterval: 15 * time.Second,
        Logger:              logger,
    }

    if err := monitor.Init(config); err != nil {
        log.Fatal("Failed to initialize monitoring:", err)
    }
    defer monitor.Shutdown()
    
    // Register system metrics
    monitor.RegisterSystemMetricsCollector(logger)
    
    // Use metrics
    monitor.IncrementCounter("requests_total")
    monitor.SetLabeledCounter("connections", 42, "type", "websocket")
    monitor.ObserveHistogram("response_time_us", 123.45)
}
```

## Basic Usage

### Counters

```go
// Simple counters
monitor.IncrementCounter("requests_total")
monitor.AddCounter("bytes_processed", 1024)
monitor.SetCounter("active_connections", 42)

// Get current value
count := monitor.GetCounter("requests_total")
```

### Labeled Counters

```go
// Increment with labels
monitor.IncrementLabeledCounter("http_requests", 
    "method", "GET", 
    "endpoint", "/api/users", 
    "status", "200")

// Set specific value
monitor.SetLabeledCounter("connected_users", 5, 
    "region", "us-west", 
    "type", "premium")

// Delete specific series
monitor.DeleteLabeledCounter("http_requests", 
    "method", "GET", 
    "endpoint", "/api/users")
```

### Histograms

```go
// Register custom buckets (optional)
monitor.RegisterHistogramBuckets("response_time", []float64{
    10, 20, 50, 100, 200, 500, 1000, 2000, 5000,
})

// Record observations
monitor.ObserveHistogram("response_time", 156.7)
```

## Advanced Configuration

### DNS Resolvers

For environments with custom DNS requirements:

```go
config := monitor.Config{
    // ... basic config ...
    
    // Enable advanced DNS resolution
    DNSEnable:          true,
    DNSCacheTTL:        10 * time.Minute,
    DNSRefreshInterval: 5 * time.Minute,
    DNSTimeout:         800 * time.Millisecond,
    
    // Custom resolvers (optional)
    DNSUDPServers:   []string{"1.1.1.1:53", "8.8.8.8:53"},
    DNSTLSServers:   []string{"1.1.1.1:853", "9.9.9.9:853"},
    DNSDoHEndpoints: []string{"https://cloudflare-dns.com/dns-query"},
}
```

### Custom Collectors

```go
type MyCollector struct {
    monitor.BaseCollector
}

func NewMyCollector(logger *zap.Logger) *MyCollector {
    return &MyCollector{
        BaseCollector: monitor.NewBaseCollector("my_collector", logger),
    }
}

func (c *MyCollector) Collect() []monitor.Metric {
    return []monitor.Metric{
        {
            Name:       "custom_metric",
            Value:      123.4,
            Labels:     map[string]string{"label1": "value1"},
            MetricType: monitor.Gauge,
            Timestamp:  time.Now(),
        },
    }
}

// Register the collector
collector := NewMyCollector(logger)
monitor.RegisterCollector(collector)
```

### Application-Specific Collectors

```go
// Create a labeled counter collector for your application
appCollector := monitor.NewApplicationCollector("http_requests", logger)

// Configure memory limits
appCollector.SetTTL(30 * time.Minute)    // Clean up after 30 minutes
appCollector.SetMaxSeries(50000)         // Limit to 50k time series

// Use it
appCollector.Inc("total", "method", "GET", "endpoint", "/api/users")
appCollector.Set("active", 42, "region", "us-west")
```

## System Metrics

The SDK includes built-in system metrics collection:

```go
// Register system metrics collector
monitor.RegisterSystemMetricsCollector(logger)

// Available metrics:
// - memory_alloc_bytes
// - memory_sys_bytes  
// - memory_heap_alloc_bytes
// - memory_heap_inuse_bytes
// - goroutines_total
// - gc_runs_total

// Get detailed GC stats
gcStats := monitor.ReadGCStats()
logger.Info("GC statistics",
    zap.Time("last_gc", gcStats.LastGC),
    zap.Int64("num_gc", gcStats.NumGC),
    zap.Duration("pause_total", gcStats.PauseTotal))

// Get memory usage
alloc, sys, heapInUse := monitor.GetProcessMemory()

// Trigger GC manually
monitor.TriggerGC()
```

## Prometheus Integration

Metrics are automatically formatted for Prometheus with:

- **Metric naming**: `{namespace}_{subsystem}_{metric_name}`
- **Standard labels**: `__name__`, `_instance_`, `instance`, `_target_`
- **Custom labels**: Added from `Config.CustomLabels`
- **Metric labels**: Added from individual metric calls

Example output:

```prometheus
myapp_prod_requests_total{__name__="myapp_prod_requests_total",_instance_="10.0.1.5",instance="10.0.1.5",_target_="my_service",method="GET",endpoint="/api/users"} 42
```

## Memory Management

Labeled counters support automatic cleanup to prevent memory leaks:

```go
collector := monitor.NewApplicationCollector("metrics", logger)

// Set TTL - series not updated within this time are removed
collector.SetTTL(60 * time.Minute)

// Set max series - oldest series are evicted when limit is exceeded  
collector.SetMaxSeries(100000)
```

## Best Practices

1. **Initialize early**: Set up monitoring at application startup
2. **Control cardinality**: Limit high-cardinality labels and use TTL/max-series
3. **Reasonable intervals**: Use 15-30 second write intervals
4. **Proper shutdown**: Always call `monitor.Shutdown()` on exit
5. **Monitor the monitor**: Watch for memory usage and performance impact

## Health Monitoring and Management

The SDK provides built-in health check and management functions:

### Health Check
```go
// Check if monitoring system is healthy
if err := monitor.HealthCheck(); err != nil {
    log.Printf("Monitor health check failed: %v", err)
}

// Get detailed system status
status := monitor.GetStatus()
log.Printf("Monitor status: %+v", status)
```

### Connection Management
```go
// Refresh DNS and recreate connections (useful for network changes)
if err := monitor.RefreshConnection(); err != nil {
    log.Printf("Connection refresh failed: %v", err)
}

// Force immediate metric write (useful for testing connectivity)
if err := monitor.ForceWrite(); err != nil {
    log.Printf("Force write failed: %v", err)
}
```

### Periodic Health Checks
```go
// Example: Periodic health monitoring
go func() {
    ticker := time.NewTicker(10 * time.Minute)
    defer ticker.Stop()
    
    for range ticker.C {
        if err := monitor.HealthCheck(); err != nil {
            logger.Error("Monitor unhealthy", zap.Error(err))
            continue
        }
        
        // Refresh connection for DNS changes
        monitor.RefreshConnection()
        
        // Test connectivity
        monitor.SetCounter("heartbeat", time.Now().Unix())
        if err := monitor.ForceWrite(); err != nil {
            logger.Error("Connectivity test failed", zap.Error(err))
        }
    }
}()
```

## Error Handling

The SDK is designed to be resilient:

- Failed remote writes are logged but don't crash the application
- DNS failures trigger automatic resolver failover and retry
- Memory limits prevent unbounded growth from high-cardinality metrics
- Thread-safe operations prevent data races
- Health check functions help detect and recover from issues

## License

MIT License - see LICENSE file for details.
