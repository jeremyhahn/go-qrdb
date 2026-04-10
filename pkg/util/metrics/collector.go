// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package metrics

import (
	"fmt"
	"sync"
	"time"
)

// Collector provides a convenient interface for collecting common application metrics.
type Collector struct {
	namespace string
	subsystem string

	// Request metrics
	requestDuration  *HistogramVec
	requestRate      *CounterVec
	requestsInFlight *GaugeVec

	// Error metrics
	errorRate *CounterVec

	// Connection metrics
	activeConnections *Gauge
}

// CollectorOpts defines options for creating a Collector.
type CollectorOpts struct {
	Namespace string
	Subsystem string

	// DurationBuckets defines the histogram buckets for request duration
	// If nil, uses default buckets: 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0, 5.0, 10.0 seconds.
	DurationBuckets []float64
}

// global cache to avoid duplicate registration panics when creating collectors
// with the same namespace/subsystem in the same process (e.g., across benchmarks).
var (
	collectorsMu   sync.RWMutex
	collectorCache = make(map[string]*Collector)
)

// defaultDurationBuckets returns the default histogram buckets for request duration.
// Covers typical request latencies from 1ms to 10s.
func defaultDurationBuckets() []float64 {
	return []float64{
		0.001, // 1ms
		0.005, // 5ms
		0.01,  // 10ms
		0.05,  // 50ms
		0.1,   // 100ms
		0.5,   // 500ms
		1.0,   // 1s
		5.0,   // 5s
		10.0,  // 10s
	}
}

// NewCollector creates a new metrics collector with common application metrics.
func NewCollector(opts CollectorOpts) *Collector {
	// Reuse existing collector for the same namespace/subsystem to prevent
	// duplicate metric registration in the shared registry.
	key := fmt.Sprintf("%p::%s::%s", Registry(), opts.Namespace, opts.Subsystem)
	collectorsMu.RLock()
	if existing, ok := collectorCache[key]; ok {
		collectorsMu.RUnlock()
		return existing
	}
	collectorsMu.RUnlock()

	buckets := opts.DurationBuckets
	if len(buckets) == 0 {
		buckets = defaultDurationBuckets()
	}

	c := &Collector{
		namespace: opts.Namespace,
		subsystem: opts.Subsystem,
	}

	// Request duration histogram (latency)
	c.requestDuration = NewHistogramVec(
		HistogramOpts{
			Namespace: opts.Namespace,
			Subsystem: opts.Subsystem,
			Name:      "request_duration_seconds",
			Help:      "Request duration in seconds",
			Buckets:   buckets,
		},
		[]string{"method", "status"},
	)

	// Request rate counter
	c.requestRate = NewCounterVec(
		CounterOpts{
			Namespace: opts.Namespace,
			Subsystem: opts.Subsystem,
			Name:      "requests_total",
			Help:      "Total number of requests",
		},
		[]string{"method", "status"},
	)

	// Requests in flight gauge
	c.requestsInFlight = NewGaugeVec(
		GaugeOpts{
			Namespace: opts.Namespace,
			Subsystem: opts.Subsystem,
			Name:      "requests_in_flight",
			Help:      "Current number of requests being processed",
		},
		[]string{"method"},
	)

	// Error rate counter
	c.errorRate = NewCounterVec(
		CounterOpts{
			Namespace: opts.Namespace,
			Subsystem: opts.Subsystem,
			Name:      "errors_total",
			Help:      "Total number of errors",
		},
		[]string{"type", "operation"},
	)

	// Active connections gauge
	c.activeConnections = NewGauge(
		GaugeOpts{
			Namespace: opts.Namespace,
			Subsystem: opts.Subsystem,
			Name:      "active_connections",
			Help:      "Current number of active connections",
		},
	)

	// Cache the collector instance for subsequent calls with same key
	collectorsMu.Lock()
	collectorCache[key] = c
	collectorsMu.Unlock()

	return c
}

// RecordRequestDuration records a request duration.
func (c *Collector) RecordRequestDuration(method, status string, duration time.Duration) {
	c.requestDuration.With(method, status).Observe(duration.Seconds())
}

// RecordRequest increments the request counter.
func (c *Collector) RecordRequest(method, status string) {
	c.requestRate.With(method, status).Inc()
}

// IncRequestsInFlight increments the in-flight requests gauge.
func (c *Collector) IncRequestsInFlight(method string) {
	c.requestsInFlight.With(method).Inc()
}

// DecRequestsInFlight decrements the in-flight requests gauge.
func (c *Collector) DecRequestsInFlight(method string) {
	c.requestsInFlight.With(method).Dec()
}

// RecordError increments the error counter.
func (c *Collector) RecordError(errorType, operation string) {
	c.errorRate.With(errorType, operation).Inc()
}

// SetActiveConnections sets the active connections gauge.
func (c *Collector) SetActiveConnections(count float64) {
	c.activeConnections.Set(count)
}

// IncActiveConnections increments the active connections gauge.
func (c *Collector) IncActiveConnections() {
	c.activeConnections.Inc()
}

// DecActiveConnections decrements the active connections gauge.
func (c *Collector) DecActiveConnections() {
	c.activeConnections.Dec()
}

// Timer provides a convenient way to time operations.
type Timer struct {
	start     time.Time
	histogram *Histogram
}

// NewTimer creates a new timer that will record to the given histogram when stopped.
func NewTimer(histogram *Histogram) *Timer {
	return &Timer{
		start:     time.Now(),
		histogram: histogram,
	}
}

// ObserveDuration records the duration since the timer was created.
func (t *Timer) ObserveDuration() time.Duration {
	duration := time.Since(t.start)
	if t.histogram != nil {
		t.histogram.Observe(duration.Seconds())
	}
	return duration
}

// RequestTimer provides a convenient way to time and record requests.
type RequestTimer struct {
	collector *Collector
	method    string
	status    string
	start     time.Time
}

// StartRequestTimer starts a new request timer.
func (c *Collector) StartRequestTimer(method string) *RequestTimer {
	c.IncRequestsInFlight(method)
	return &RequestTimer{
		collector: c,
		method:    method,
		start:     time.Now(),
	}
}

// Done records the request duration and updates all relevant metrics.
// Should be called with defer, e.g., defer timer.Done("200").
func (rt *RequestTimer) Done(status string) {
	rt.status = status
	duration := time.Since(rt.start)

	rt.collector.RecordRequestDuration(rt.method, status, duration)
	rt.collector.RecordRequest(rt.method, status)
	rt.collector.DecRequestsInFlight(rt.method)
}

// DoneWithError is a convenience method that records the request as an error
// and updates error metrics in addition to request metrics.
func (rt *RequestTimer) DoneWithError(status, errorType string) {
	rt.Done(status)
	rt.collector.RecordError(errorType, rt.method)
}

// LatencyBuckets returns common histogram bucket configurations.
type LatencyBuckets struct{}

// Buckets provides pre-configured latency bucket sets.
var Buckets LatencyBuckets

// Microseconds returns buckets suitable for very fast operations (1us to 1ms).
func (LatencyBuckets) Microseconds() []float64 {
	return []float64{
		0.000001, // 1us
		0.000005, // 5us
		0.00001,  // 10us
		0.00005,  // 50us
		0.0001,   // 100us
		0.0005,   // 500us
		0.001,    // 1ms
	}
}

// Milliseconds returns buckets suitable for fast operations (1ms to 1s).
func (LatencyBuckets) Milliseconds() []float64 {
	return []float64{
		0.001, // 1ms
		0.005, // 5ms
		0.01,  // 10ms
		0.025, // 25ms
		0.05,  // 50ms
		0.1,   // 100ms
		0.25,  // 250ms
		0.5,   // 500ms
		1.0,   // 1s
	}
}

// Seconds returns buckets suitable for slow operations (100ms to 60s).
func (LatencyBuckets) Seconds() []float64 {
	return []float64{
		0.1,  // 100ms
		0.5,  // 500ms
		1.0,  // 1s
		2.5,  // 2.5s
		5.0,  // 5s
		10.0, // 10s
		30.0, // 30s
		60.0, // 60s
	}
}

// Default returns the default buckets (1ms to 10s).
func (LatencyBuckets) Default() []float64 {
	return defaultDurationBuckets()
}

// Custom creates custom buckets with the given boundaries.
func (LatencyBuckets) Custom(boundaries ...float64) []float64 {
	return boundaries
}
