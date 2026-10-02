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

// Package metrics provides a convenient wrapper around Prometheus metrics
// for collecting and exposing application metrics.
package metrics

import (
	"fmt"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// defaultRegistry is the default Prometheus registry used by this package.
	defaultRegistry = prometheus.NewRegistry()

	// registryMu protects access to the default registry.
	registryMu sync.RWMutex

	// metricCache deduplicates metric registrations within the same process & registry.
	metricCacheMu sync.RWMutex
	metricCache   = make(map[string]interface{})
)

func init() {
	// Register Go runtime collectors (go_goroutines, go_memstats, etc.)
	defaultRegistry.MustRegister(collectors.NewGoCollector())
	defaultRegistry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}

// Counter wraps a Prometheus counter metric.
type Counter struct {
	counter prometheus.Counter
}

// Inc increments the counter by 1.
func (c *Counter) Inc() {
	c.counter.Inc()
}

// Add adds the given value to the counter.
func (c *Counter) Add(delta float64) {
	c.counter.Add(delta)
}

// Gauge wraps a Prometheus gauge metric.
type Gauge struct {
	gauge prometheus.Gauge
}

// Set sets the gauge to the given value.
func (g *Gauge) Set(value float64) {
	g.gauge.Set(value)
}

// Inc increments the gauge by 1.
func (g *Gauge) Inc() {
	g.gauge.Inc()
}

// Dec decrements the gauge by 1.
func (g *Gauge) Dec() {
	g.gauge.Dec()
}

// Add adds the given value to the gauge.
func (g *Gauge) Add(delta float64) {
	g.gauge.Add(delta)
}

// Sub subtracts the given value from the gauge.
func (g *Gauge) Sub(delta float64) {
	g.gauge.Sub(delta)
}

// Histogram wraps a Prometheus histogram metric.
type Histogram struct {
	histogram prometheus.Histogram
}

// Observe adds a single observation to the histogram.
func (h *Histogram) Observe(value float64) {
	h.histogram.Observe(value)
}

// CounterVec wraps a Prometheus counter vector.
type CounterVec struct {
	counterVec *prometheus.CounterVec
}

// WithLabels returns a Counter for the given label values.
func (cv *CounterVec) WithLabels(labels prometheus.Labels) *Counter {
	return &Counter{counter: cv.counterVec.With(labels)}
}

// With returns a Counter for the given label values (variadic).
func (cv *CounterVec) With(labelValues ...string) *Counter {
	return &Counter{counter: cv.counterVec.WithLabelValues(labelValues...)}
}

// GaugeVec wraps a Prometheus gauge vector.
type GaugeVec struct {
	gaugeVec *prometheus.GaugeVec
}

// WithLabels returns a Gauge for the given label values.
func (gv *GaugeVec) WithLabels(labels prometheus.Labels) *Gauge {
	return &Gauge{gauge: gv.gaugeVec.With(labels)}
}

// With returns a Gauge for the given label values (variadic).
func (gv *GaugeVec) With(labelValues ...string) *Gauge {
	return &Gauge{gauge: gv.gaugeVec.WithLabelValues(labelValues...)}
}

// HistogramVec wraps a Prometheus histogram vector.
type HistogramVec struct {
	histogramVec *prometheus.HistogramVec
}

// WithLabels returns a Histogram for the given label values.
func (hv *HistogramVec) WithLabels(labels prometheus.Labels) *Histogram {
	observer := hv.histogramVec.With(labels)
	if h, ok := observer.(prometheus.Histogram); ok {
		return &Histogram{histogram: h}
	}
	// Should never happen with valid Prometheus usage
	panic("unexpected type assertion failure: observer is not prometheus.Histogram")
}

// With returns a Histogram for the given label values (variadic).
func (hv *HistogramVec) With(labelValues ...string) *Histogram {
	observer := hv.histogramVec.WithLabelValues(labelValues...)
	if h, ok := observer.(prometheus.Histogram); ok {
		return &Histogram{histogram: h}
	}
	// Should never happen with valid Prometheus usage
	panic("unexpected type assertion failure: observer is not prometheus.Histogram")
}

// CounterOpts defines options for creating a Counter.
type CounterOpts struct {
	Namespace   string
	Subsystem   string
	Name        string
	Help        string
	ConstLabels prometheus.Labels
}

// GaugeOpts defines options for creating a Gauge.
type GaugeOpts struct {
	Namespace   string
	Subsystem   string
	Name        string
	Help        string
	ConstLabels prometheus.Labels
}

// HistogramOpts defines options for creating a Histogram.
type HistogramOpts struct {
	Namespace   string
	Subsystem   string
	Name        string
	Help        string
	ConstLabels prometheus.Labels
	Buckets     []float64
}

// NewCounter creates and registers a new counter metric.
func NewCounter(opts CounterOpts) *Counter {
	registryMu.RLock()
	reg := defaultRegistry
	registryMu.RUnlock()

	key := "counter:" + opts.Namespace + ":" + opts.Subsystem + ":" + opts.Name
	// The lock is held across the lookup and the registration: two callers
	// asking for the same metric at once must not both register it.
	metricCacheMu.Lock()
	defer metricCacheMu.Unlock()
	if v, ok := metricCache[key]; ok {
		if c, ok := v.(prometheus.Counter); ok {
			return &Counter{counter: c}
		}
	}

	counter := promauto.With(reg).NewCounter(prometheus.CounterOpts{
		Namespace:   opts.Namespace,
		Subsystem:   opts.Subsystem,
		Name:        opts.Name,
		Help:        opts.Help,
		ConstLabels: opts.ConstLabels,
	})

	metricCache[key] = counter
	return &Counter{counter: counter}
}

// NewGauge creates and registers a new gauge metric.
func NewGauge(opts GaugeOpts) *Gauge {
	registryMu.RLock()
	reg := defaultRegistry
	registryMu.RUnlock()

	key := "gauge:" + opts.Namespace + ":" + opts.Subsystem + ":" + opts.Name
	metricCacheMu.Lock()
	defer metricCacheMu.Unlock()
	if v, ok := metricCache[key]; ok {
		if g, ok := v.(prometheus.Gauge); ok {
			return &Gauge{gauge: g}
		}
		panic("cached metric is not a prometheus.Gauge")
	}

	gauge := promauto.With(reg).NewGauge(prometheus.GaugeOpts{
		Namespace:   opts.Namespace,
		Subsystem:   opts.Subsystem,
		Name:        opts.Name,
		Help:        opts.Help,
		ConstLabels: opts.ConstLabels,
	})

	metricCache[key] = gauge
	return &Gauge{gauge: gauge}
}

// NewHistogram creates and registers a new histogram metric.
func NewHistogram(opts HistogramOpts) *Histogram {
	registryMu.RLock()
	reg := defaultRegistry
	registryMu.RUnlock()

	buckets := opts.Buckets
	if len(buckets) == 0 {
		buckets = prometheus.DefBuckets
	}

	key := "hist:" + opts.Namespace + ":" + opts.Subsystem + ":" + opts.Name + ":" + fmt.Sprint(buckets)
	metricCacheMu.Lock()
	defer metricCacheMu.Unlock()
	if v, ok := metricCache[key]; ok {
		if h, ok := v.(prometheus.Histogram); ok {
			return &Histogram{histogram: h}
		}
		panic("cached metric is not a prometheus.Histogram")
	}

	histogram := promauto.With(reg).NewHistogram(prometheus.HistogramOpts{
		Namespace:   opts.Namespace,
		Subsystem:   opts.Subsystem,
		Name:        opts.Name,
		Help:        opts.Help,
		ConstLabels: opts.ConstLabels,
		Buckets:     buckets,
	})
	metricCache[key] = histogram
	return &Histogram{histogram: histogram}
}

// NewCounterVec creates and registers a new counter vector metric.
func NewCounterVec(opts CounterOpts, labelNames []string) *CounterVec {
	registryMu.RLock()
	reg := defaultRegistry
	registryMu.RUnlock()

	key := "countervec:" + opts.Namespace + ":" + opts.Subsystem + ":" + opts.Name + ":" + fmt.Sprint(labelNames)

	// Use write lock to prevent race condition during registration
	metricCacheMu.Lock()
	defer metricCacheMu.Unlock()

	// Double-check cache after acquiring write lock
	if v, ok := metricCache[key]; ok {
		if cv, ok := v.(*prometheus.CounterVec); ok {
			return &CounterVec{counterVec: cv}
		}
		panic("cached metric is not a *prometheus.CounterVec")
	}

	// Not in cache - register new metric
	counterVec := promauto.With(reg).NewCounterVec(
		prometheus.CounterOpts{
			Namespace:   opts.Namespace,
			Subsystem:   opts.Subsystem,
			Name:        opts.Name,
			Help:        opts.Help,
			ConstLabels: opts.ConstLabels,
		},
		labelNames,
	)

	metricCache[key] = counterVec
	return &CounterVec{counterVec: counterVec}
}

// NewGaugeVec creates and registers a new gauge vector metric.
func NewGaugeVec(opts GaugeOpts, labelNames []string) *GaugeVec {
	registryMu.RLock()
	reg := defaultRegistry
	registryMu.RUnlock()

	key := "gaugevec:" + opts.Namespace + ":" + opts.Subsystem + ":" + opts.Name + ":" + fmt.Sprint(labelNames)

	// Use write lock to prevent race condition during registration
	metricCacheMu.Lock()
	defer metricCacheMu.Unlock()

	// Double-check cache after acquiring write lock
	if v, ok := metricCache[key]; ok {
		if gv, ok := v.(*prometheus.GaugeVec); ok {
			return &GaugeVec{gaugeVec: gv}
		}
		panic("cached metric is not a *prometheus.GaugeVec")
	}

	// Not in cache - register new metric
	gaugeVec := promauto.With(reg).NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace:   opts.Namespace,
			Subsystem:   opts.Subsystem,
			Name:        opts.Name,
			Help:        opts.Help,
			ConstLabels: opts.ConstLabels,
		},
		labelNames,
	)

	metricCache[key] = gaugeVec
	return &GaugeVec{gaugeVec: gaugeVec}
}

// NewHistogramVec creates and registers a new histogram vector metric.
func NewHistogramVec(opts HistogramOpts, labelNames []string) *HistogramVec {
	registryMu.RLock()
	reg := defaultRegistry
	registryMu.RUnlock()

	buckets := opts.Buckets
	if len(buckets) == 0 {
		buckets = prometheus.DefBuckets
	}

	key := "histvec:" + opts.Namespace + ":" + opts.Subsystem + ":" + opts.Name + ":" + fmt.Sprint(labelNames) + ":" + fmt.Sprint(buckets)

	// Use write lock to prevent race condition during registration
	metricCacheMu.Lock()
	defer metricCacheMu.Unlock()

	// Double-check cache after acquiring write lock
	if v, ok := metricCache[key]; ok {
		if hv, ok := v.(*prometheus.HistogramVec); ok {
			return &HistogramVec{histogramVec: hv}
		}
		panic("cached metric is not a *prometheus.HistogramVec")
	}

	// Not in cache - register new metric
	histogramVec := promauto.With(reg).NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace:   opts.Namespace,
			Subsystem:   opts.Subsystem,
			Name:        opts.Name,
			Help:        opts.Help,
			ConstLabels: opts.ConstLabels,
			Buckets:     buckets,
		},
		labelNames,
	)

	metricCache[key] = histogramVec
	return &HistogramVec{histogramVec: histogramVec}
}

// Registry returns the default Prometheus registry.
func Registry() *prometheus.Registry {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return defaultRegistry
}

// SetRegistry sets a custom Prometheus registry as the default.
func SetRegistry(registry *prometheus.Registry) {
	registryMu.Lock()
	defer registryMu.Unlock()
	defaultRegistry = registry
	// Reset cached collectors so new registry can re-register metrics without conflicts
	collectorsMu.Lock()
	collectorCache = make(map[string]*Collector)
	collectorsMu.Unlock()
	metricCacheMu.Lock()
	metricCache = make(map[string]interface{})
	metricCacheMu.Unlock()
}

// MustRegister registers the provided collectors with the default registry
// and panics if any error occurs.
func MustRegister(cs ...prometheus.Collector) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	defaultRegistry.MustRegister(cs...)
}

// Register registers the provided collectors with the default registry.
func Register(cs ...prometheus.Collector) error {
	registryMu.RLock()
	defer registryMu.RUnlock()

	for _, c := range cs {
		if err := defaultRegistry.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// Unregister removes the provided collector from the default registry.
func Unregister(collector prometheus.Collector) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return defaultRegistry.Unregister(collector)
}
