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

package rest

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestSealedWindowListener_NoRequestInFlightAcrossUnseal pins the gate's
// time-of-check/time-of-use window. The listener's gate samples the barrier
// when a request arrives; a request admitted while sealed must not still be
// running when the barrier unseals, or it runs anonymously against an
// unsealed barrier. An unsealing request therefore waits for the anonymous
// requests already in flight, and every request admitted after it sees the
// barrier unsealed and is refused.
func TestSealedWindowListener_NoRequestInFlightAcrossUnseal(t *testing.T) {
	probe := newFakeSealed(true)
	entered := make(chan struct{})
	release := make(chan struct{})
	var sealedAtEnd atomic.Bool
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/barrier/quorum/begin":
			close(entered)
			<-release
			sealedAtEnd.Store(probe.IsSealed())
		case "/v1/barrier/unseal":
			probe.unseal()
		}
		w.WriteHeader(http.StatusOK)
	})
	gate := SealedWindowListenerMiddleware(probe, nil)(h)

	statusDone := make(chan int, 1)
	go func() {
		rr := httptest.NewRecorder()
		gate.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/barrier/quorum/begin", nil))
		statusDone <- rr.Code
	}()
	<-entered

	unsealDone := make(chan int, 1)
	go func() {
		rr := httptest.NewRecorder()
		gate.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal", nil))
		unsealDone <- rr.Code
	}()
	select {
	case <-unsealDone:
		close(release)
		<-statusDone
		t.Fatal("the barrier unsealed while an anonymous request admitted under seal was still in flight")
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	if code := <-statusDone; code != http.StatusOK {
		t.Fatalf("in-flight quorum request: %d", code)
	}
	if !sealedAtEnd.Load() {
		t.Fatal("the in-flight request ran past the unseal")
	}
	select {
	case code := <-unsealDone:
		if code != http.StatusOK {
			t.Fatalf("unseal: %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unseal never ran after the in-flight request finished")
	}
	rr := httptest.NewRecorder()
	gate.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/barrier/status", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("a request after the unseal: %d, want 503", rr.Code)
	}
}
