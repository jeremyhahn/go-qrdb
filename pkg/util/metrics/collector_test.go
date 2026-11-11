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
	if c == nil {
		t.Fatal("expected non-nil Collector with custom buckets")
	}
	// Verify it works by recording a duration.
	c.RecordRequestDuration("GET", "200", 50*time.Millisecond)
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
	// No panic = success; the histogram accepted both observations.
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
	// If underlying counter did not panic, requests were recorded.
}

// ---------------------------------------------------------------------------
// IncRequestsInFlight / DecRequestsInFlight
// ---------------------------------------------------------------------------

func Test_IncDecRequestsInFlight(t *testing.T) {
	freshRegistry(t)
	c := NewCollector(CollectorOpts{Namespace: "rif", Subsystem: "test"})
	c.IncRequestsInFlight("GET")
	c.IncRequestsInFlight("GET")
	c.DecRequestsInFlight("GET")
	// Net in-flight = 1 (we only verify no panic).
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

func Test_Timer_ObserveDuration_nilHistogram(t *testing.T) {
	timer := NewTimer(nil)
	d := timer.ObserveDuration()
	if d < 0 {
		t.Fatalf("expected non-negative duration with nil histogram, got %v", d)
	}
}

func Test_Timer_ObserveDuration_multipleCallsAccumulate(t *testing.T) {
	freshRegistry(t)
	h := NewHistogram(HistogramOpts{
		Namespace: "timer", Subsystem: "multi", Name: "dur", Help: "multi observe",
		Buckets: []float64{1.0},
	})
	t1 := NewTimer(h)
	t1.ObserveDuration()
	t2 := NewTimer(h)
	t2.ObserveDuration()
	got := histogramCount(h.histogram)
	if got != 2 {
		t.Fatalf("expected 2 observations from two timers, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// RequestTimer
// ---------------------------------------------------------------------------

func Test_RequestTimer_Done(t *testing.T) {
	freshRegistry(t)
	c := NewCollector(CollectorOpts{Namespace: "rt", Subsystem: "done"})
	timer := c.StartRequestTimer("GET")
	timer.Done("200")
	if timer.status != "200" {
		t.Fatalf("expected status '200', got %q", timer.status)
	}
	if timer.method != "GET" {
		t.Fatalf("expected method 'GET', got %q", timer.method)
	}
}

func Test_RequestTimer_DoneWithError(t *testing.T) {
	freshRegistry(t)
	c := NewCollector(CollectorOpts{Namespace: "rt", Subsystem: "err"})
	timer := c.StartRequestTimer("PUT")
	timer.DoneWithError("500", "internal_error")
	if timer.status != "500" {
		t.Fatalf("expected status '500', got %q", timer.status)
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
