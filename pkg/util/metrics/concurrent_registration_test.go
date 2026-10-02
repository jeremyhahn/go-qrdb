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
	"sync"
	"testing"
)

// registerConcurrently calls register from many goroutines released at
// once, and fails the test with any panic one of them raised. Several nodes
// booting in one process build the same services at the same time, so two
// callers asking for the same metric concurrently is the ordinary case: both
// must get the one registered metric, and neither may panic with a duplicate
// registration.
func registerConcurrently(t *testing.T, register func()) {
	t.Helper()
	const callers = 64
	start := make(chan struct{})
	panics := make(chan any, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panics <- r
				}
			}()
			<-start
			register()
		}()
	}
	close(start)
	wg.Wait()
	close(panics)
	for r := range panics {
		t.Fatalf("concurrent registration of one metric panicked: %v", r)
	}
}

func Test_NewCounter_concurrentSameMetric(t *testing.T) {
	for range 20 {
		freshRegistry(t)
		registerConcurrently(t, func() {
			NewCounter(CounterOpts{Namespace: "test", Name: "concurrent_counter_total", Help: "h"}).Inc()
		})
	}
}

func Test_NewGauge_concurrentSameMetric(t *testing.T) {
	for range 20 {
		freshRegistry(t)
		registerConcurrently(t, func() {
			NewGauge(GaugeOpts{Namespace: "test", Name: "concurrent_gauge", Help: "h"}).Inc()
		})
	}
}

func Test_NewHistogram_concurrentSameMetric(t *testing.T) {
	for range 20 {
		freshRegistry(t)
		registerConcurrently(t, func() {
			NewHistogram(HistogramOpts{Namespace: "test", Name: "concurrent_histogram", Help: "h"}).Observe(1)
		})
	}
}
