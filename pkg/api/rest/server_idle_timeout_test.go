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

package rest

import (
	"testing"
	"time"
)

// TestNewServer_IdleTimeoutOutlastsClients confirms the server sets its
// keep-alive idle timeout explicitly instead of inheriting ReadTimeout (5 s,
// net/http's fallback when IdleTimeout is zero), and that it outlasts the
// idle window clients keep connections for (net/http's DefaultTransport and
// the Go SDK: 90 s). A server that closes an idle connection first fails a
// client's non-idempotent POST written onto it at that moment: net/http
// never resends it ("http: server closed idle connection").
func TestNewServer_IdleTimeoutOutlastsClients(t *testing.T) {
	const clientIdleConnTimeout = 90 * time.Second

	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0")
	got := srv.server.IdleTimeout
	if got != serverIdleTimeout {
		t.Fatalf("http.Server IdleTimeout = %v, want %v", got, serverIdleTimeout)
	}
	if got <= srv.server.ReadTimeout {
		t.Fatalf("http.Server IdleTimeout = %v, want it above ReadTimeout %v", got, srv.server.ReadTimeout)
	}
	if got <= clientIdleConnTimeout {
		t.Fatalf("http.Server IdleTimeout = %v, want it above the clients' idle window %v",
			got, clientIdleConnTimeout)
	}
}
