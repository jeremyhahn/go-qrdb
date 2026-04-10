//go:build !integration

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// TestNewCounter_CacheHitWrongType exercises the type assertion failure path
// inside NewCounter by manually injecting an incorrect type into the metric cache
// for a key, then calling NewCounter with matching opts. This covers the second
// metricCacheMu.RUnlock() inside the type-assertion-fail branch.
func TestNewCounter_CacheHitWrongType(t *testing.T) {
	freshRegistry(t)

	key := "counter:coverage:wrongtype:test_total"

	// Inject a non-Counter value into the cache for this key.
	metricCacheMu.Lock()
	metricCache[key] = "not-a-counter"
	metricCacheMu.Unlock()

	// NewCounter should see the cache hit but fail the type assertion,
	// hit the second RUnlock, and then fall through to register a new counter.
	c := NewCounter(CounterOpts{
		Namespace: "coverage",
		Subsystem: "wrongtype",
		Name:      "test_total",
		Help:      "wrong type cache test",
	})
	if c == nil {
		t.Fatal("NewCounter returned nil after wrong-type cache entry")
	}
	c.Inc()
	if counterValue(c.counter) != 1 {
		t.Errorf("expected counter value 1, got %v", counterValue(c.counter))
	}
}

// TestHistogramVec_WithLabels_ObservesCorrectly verifies that
// HistogramVec.WithLabels returns a working Histogram that records observations,
// covering the type-assertion success path in that method.
func TestHistogramVec_WithLabels_ObservesCorrectly(t *testing.T) {
	freshRegistry(t)
	hv := NewHistogramVec(
		HistogramOpts{
			Namespace: "coverage",
			Subsystem: "hvl",
			Name:      "withlabels_dur",
			Help:      "WithLabels coverage test",
			Buckets:   []float64{0.1, 1.0},
		},
		[]string{"endpoint"},
	)

	h := hv.WithLabels(prometheus.Labels{"endpoint": "/api/health"})
	if h == nil {
		t.Fatal("HistogramVec.WithLabels returned nil Histogram")
	}
	h.Observe(0.05)
	h.Observe(0.5)

	raw := hv.histogramVec.With(prometheus.Labels{"endpoint": "/api/health"})
	m := &dto.Metric{}
	if err := raw.(prometheus.Histogram).Write(m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if m.GetHistogram().GetSampleCount() != 2 {
		t.Errorf("expected 2 observations, got %d", m.GetHistogram().GetSampleCount())
	}
}

// TestHistogramVec_With_ObservesCorrectly verifies that HistogramVec.With
// returns a working Histogram for variadic label values, covering the
// type-assertion success path in that method.
func TestHistogramVec_With_ObservesCorrectly(t *testing.T) {
	freshRegistry(t)
	hv := NewHistogramVec(
		HistogramOpts{
			Namespace: "coverage",
			Subsystem: "hvw",
			Name:      "with_variadic_dur",
			Help:      "With variadic coverage test",
			Buckets:   []float64{0.01, 0.1},
		},
		[]string{"op"},
	)

	h := hv.With("list")
	if h == nil {
		t.Fatal("HistogramVec.With returned nil Histogram")
	}
	h.Observe(0.005)
	h.Observe(0.05)

	raw := hv.histogramVec.WithLabelValues("list")
	m := &dto.Metric{}
	if err := raw.(prometheus.Histogram).Write(m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if m.GetHistogram().GetSampleCount() != 2 {
		t.Errorf("expected 2 observations, got %d", m.GetHistogram().GetSampleCount())
	}
}
