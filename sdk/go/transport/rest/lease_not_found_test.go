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
	"errors"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestMapOpsNotFoundError_ConvertsOnlyWhenMessageConfirmsIt is go-qrdb#591's
// review (non-blocking) red/green proof: REST's 404 is HTTP's own generic
// status, shared with a reverse proxy, load balancer or misrouted path, so
// a bare status-code check -- the test the other four remote transports'
// bespoke, application-only NotFound signals can safely rely on alone --
// is not enough here. A 404 naming a lease in the request but carrying an
// UNRELATED message (standing in for an infrastructure 404 that has
// nothing to do with this service) must be left exactly as-is, never
// reconstructed into *transport.LeaseNotFoundError merely because the
// request happened to name one.
func TestMapOpsNotFoundError_ConvertsOnlyWhenMessageConfirmsIt(t *testing.T) {
	leasedOps := []transport.Op{{Type: transport.OpPut, Key: []byte("k"), LeaseID: 42}}

	t.Run("404 with the server's own lease-not-found message converts", func(t *testing.T) {
		err := &transport.RequestError{
			Method:     "ExecuteTransaction",
			StatusCode: http.StatusNotFound,
			Err:        errors.New("service: lease not found: 42"),
		}
		got := mapOpsNotFoundError(leasedOps, err)
		var lnf *transport.LeaseNotFoundError
		if !errors.As(got, &lnf) {
			t.Fatalf("mapOpsNotFoundError = %T (%v), want *transport.LeaseNotFoundError", got, got)
		}
		if lnf.LeaseID != 42 {
			t.Errorf("LeaseID = %d, want 42", lnf.LeaseID)
		}
	})

	t.Run("404 with an unrelated message is left alone", func(t *testing.T) {
		err := &transport.RequestError{
			Method:     "ExecuteTransaction",
			StatusCode: http.StatusNotFound,
			Err:        errors.New("404 page not found"), // e.g. a reverse proxy's own catch-all
		}
		got := mapOpsNotFoundError(leasedOps, err)
		if got != err {
			t.Fatalf("mapOpsNotFoundError = %v, want the original error unchanged", got)
		}
		var lnf *transport.LeaseNotFoundError
		if errors.As(got, &lnf) {
			t.Fatal("an unrelated 404 must never be reconstructed as *transport.LeaseNotFoundError merely because the request named a lease")
		}
	})

	t.Run("404 with no op naming a lease is left alone even with the right message", func(t *testing.T) {
		err := &transport.RequestError{
			Method:     "ExecuteTransaction",
			StatusCode: http.StatusNotFound,
			Err:        errors.New("service: lease not found: 42"),
		}
		got := mapOpsNotFoundError([]transport.Op{{Type: transport.OpPut, Key: []byte("k")}}, err)
		if got != err {
			t.Fatalf("mapOpsNotFoundError = %v, want the original error unchanged", got)
		}
	})

	t.Run("non-404 status is left alone", func(t *testing.T) {
		err := &transport.RequestError{
			Method:     "ExecuteTransaction",
			StatusCode: http.StatusInternalServerError,
			Err:        errors.New("service: lease not found: 42"),
		}
		got := mapOpsNotFoundError(leasedOps, err)
		if got != err {
			t.Fatalf("mapOpsNotFoundError = %v, want the original error unchanged", got)
		}
	})
}
