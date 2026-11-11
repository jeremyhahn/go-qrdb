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
)

// TestNewCounter_CacheHitWrongType exercises the type assertion failure path
// inside NewCounter by manually injecting an incorrect type into the metric cache
// for a key, then calling NewCounter with matching opts. This covers the second
// type-assertion-fail branch, which registers a new counter.
func TestNewCounter_CacheHitWrongType(t *testing.T) {
	freshRegistry(t)

	key := "counter:coverage:wrongtype:test_total"

	// Inject a non-Counter value into the cache for this key.
	metricCacheMu.Lock()
	metricCache[key] = "not-a-counter"
	metricCacheMu.Unlock()

	// NewCounter should see the cache hit but fail the type assertion,
	// and fall through to register a new counter.
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
