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

//go:build !integration

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// freshRegistry installs a brand-new registry so every test starts clean.
func freshRegistry(t *testing.T) {
	t.Helper()
	SetRegistry(prometheus.NewRegistry())
}

// counterValue extracts the float64 value from a prometheus.Counter.
func counterValue(c prometheus.Counter) float64 {
	m := &dto.Metric{}
	if err := c.Write(m); err != nil {
		panic(err)
	}
	return m.GetCounter().GetValue()
}

// gaugeValue extracts the float64 value from a prometheus.Gauge.
func gaugeValue(g prometheus.Gauge) float64 {
	m := &dto.Metric{}
	if err := g.Write(m); err != nil {
		panic(err)
	}
	return m.GetGauge().GetValue()
}

// histogramCount extracts the sample count from a prometheus.Histogram.
func histogramCount(h prometheus.Histogram) uint64 {
	m := &dto.Metric{}
	if err := h.Write(m); err != nil {
		panic(err)
	}
	return m.GetHistogram().GetSampleCount()
}

// ---------------------------------------------------------------------------
// Counter
// ---------------------------------------------------------------------------

func Test_Counter_Inc(t *testing.T) {
	freshRegistry(t)
	c := NewCounter(CounterOpts{
		Namespace: "test", Subsystem: "counter", Name: "inc_total", Help: "inc test",
	})
	c.Inc()
	c.Inc()
	got := counterValue(c.counter)
	if got != 2 {
		t.Fatalf("expected counter value 2, got %v", got)
	}
}

func Test_Counter_Add(t *testing.T) {
	freshRegistry(t)
	c := NewCounter(CounterOpts{
		Namespace: "test", Subsystem: "counter", Name: "add_total", Help: "add test",
	})
	c.Add(3.5)
	c.Add(1.5)
	got := counterValue(c.counter)
	if got != 5 {
		t.Fatalf("expected counter value 5, got %v", got)
	}
}

func Test_Counter_cacheHit(t *testing.T) {
	freshRegistry(t)
	opts := CounterOpts{
		Namespace: "test", Subsystem: "counter", Name: "cache_total", Help: "cache test",
	}
	c1 := NewCounter(opts)
	c1.Inc()
	c2 := NewCounter(opts)
	// c2 wraps the same underlying counter, so Inc from c1 is visible
	got := counterValue(c2.counter)
	if got != 1 {
		t.Fatalf("expected cache-hit counter value 1, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Gauge
// ---------------------------------------------------------------------------

func Test_Gauge_Set(t *testing.T) {
	freshRegistry(t)
	g := NewGauge(GaugeOpts{
		Namespace: "test", Subsystem: "gauge", Name: "set_val", Help: "set test",
	})
	g.Set(42)
	got := gaugeValue(g.gauge)
	if got != 42 {
		t.Fatalf("expected gauge 42, got %v", got)
	}
}

func Test_Gauge_IncDec(t *testing.T) {
	freshRegistry(t)
	g := NewGauge(GaugeOpts{
		Namespace: "test", Subsystem: "gauge", Name: "incdec_val", Help: "incdec test",
	})
	g.Inc()
	g.Inc()
	g.Dec()
	got := gaugeValue(g.gauge)
	if got != 1 {
		t.Fatalf("expected gauge 1 after inc+inc+dec, got %v", got)
	}
}

func Test_Gauge_AddSub(t *testing.T) {
	freshRegistry(t)
	g := NewGauge(GaugeOpts{
		Namespace: "test", Subsystem: "gauge", Name: "addsub_val", Help: "addsub test",
	})
	g.Add(10)
	g.Sub(3)
	got := gaugeValue(g.gauge)
	if got != 7 {
		t.Fatalf("expected gauge 7, got %v", got)
	}
}

func Test_Gauge_cacheHit(t *testing.T) {
	freshRegistry(t)
	opts := GaugeOpts{
		Namespace: "test", Subsystem: "gauge", Name: "cache_val", Help: "cache test",
	}
	g1 := NewGauge(opts)
	g1.Set(99)
	g2 := NewGauge(opts)
	got := gaugeValue(g2.gauge)
	if got != 99 {
		t.Fatalf("expected cache-hit gauge 99, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Histogram
// ---------------------------------------------------------------------------

func Test_Histogram_Observe(t *testing.T) {
	freshRegistry(t)
	h := NewHistogram(HistogramOpts{
		Namespace: "test", Subsystem: "hist", Name: "obs_dur", Help: "obs test",
		Buckets: []float64{0.01, 0.1, 1.0},
	})
	h.Observe(0.05)
	h.Observe(0.5)
	got := histogramCount(h.histogram)
	if got != 2 {
		t.Fatalf("expected 2 observations, got %d", got)
	}
}

func Test_Histogram_defaultBuckets(t *testing.T) {
	freshRegistry(t)
	h := NewHistogram(HistogramOpts{
		Namespace: "test", Subsystem: "hist", Name: "def_dur", Help: "default buckets test",
	})
	h.Observe(0.001)
	got := histogramCount(h.histogram)
	if got != 1 {
		t.Fatalf("expected 1 observation with default buckets, got %d", got)
	}
}

func Test_Histogram_cacheHit(t *testing.T) {
	freshRegistry(t)
	opts := HistogramOpts{
		Namespace: "test", Subsystem: "hist", Name: "cache_dur", Help: "cache test",
		Buckets: []float64{0.1, 1.0},
	}
	h1 := NewHistogram(opts)
	h1.Observe(0.5)
	h2 := NewHistogram(opts)
	got := histogramCount(h2.histogram)
	if got != 1 {
		t.Fatalf("expected cache-hit histogram count 1, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// CounterVec
// ---------------------------------------------------------------------------

func Test_CounterVec_With(t *testing.T) {
	freshRegistry(t)
	cv := NewCounterVec(
		CounterOpts{Namespace: "test", Subsystem: "cv", Name: "with_total", Help: "with test"},
		[]string{"method", "status"},
	)
	cv.With("GET", "200").Inc()
	cv.With("GET", "200").Add(2)
	cv.With("POST", "500").Inc()

	got := counterValue(cv.counterVec.WithLabelValues("GET", "200"))
	if got != 3 {
		t.Fatalf("expected GET/200 counter 3, got %v", got)
	}
	got = counterValue(cv.counterVec.WithLabelValues("POST", "500"))
	if got != 1 {
		t.Fatalf("expected POST/500 counter 1, got %v", got)
	}
}

func Test_CounterVec_WithLabels(t *testing.T) {
	freshRegistry(t)
	cv := NewCounterVec(
		CounterOpts{Namespace: "test", Subsystem: "cv", Name: "labels_total", Help: "labels test"},
		[]string{"op"},
	)
	cv.WithLabels(prometheus.Labels{"op": "read"}).Inc()
	cv.WithLabels(prometheus.Labels{"op": "read"}).Inc()
	got := counterValue(cv.counterVec.With(prometheus.Labels{"op": "read"}))
	if got != 2 {
		t.Fatalf("expected op=read counter 2, got %v", got)
	}
}

func Test_CounterVec_cacheHit(t *testing.T) {
	freshRegistry(t)
	opts := CounterOpts{Namespace: "test", Subsystem: "cv", Name: "cache_total", Help: "cache test"}
	labels := []string{"l"}
	cv1 := NewCounterVec(opts, labels)
	cv1.With("a").Inc()
	cv2 := NewCounterVec(opts, labels)
	got := counterValue(cv2.counterVec.WithLabelValues("a"))
	if got != 1 {
		t.Fatalf("expected cache-hit counter 1, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// GaugeVec
// ---------------------------------------------------------------------------

func Test_GaugeVec_With(t *testing.T) {
	freshRegistry(t)
	gv := NewGaugeVec(
		GaugeOpts{Namespace: "test", Subsystem: "gv", Name: "with_val", Help: "with test"},
		[]string{"region"},
	)
	gv.With("us-east").Set(10)
	gv.With("us-west").Set(20)
	got := gaugeValue(gv.gaugeVec.WithLabelValues("us-east"))
	if got != 10 {
		t.Fatalf("expected us-east 10, got %v", got)
	}
	got = gaugeValue(gv.gaugeVec.WithLabelValues("us-west"))
	if got != 20 {
		t.Fatalf("expected us-west 20, got %v", got)
	}
}

func Test_GaugeVec_WithLabels(t *testing.T) {
	freshRegistry(t)
	gv := NewGaugeVec(
		GaugeOpts{Namespace: "test", Subsystem: "gv", Name: "labels_val", Help: "labels test"},
		[]string{"host"},
	)
	gv.WithLabels(prometheus.Labels{"host": "srv1"}).Set(5)
	gv.WithLabels(prometheus.Labels{"host": "srv1"}).Inc()
	got := gaugeValue(gv.gaugeVec.With(prometheus.Labels{"host": "srv1"}))
	if got != 6 {
		t.Fatalf("expected host=srv1 gauge 6, got %v", got)
	}
}

func Test_GaugeVec_cacheHit(t *testing.T) {
	freshRegistry(t)
	opts := GaugeOpts{Namespace: "test", Subsystem: "gv", Name: "cache_val", Help: "cache test"}
	labels := []string{"k"}
	gv1 := NewGaugeVec(opts, labels)
	gv1.With("x").Set(77)
	gv2 := NewGaugeVec(opts, labels)
	got := gaugeValue(gv2.gaugeVec.WithLabelValues("x"))
	if got != 77 {
		t.Fatalf("expected cache-hit gauge 77, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// HistogramVec
// ---------------------------------------------------------------------------

func Test_HistogramVec_With(t *testing.T) {
	freshRegistry(t)
	hv := NewHistogramVec(
		HistogramOpts{
			Namespace: "test", Subsystem: "hv", Name: "with_dur", Help: "with test",
			Buckets: []float64{0.01, 0.1, 1.0},
		},
		[]string{"op"},
	)
	hv.With("read").Observe(0.05)
	hv.With("read").Observe(0.5)
	obs := hv.histogramVec.WithLabelValues("read")
	m := &dto.Metric{}
	if err := obs.(prometheus.Histogram).Write(m); err != nil {
		t.Fatal(err)
	}
	got := m.GetHistogram().GetSampleCount()
	if got != 2 {
		t.Fatalf("expected 2 observations, got %d", got)
	}
}

func Test_HistogramVec_WithLabels(t *testing.T) {
	freshRegistry(t)
	hv := NewHistogramVec(
		HistogramOpts{
			Namespace: "test", Subsystem: "hv", Name: "labels_dur", Help: "labels test",
			Buckets: []float64{0.1, 1.0},
		},
		[]string{"method"},
	)
	hv.WithLabels(prometheus.Labels{"method": "GET"}).Observe(0.5)
	obs := hv.histogramVec.With(prometheus.Labels{"method": "GET"})
	m := &dto.Metric{}
	if err := obs.(prometheus.Histogram).Write(m); err != nil {
		t.Fatal(err)
	}
	got := m.GetHistogram().GetSampleCount()
	if got != 1 {
		t.Fatalf("expected 1 observation, got %d", got)
	}
}

func Test_HistogramVec_defaultBuckets(t *testing.T) {
	freshRegistry(t)
	hv := NewHistogramVec(
		HistogramOpts{
			Namespace: "test", Subsystem: "hv", Name: "def_dur", Help: "default buckets test",
		},
		[]string{"code"},
	)
	hv.With("200").Observe(0.01)
	obs := hv.histogramVec.WithLabelValues("200")
	m := &dto.Metric{}
	if err := obs.(prometheus.Histogram).Write(m); err != nil {
		t.Fatal(err)
	}
	got := m.GetHistogram().GetSampleCount()
	if got != 1 {
		t.Fatalf("expected 1 observation, got %d", got)
	}
}

func Test_HistogramVec_cacheHit(t *testing.T) {
	freshRegistry(t)
	opts := HistogramOpts{
		Namespace: "test", Subsystem: "hv", Name: "cache_dur", Help: "cache test",
		Buckets: []float64{0.1},
	}
	labels := []string{"a"}
	hv1 := NewHistogramVec(opts, labels)
	hv1.With("v").Observe(0.05)
	hv2 := NewHistogramVec(opts, labels)
	obs := hv2.histogramVec.WithLabelValues("v")
	m := &dto.Metric{}
	if err := obs.(prometheus.Histogram).Write(m); err != nil {
		t.Fatal(err)
	}
	got := m.GetHistogram().GetSampleCount()
	if got != 1 {
		t.Fatalf("expected cache-hit count 1, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// Registry / SetRegistry / MustRegister / Register / Unregister
// ---------------------------------------------------------------------------

func Test_Registry_returnsCurrentRegistry(t *testing.T) {
	freshRegistry(t)
	reg := Registry()
	if reg == nil {
		t.Fatal("expected non-nil registry")
	}
}

func Test_SetRegistry_resetsMetricCache(t *testing.T) {
	freshRegistry(t)
	opts := CounterOpts{Namespace: "test", Subsystem: "reg", Name: "reset_total", Help: "reset test"}
	c1 := NewCounter(opts)
	c1.Add(100)

	// Switch to a new registry, which clears the metric cache.
	SetRegistry(prometheus.NewRegistry())
	c2 := NewCounter(opts)
	got := counterValue(c2.counter)
	// New counter starts at 0 because it was re-registered in a new registry.
	if got != 0 {
		t.Fatalf("expected counter 0 after SetRegistry, got %v", got)
	}
}

func Test_SetRegistry_resetsCollectorCache(t *testing.T) {
	freshRegistry(t)
	col1 := NewCollector(CollectorOpts{Namespace: "test", Subsystem: "setregcol"})
	col1.IncActiveConnections()

	SetRegistry(prometheus.NewRegistry())
	col2 := NewCollector(CollectorOpts{Namespace: "test", Subsystem: "setregcol"})
	// After registry reset, col2 should be a new instance (different pointer).
	if col1 == col2 {
		t.Fatal("expected new Collector after SetRegistry, got cached instance")
	}
}

func Test_MustRegister_panicsOnDuplicate(t *testing.T) {
	freshRegistry(t)
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "test", Subsystem: "mr", Name: "dup_total", Help: "dup test",
	})
	MustRegister(c)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate MustRegister")
		}
	}()
	MustRegister(c)
}

func Test_Register_succeeds(t *testing.T) {
	freshRegistry(t)
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "test", Subsystem: "r", Name: "ok_total", Help: "register test",
	})
	if err := Register(c); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func Test_Register_returnsDuplicateError(t *testing.T) {
	freshRegistry(t)
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "test", Subsystem: "r", Name: "dup_total", Help: "dup test",
	})
	if err := Register(c); err != nil {
		t.Fatalf("first register failed: %v", err)
	}
	if err := Register(c); err == nil {
		t.Fatal("expected error on duplicate Register, got nil")
	}
}

func Test_Unregister_succeeds(t *testing.T) {
	freshRegistry(t)
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "test", Subsystem: "u", Name: "unreg_total", Help: "unregister test",
	})
	MustRegister(c)
	ok := Unregister(c)
	if !ok {
		t.Fatal("expected Unregister to return true")
	}
}

func Test_Unregister_returnsFalseForUnknown(t *testing.T) {
	freshRegistry(t)
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "test", Subsystem: "u", Name: "unknown_total", Help: "unknown test",
	})
	ok := Unregister(c)
	if ok {
		t.Fatal("expected Unregister to return false for unregistered collector")
	}
}
