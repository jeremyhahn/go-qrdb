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

package mcp

import (
	"crypto/tls"
	"io"
	"net/http"
	"testing"
)

// TestMCP_AnonymousBodyCappedSmall: an MCP listener with no authentication
// -- the boot-time sealed-window listener, or a node with authentication off
// -- serves anonymous callers, and nothing it serves to them takes more than
// a small JSON-RPC document. Its body cap is about 1 MiB, not the batch-import
// bound (about 134 MiB) an authenticated caller gets.
func TestMCP_AnonymousBodyCappedSmall(t *testing.T) {
	tlsCfg := testTLSConfig(t)
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: tlsCfg.RootCAs, ServerName: "localhost", MinVersion: tls.VersionTLS13,
	}}}
	t.Cleanup(client.CloseIdleConnections)

	const total = int64(16 << 20)
	body := newStreamingBody(total)
	req, err := http.NewRequest(http.MethodPost, "https://"+srv.Addr()+"/mcp", body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if resp, err := client.Do(req); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	// The client's socket buffers absorb some bytes past the cap before the
	// server's refusal stops it; well under the 16 MiB sent means the server
	// stopped reading near the anonymous cap.
	if sent := body.sent.Load(); sent >= total {
		t.Fatalf("an anonymous MCP request was read to %d bytes; want it refused near 1 MiB", sent)
	}
}

// TestRequestBodyLimit: authenticated listeners get the batch-import bound;
// a listener without authentication, or the sealed-window listener, gets the
// anonymous bound.
func TestRequestBodyLimit(t *testing.T) {
	if got := (&Server{}).requestBodyLimit(); got != maxAnonymousRequestBodyBytes {
		t.Errorf("no authentication: %d, want %d", got, maxAnonymousRequestBodyBytes)
	}
	authed := newBearerTestServer(t, "")
	if got := authed.requestBodyLimit(); got != maxRequestBodyBytes {
		t.Errorf("authenticated: %d, want %d", got, maxRequestBodyBytes)
	}
	WithSealedWindowListener(&fakeMCPSealedProbe{sealed: true})(authed)
	if got := authed.requestBodyLimit(); got != maxAnonymousRequestBodyBytes {
		t.Errorf("sealed-window listener: %d, want %d", got, maxAnonymousRequestBodyBytes)
	}
}
