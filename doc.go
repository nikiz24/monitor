// Package monitor provides a lightweight, high-performance metrics collection SDK
// for Go applications with Prometheus Remote Write support.
//
// Design goals:
//   - Minimal overhead and allocations for hot paths
//   - Thread-safe primitives built with atomic operations
//   - Bounded memory with TTL and max-series limits for labeled counters
//   - Prometheus-compatible format with standard labels
//
// Basic usage:
//
//	config := monitor.Config{
//	  Namespace:           "myapp",
//	  Subsystem:           "prod",
//	  ServiceName:         "service",
//	  RemoteWriteURL:      "http://prometheus:9090/api/v1/write",
//	  RemoteWriteInterval: 15 * time.Second,
//	}
//
//	if err := monitor.Init(config); err != nil {
//	  log.Fatal(err)
//	}
//	defer monitor.Shutdown()
//
//	monitor.IncrementCounter("requests_total")
//	monitor.SetLabeledCounter("connections", 42, "type", "websocket")
//	monitor.ObserveHistogram("response_time", 0.123)
package monitor
