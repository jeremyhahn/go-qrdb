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
	"net/http"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward/replicaforwardtest"
)

// TestNewHTTPServer_IdleTimeoutOutlastsClients confirms the MCP listener sets
// its keep-alive idle timeout explicitly instead of inheriting ReadTimeout
// (5 s, net/http's fallback when IdleTimeout is zero), and that it outlasts
// the idle window clients keep connections for (net/http's DefaultTransport
// and the Go SDK: 90 s). A server that closes an idle connection first fails
// a client's tool call written onto it at that moment: every MCP call is a
// POST, which net/http never resends ("http: server closed idle connection").
func TestNewHTTPServer_IdleTimeoutOutlastsClients(t *testing.T) {
	const clientIdleConnTimeout = 90 * time.Second

	srv := newHTTPServer(http.NotFoundHandler())
	got := srv.IdleTimeout
	if got != serverIdleTimeout {
		t.Fatalf("http.Server IdleTimeout = %v, want %v", got, serverIdleTimeout)
	}
	if got <= srv.ReadTimeout {
		t.Fatalf("http.Server IdleTimeout = %v, want it above ReadTimeout %v", got, srv.ReadTimeout)
	}
	if got <= clientIdleConnTimeout {
		t.Fatalf("http.Server IdleTimeout = %v, want it above the clients' idle window %v",
			got, clientIdleConnTimeout)
	}
}

// TestReplicaForwarder_IdleConnTimeoutBelowServer confirms a forward retires
// an idle connection to a replica's MCP listener before that listener closes
// it, instead of keeping it forever (http.Transport's zero value).
func TestReplicaForwarder_IdleConnTimeoutBelowServer(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	f := newReplicaForwarder(replicaforward.Config{
		Locator: replicaforward.NewLocator(replicaforwardtest.Members{}, replicaforward.NewLeaderCache(), "raft-a", nil),
		RootCAs: ca.Pool,
		Timeout: time.Second,
	}, ca.TLS(t, "node-a"))
	defer f.close()

	got := f.base.IdleConnTimeout
	if got != forwardIdleConnTimeout {
		t.Fatalf("forward IdleConnTimeout = %v, want %v", got, forwardIdleConnTimeout)
	}
	if got <= 0 || got >= serverIdleTimeout {
		t.Fatalf("forward IdleConnTimeout = %v, want a positive value below the listener's idle timeout %v",
			got, serverIdleTimeout)
	}
}
