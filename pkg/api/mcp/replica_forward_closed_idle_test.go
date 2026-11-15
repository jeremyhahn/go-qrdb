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

package mcp

import (
	"context"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward/replicaforwardtest"
	sdktransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/closedidletest"
)

// TestOnBehalfTransport_ServerClosedIdleAppliesOnce: a forwarded call written
// onto a pooled connection the target closes as idle, before reading it, is
// sent once more and applied exactly once -- not answered to the caller as
// the tool call's failure ("http: server closed idle connection").
func TestOnBehalfTransport_ServerClosedIdleAppliesOnce(t *testing.T) {
	srv := closedidletest.New(t)
	base := srv.Transport()
	defer base.CloseIdleConnections()
	srv.Warm(t, base)

	key, nodeID := forward.NodeIdentity(replicaforwardtest.NewCA(t).TLS(t, "node-a"))
	rt := &onBehalfTransport{base: base, caller: &auth.Identity{Subject: "alice", TenantID: "tenant-a"}, key: key, nodeID: nodeID}

	resp, err := (&http.Client{Transport: rt}).Do(srv.SwapRequest(t, srv.Arm(context.Background())))
	if err != nil {
		t.Fatalf("forwarded swap: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("forwarded swap answered %s, want 200", resp.Status)
	}
	if srv.Closes() != 1 || srv.Applied() != 1 {
		t.Fatalf("closes %d, applied %d; want the connection closed once and the swap applied once",
			srv.Closes(), srv.Applied())
	}
}

// TestOnBehalfTransport_AmbiguousFailureNotResent: a forwarded call the
// target applied and then dropped the connection on, without answering, is
// not resent; the error reaches the caller and the target saw it once.
func TestOnBehalfTransport_AmbiguousFailureNotResent(t *testing.T) {
	srv := closedidletest.New(t)
	base := srv.Transport()
	defer base.CloseIdleConnections()

	key, nodeID := forward.NodeIdentity(replicaforwardtest.NewCA(t).TLS(t, "node-a"))
	rt := &onBehalfTransport{base: base, caller: &auth.Identity{Subject: "alice", TenantID: "tenant-a"}, key: key, nodeID: nodeID}

	resp, err := (&http.Client{Transport: rt}).Do(srv.DropRequest(t, context.Background()))
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("dropped forwarded swap answered %s, want the broken connection's error", resp.Status)
	}
	if sdktransport.IsServerClosedIdle(err) {
		t.Fatalf("dropped forwarded swap error = %v, want an error other than server-closed-idle", err)
	}
	if srv.Received() != 1 || srv.Applied() != 1 {
		t.Fatalf("received %d, applied %d; want the swap seen once and applied once, not resent",
			srv.Received(), srv.Applied())
	}
}
