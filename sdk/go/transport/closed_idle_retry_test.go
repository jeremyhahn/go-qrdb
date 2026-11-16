// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package transport_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/closedidletest"
)

// TestServerClosedIdle_Reproduced pins the race itself: a POST handed an idle
// connection the server closes before the request is written fails with
// net/http's "server closed idle connection", and the server never applied
// it. net/http does not resend the POST on its own.
func TestServerClosedIdle_Reproduced(t *testing.T) {
	srv := closedidletest.New(t)
	rt := srv.Transport()
	defer rt.CloseIdleConnections()
	srv.Warm(t, rt)

	resp, err := rt.RoundTrip(srv.SwapRequest(t, srv.Arm(context.Background())))
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("swap on a server-closed idle connection answered %s, want the closed-idle error", resp.Status)
	}
	if !transport.IsServerClosedIdle(err) {
		t.Fatalf("swap error = %v, want net/http's server-closed-idle error", err)
	}
	if srv.Closes() != 1 || srv.Applied() != 0 {
		t.Fatalf("closes %d, applied %d; want 1 close and the swap never applied", srv.Closes(), srv.Applied())
	}
}

// TestRetryServerClosedIdle_AppliesOnce: through RetryServerClosedIdle the
// same guarded POST succeeds, applied exactly once.
func TestRetryServerClosedIdle_AppliesOnce(t *testing.T) {
	srv := closedidletest.New(t)
	base := srv.Transport()
	defer base.CloseIdleConnections()
	srv.Warm(t, base)
	client := &http.Client{Transport: transport.RetryServerClosedIdle(base)}

	resp, err := client.Do(srv.SwapRequest(t, srv.Arm(context.Background())))
	if err != nil {
		t.Fatalf("swap: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("swap answered %s, want 200", resp.Status)
	}
	if srv.Closes() != 1 || srv.Applied() != 1 {
		t.Fatalf("closes %d, applied %d; want the connection closed once and the swap applied once",
			srv.Closes(), srv.Applied())
	}
}

// TestRetryServerClosedIdle_UnreplayableBody: a body that cannot be produced
// again (no GetBody) is not resent; the caller gets the error.
func TestRetryServerClosedIdle_UnreplayableBody(t *testing.T) {
	srv := closedidletest.New(t)
	base := srv.Transport()
	defer base.CloseIdleConnections()
	srv.Warm(t, base)

	req := srv.SwapRequest(t, srv.Arm(context.Background()))
	req.Body = io.NopCloser(strings.NewReader(closedidletest.Initial))
	req.GetBody = nil
	resp, err := transport.RetryServerClosedIdle(base).RoundTrip(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("unreplayable swap answered %s, want the closed-idle error", resp.Status)
	}
	if !transport.IsServerClosedIdle(err) || srv.Applied() != 0 {
		t.Fatalf("error %v, applied %d; want the closed-idle error and nothing applied", err, srv.Applied())
	}
}

// TestRetryServerClosedIdle_AmbiguousFailureNotResent pins the retry's
// boundary: a POST the server applied and then dropped the connection on,
// without answering, fails with an error that is not the closed-idle one.
// The caller gets that error, and the server saw the request exactly once.
// Resending it would apply a committed compare-and-swap a second time.
func TestRetryServerClosedIdle_AmbiguousFailureNotResent(t *testing.T) {
	srv := closedidletest.New(t)
	base := srv.Transport()
	defer base.CloseIdleConnections()
	client := &http.Client{Transport: transport.RetryServerClosedIdle(base)}

	resp, err := client.Do(srv.DropRequest(t, context.Background()))
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("dropped swap answered %s, want the broken connection's error", resp.Status)
	}
	if transport.IsServerClosedIdle(err) {
		t.Fatalf("dropped swap error = %v, want an error other than server-closed-idle", err)
	}
	if srv.Received() != 1 || srv.Applied() != 1 {
		t.Fatalf("received %d, applied %d; want the swap seen once and applied once, not resent",
			srv.Received(), srv.Applied())
	}
}
