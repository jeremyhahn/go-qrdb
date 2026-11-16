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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// goQRDBServerIdleTimeout is the go-qrdb REST server's http.Server
// IdleTimeout (pkg/api/rest serverIdleTimeout). The SDK is its own module
// and cannot import the server, so the value the client must stay below is
// pinned here.
const goQRDBServerIdleTimeout = 120 * time.Second

// TestIdleConnTimeout_ShorterThanServer confirms the client retires an idle
// keep-alive connection before the server closes it. Were the server first,
// a guarded-verb POST (CompareAndSwap, CompareAndDelete) written onto a
// connection the server is closing fails with "http: server closed idle
// connection", and net/http never resends a non-idempotent request.
func TestIdleConnTimeout_ShorterThanServer(t *testing.T) {
	rt, err := New(transport.WithAddress("http://127.0.0.1:1"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := rt.ensureClient(); err != nil {
		t.Fatalf("ensureClient: %v", err)
	}
	got := rt.httpTransport.IdleConnTimeout
	if got != idleConnTimeout {
		t.Fatalf("IdleConnTimeout = %v, want %v", got, idleConnTimeout)
	}
	if got <= 0 || got >= goQRDBServerIdleTimeout {
		t.Fatalf("IdleConnTimeout = %v, want a positive value below the server's idle timeout %v",
			got, goQRDBServerIdleTimeout)
	}
}
