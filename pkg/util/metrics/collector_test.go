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
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// ---------------------------------------------------------------------------
// NewCollector
// ---------------------------------------------------------------------------

func Test_NewCollector_defaultBuckets(t *testing.T) {
	freshRegistry(t)
	c := NewCollector(CollectorOpts{
		Namespace: "app",
		Subsystem: "default",
	})
	if c == nil {
		t.Fatal("expected non-nil Collector")
	}
	if c.namespace != "app" {
		t.Fatalf("expected namespace 'app', got %q", c.namespace)
	}
	if c.subsystem != "default" {
		t.Fatalf("expected subsystem 'default', got %q", c.subsystem)
	}
	if c.requestDuration == nil {
		t.Fatal("expected non-nil requestDuration")
	}
	if c.requestRate == nil {
		t.Fatal("expected non-nil requestRate")
	}
	if c.requestsInFlight == nil {
		t.Fatal("expected non-nil requestsInFlight")
	}
	if c.errorRate == nil {
		t.Fatal("expected non-nil errorRate")
	}
	if c.activeConnections == nil {
		t.Fatal("expected non-nil activeConnections")
	}
}

func Test_NewCollector_customBuckets(t *testing.T) {
	freshRegistry(t)
	buckets := []float64{0.01, 0.1, 1.0}
	c := NewCollector(CollectorOpts{
		Namespace:       "app",
		Subsystem:       "custom",
		DurationBuckets: buckets,
	})
	c.RecordRequestDuration("GET", "200", 50*time.Millisecond)
	m := histogramVecMetric(t, c, "GET", "200")
	got := m.GetHistogram().GetBucket()
	if len(got) != len(buckets) {
		t.Fatalf("expected %d buckets, got %d", len(buckets), len(got))
	}
	for i, b := range got {
		if b.GetUpperBound() != buckets[i] {
			t.Fatalf("bucket[%d] upper bound = %v, want %v", i, b.GetUpperBound(), buckets[i])
		}
	}
}

// histogramVecMetric returns the request-duration sample for method/status.
func histogramVecMetric(t *testing.T, c *Collector, method, status string) *dto.Metric {
	t.Helper()
	m := &dto.Metric{}
	obs := c.requestDuration.histogramVec.WithLabelValues(method, status)
	if err := obs.(prometheus.Histogram).Write(m); err != nil {
		t.Fatal(err)
	}
	return m
}

func Test_NewCollector_cacheHit(t *testing.T) {
	freshRegistry(t)
	opts := CollectorOpts{Namespace: "app", Subsystem: "cached"}
	c1 := NewCollector(opts)
	c2 := NewCollector(opts)
	if c1 != c2 {
		t.Fatal("expected cached Collector to return same pointer")
	}
}

func Test_NewCollector_differentSubsystem_noCacheHit(t *testing.T) {
	freshRegistry(t)
	c1 := NewCollector(CollectorOpts{Namespace: "app", Subsystem: "one"})
	c2 := NewCollector(CollectorOpts{Namespace: "app", Subsystem: "two"})
	if c1 == c2 {
		t.Fatal("expected different Collectors for different subsystems")
	}
}

// ---------------------------------------------------------------------------
// RecordRequestDuration
// ---------------------------------------------------------------------------

func Test_RecordRequestDuration_records(t *testing.T) {
	freshRegistry(t)
	c := NewCollector(CollectorOpts{Namespace: "rrd", Subsystem: "test"})
	c.RecordRequestDuration("GET", "200", 100*time.Millisecond)
	c.RecordRequestDuration("GET", "200", 200*time.Millisecond)
	c.RecordRequestDuration("GET", "500", time.Second)
	h := histogramVecMetric(t, c, "GET", "200").GetHistogram()
	if h.GetSampleCount() != 2 {
		t.Fatalf("GET/200 sample count = %d, want 2", h.GetSampleCount())
	}
	if want := (300 * time.Millisecond).Seconds(); h.GetSampleSum() < want-1e-9 || h.GetSampleSum() > want+1e-9 {
		t.Fatalf("GET/200 sample sum = %v, want %v", h.GetSampleSum(), want)
	}
	if n := histogramVecMetric(t, c, "GET", "500").GetHistogram().GetSampleCount(); n != 1 {
		t.Fatalf("GET/500 sample count = %d, want 1", n)
	}
}

// ---------------------------------------------------------------------------
// RecordRequest
// ---------------------------------------------------------------------------

func Test_RecordRequest_increments(t *testing.T) {
	freshRegistry(t)
	c := NewCollector(CollectorOpts{Namespace: "rr", Subsystem: "test"})
	c.RecordRequest("GET", "200")
	c.RecordRequest("GET", "200")
	c.RecordRequest("POST", "500")
	if got := counterValue(c.requestRate.counterVec.WithLabelValues("GET", "200")); got != 2 {
		t.Fatalf("GET/200 requests = %v, want 2", got)
	}
	if got := counterValue(c.requestRate.counterVec.WithLabelValues("POST", "500")); got != 1 {
		t.Fatalf("POST/500 requests = %v, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// IncRequestsInFlight / DecRequestsInFlight
// ---------------------------------------------------------------------------

func Test_IncDecRequestsInFlight(t *testing.T) {
	freshRegistry(t)
	c := NewCollector(CollectorOpts{Namespace: "rif", Subsystem: "test"})
	c.IncRequestsInFlight("GET")
	c.IncRequestsInFlight("GET")
	c.IncRequestsInFlight("POST")
	c.DecRequestsInFlight("GET")
	if got := gaugeValue(c.requestsInFlight.gaugeVec.WithLabelValues("GET")); got != 1 {
		t.Fatalf("GET in flight = %v, want 1", got)
	}
	if got := gaugeValue(c.requestsInFlight.gaugeVec.WithLabelValues("POST")); got != 1 {
		t.Fatalf("POST in flight = %v, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// RecordError
// ---------------------------------------------------------------------------

func Test_RecordError_increments(t *testing.T) {
	freshRegistry(t)
	c := NewCollector(CollectorOpts{Namespace: "re", Subsystem: "test"})
	c.RecordError("timeout", "read")
	c.RecordError("timeout", "read")
	c.RecordError("not_found", "lookup")
	if got := counterValue(c.errorRate.counterVec.WithLabelValues("timeout", "read")); got != 2 {
		t.Fatalf("timeout/read errors = %v, want 2", got)
	}
	if got := counterValue(c.errorRate.counterVec.WithLabelValues("not_found", "lookup")); got != 1 {
		t.Fatalf("not_found/lookup errors = %v, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// SetActiveConnections / IncActiveConnections / DecActiveConnections
// ---------------------------------------------------------------------------

func Test_SetActiveConnections(t *testing.T) {
	freshRegistry(t)
	c := NewCollector(CollectorOpts{Namespace: "ac", Subsystem: "set"})
	c.SetActiveConnections(10)
	got := gaugeValue(c.activeConnections.gauge)
	if got != 10 {
		t.Fatalf("expected 10 active connections, got %v", got)
	}
}

func Test_SetActiveConnections_overwrite(t *testing.T) {
	freshRegistry(t)
	c := NewCollector(CollectorOpts{Namespace: "ac", Subsystem: "over"})
	c.SetActiveConnections(5)
	c.SetActiveConnections(3)
	got := gaugeValue(c.activeConnections.gauge)
	if got != 3 {
		t.Fatalf("expected 3 after overwrite, got %v", got)
	}
}

func Test_IncActiveConnections(t *testing.T) {
	freshRegistry(t)
	c := NewCollector(CollectorOpts{Namespace: "ac", Subsystem: "inc"})
	c.IncActiveConnections()
	c.IncActiveConnections()
	got := gaugeValue(c.activeConnections.gauge)
	if got != 2 {
		t.Fatalf("expected 2 after two Inc, got %v", got)
	}
}

func Test_DecActiveConnections(t *testing.T) {
	freshRegistry(t)
	c := NewCollector(CollectorOpts{Namespace: "ac", Subsystem: "dec"})
	c.IncActiveConnections()
	c.IncActiveConnections()
	c.DecActiveConnections()
	got := gaugeValue(c.activeConnections.gauge)
	if got != 1 {
		t.Fatalf("expected 1 after inc+inc+dec, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Timer
// ---------------------------------------------------------------------------

func Test_Timer_ObserveDuration_withHistogram(t *testing.T) {
	freshRegistry(t)
	h := NewHistogram(HistogramOpts{
		Namespace: "timer", Subsystem: "test", Name: "dur", Help: "timer test",
		Buckets: []float64{0.001, 0.01, 0.1, 1.0},
	})
	before := time.Now()
	timer := NewTimer(h)
	d := timer.ObserveDuration()
	if d < 0 || d > time.Since(before) {
		t.Fatalf("duration %v outside [0, %v]", d, time.Since(before))
	}
	m := &dto.Metric{}
	if err := h.histogram.Write(m); err != nil {
		t.Fatal(err)
	}
	if m.GetHistogram().GetSampleCount() != 1 {
		t.Fatalf("expected 1 observation after ObserveDuration, got %d", m.GetHistogram().GetSampleCount())
	}
	if m.GetHistogram().GetSampleSum() != d.Seconds() {
		t.Fatalf("observed %v, want the returned duration %v", m.GetHistogram().GetSampleSum(), d.Seconds())
	}
}

func Test_Timer_ObserveDuration_nilHistogram(t *testing.T) {
	before := time.Now()
	timer := NewTimer(nil)
	d := timer.ObserveDuration()
	if d < 0 || d > time.Since(before) {
		t.Fatalf("duration %v outside [0, %v]", d, time.Since(before))
	}
}

// ---------------------------------------------------------------------------
// RequestTimer
// ---------------------------------------------------------------------------

func Test_RequestTimer_Done(t *testing.T) {
	freshRegistry(t)
	c := NewCollector(CollectorOpts{Namespace: "rt", Subsystem: "done"})
	t1 := c.StartRequestTimer("GET")
	_ = c.StartRequestTimer("GET")
	if got := gaugeValue(c.requestsInFlight.gaugeVec.WithLabelValues("GET")); got != 2 {
		t.Fatalf("in flight after two starts = %v, want 2", got)
	}
	t1.Done("200")
	if got := gaugeValue(c.requestsInFlight.gaugeVec.WithLabelValues("GET")); got != 1 {
		t.Fatalf("in flight after Done = %v, want 1", got)
	}
	if got := counterValue(c.requestRate.counterVec.WithLabelValues("GET", "200")); got != 1 {
		t.Fatalf("GET/200 requests = %v, want 1", got)
	}
	if n := histogramVecMetric(t, c, "GET", "200").GetHistogram().GetSampleCount(); n != 1 {
		t.Fatalf("GET/200 duration samples = %d, want 1", n)
	}
}

func Test_RequestTimer_DoneWithError(t *testing.T) {
	freshRegistry(t)
	c := NewCollector(CollectorOpts{Namespace: "rt", Subsystem: "err"})
	timer := c.StartRequestTimer("PUT")
	timer.DoneWithError("500", "internal_error")
	if got := counterValue(c.requestRate.counterVec.WithLabelValues("PUT", "500")); got != 1 {
		t.Fatalf("PUT/500 requests = %v, want 1", got)
	}
	if got := counterValue(c.errorRate.counterVec.WithLabelValues("internal_error", "PUT")); got != 1 {
		t.Fatalf("internal_error/PUT errors = %v, want 1", got)
	}
	if got := gaugeValue(c.requestsInFlight.gaugeVec.WithLabelValues("PUT")); got != 0 {
		t.Fatalf("PUT in flight = %v, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// LatencyBuckets
// ---------------------------------------------------------------------------

func Test_LatencyBuckets_Microseconds(t *testing.T) {
	b := Buckets.Microseconds()
	if len(b) == 0 {
		t.Fatal("expected non-empty Microseconds buckets")
	}
	if b[0] != 0.000001 {
		t.Fatalf("expected first bucket 0.000001, got %v", b[0])
	}
	if b[len(b)-1] != 0.001 {
		t.Fatalf("expected last bucket 0.001, got %v", b[len(b)-1])
	}
}

func Test_LatencyBuckets_Microseconds_count(t *testing.T) {
	b := Buckets.Microseconds()
	if len(b) != 7 {
		t.Fatalf("expected 7 microsecond buckets, got %d", len(b))
	}
}

func Test_LatencyBuckets_Milliseconds(t *testing.T) {
	b := Buckets.Milliseconds()
	if len(b) == 0 {
		t.Fatal("expected non-empty Milliseconds buckets")
	}
	if b[0] != 0.001 {
		t.Fatalf("expected first bucket 0.001, got %v", b[0])
	}
	if b[len(b)-1] != 1.0 {
		t.Fatalf("expected last bucket 1.0, got %v", b[len(b)-1])
	}
}

func Test_LatencyBuckets_Milliseconds_count(t *testing.T) {
	b := Buckets.Milliseconds()
	if len(b) != 9 {
		t.Fatalf("expected 9 millisecond buckets, got %d", len(b))
	}
}

func Test_LatencyBuckets_Seconds(t *testing.T) {
	b := Buckets.Seconds()
	if len(b) == 0 {
		t.Fatal("expected non-empty Seconds buckets")
	}
	if b[0] != 0.1 {
		t.Fatalf("expected first bucket 0.1, got %v", b[0])
	}
	if b[len(b)-1] != 60.0 {
		t.Fatalf("expected last bucket 60.0, got %v", b[len(b)-1])
	}
}

func Test_LatencyBuckets_Seconds_count(t *testing.T) {
	b := Buckets.Seconds()
	if len(b) != 8 {
		t.Fatalf("expected 8 second buckets, got %d", len(b))
	}
}

func Test_LatencyBuckets_Default(t *testing.T) {
	b := Buckets.Default()
	def := defaultDurationBuckets()
	if len(b) != len(def) {
		t.Fatalf("expected Default() to match defaultDurationBuckets(): len %d vs %d", len(b), len(def))
	}
	for i := range b {
		if b[i] != def[i] {
			t.Fatalf("bucket[%d] mismatch: %v vs %v", i, b[i], def[i])
		}
	}
}

func Test_LatencyBuckets_Custom(t *testing.T) {
	input := []float64{0.5, 1.0, 2.0, 5.0}
	b := Buckets.Custom(input...)
	if len(b) != len(input) {
		t.Fatalf("expected %d custom buckets, got %d", len(input), len(b))
	}
	for i := range b {
		if b[i] != input[i] {
			t.Fatalf("bucket[%d] mismatch: %v vs %v", i, b[i], input[i])
		}
	}
}

func Test_LatencyBuckets_Custom_empty(t *testing.T) {
	b := Buckets.Custom()
	if len(b) != 0 {
		t.Fatalf("expected empty custom buckets, got %d", len(b))
	}
}

// ---------------------------------------------------------------------------
// defaultDurationBuckets
// ---------------------------------------------------------------------------

func Test_defaultDurationBuckets_returnsNineBuckets(t *testing.T) {
	b := defaultDurationBuckets()
	if len(b) != 9 {
		t.Fatalf("expected 9 default buckets, got %d", len(b))
	}
}

func Test_defaultDurationBuckets_ordered(t *testing.T) {
	b := defaultDurationBuckets()
	for i := 1; i < len(b); i++ {
		if b[i] <= b[i-1] {
			t.Fatalf("expected strictly increasing buckets, but bucket[%d]=%v <= bucket[%d]=%v", i, b[i], i-1, b[i-1])
		}
	}
}
