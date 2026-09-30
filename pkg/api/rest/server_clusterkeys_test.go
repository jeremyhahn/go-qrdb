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
	"testing"
)

// fakeClusterKeyReceiver is a test double for ClusterKeyReceiver that
// records whether each handler was invoked.
type fakeClusterKeyReceiver struct {
	deliveryCalled bool
	nodeKeyCalled  bool
}

func (f *fakeClusterKeyReceiver) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		f.deliveryCalled = true
		w.WriteHeader(http.StatusOK)
	}
}

func (f *fakeClusterKeyReceiver) NodeKeyHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		f.nodeKeyCalled = true
		w.WriteHeader(http.StatusOK)
	}
}

// TestWithClusterKeyReceiver_RoutesRegistered verifies that both
// /v1/cluster/keydelivery (POST) and /v1/cluster/nodekey (GET) are
// registered and dispatch to the receiver's handlers when
// WithClusterKeyReceiver is supplied.
func TestWithClusterKeyReceiver_RoutesRegistered(t *testing.T) {
	t.Parallel()

	rcv := &fakeClusterKeyReceiver{}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithClusterKeyReceiver(rcv))

	t.Run("POST /v1/cluster/keydelivery", func(t *testing.T) {
		t.Parallel()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery", nil)
		srv.dispatch(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rr.Code)
		}
		if !rcv.deliveryCalled {
			t.Error("Handler() was not called")
		}
	})

	t.Run("GET /v1/cluster/nodekey", func(t *testing.T) {
		t.Parallel()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/cluster/nodekey", nil)
		srv.dispatch(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rr.Code)
		}
		if !rcv.nodeKeyCalled {
			t.Error("NodeKeyHandler() was not called")
		}
	})
}

// TestWithClusterKeyReceiver_NotRegisteredWhenNil verifies that when no
// ClusterKeyReceiver is supplied the cluster routes return 404.
func TestWithClusterKeyReceiver_NotRegisteredWhenNil(t *testing.T) {
	t.Parallel()

	srv := NewServer(nil, nil, "127.0.0.1:0")

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{"POST keydelivery", http.MethodPost, "/v1/cluster/keydelivery"},
		{"GET nodekey", http.MethodGet, "/v1/cluster/nodekey"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			srv.dispatch(rr, req)
			if rr.Code != http.StatusNotFound {
				t.Errorf("%s %s: expected 404, got %d", tc.method, tc.path, rr.Code)
			}
		})
	}
}

// TestWithClusterKeyReceiver_SealedWindowAllowlist verifies that
// /v1/cluster/ is in the sealed-window allowlist so the receiver can
// operate while the barrier is sealed.
func TestWithClusterKeyReceiver_SealedWindowAllowlist(t *testing.T) {
	t.Parallel()

	reqs := []struct{ method, path string }{
		{http.MethodPost, "/v1/cluster/keydelivery"},
		{http.MethodGet, "/v1/cluster/nodekey"},
	}
	for _, req := range reqs {
		t.Run(req.path, func(t *testing.T) {
			t.Parallel()
			if !requestInAllowlist(httptest.NewRequest(req.method, req.path, nil), SealedWindowAllowlist) {
				t.Errorf("%s %s is not on the SealedWindowAllowlist", req.method, req.path)
			}
		})
	}
}
