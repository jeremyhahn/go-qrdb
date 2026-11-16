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
	"crypto/tls"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/publicverbs"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// TestPublicHealth_RateLimited_MCP calls the public qrdb_health tool
// anonymously over a real MCP listener whose admin:health quota is one:
// the first call is served, the second is answered with the rate-limited
// tool error.
func TestPublicHealth_RateLimited_MCP(t *testing.T) {
	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	mw := authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)),
		authmw.WithAuthenticator(&bearerStubAuthenticator{token: "Bearer good"}),
		authmw.WithRateLimiter(limiter),
		authmw.WithRateLimitPolicies(authtest.OneToken(publicverbs.Table[publicverbs.VerbHealth].RateLimit)))
	tlsCfg := testTLSConfig(t)
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithTLS(tlsCfg), WithAuthMiddleware(mw))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: tlsCfg.RootCAs, ServerName: "localhost", MinVersion: tls.VersionTLS13,
	}}}
	t.Cleanup(client.CloseIdleConnections)
	s := &mcpSession{t: t, url: "https://" + srv.Addr() + "/mcp", client: client}
	s.initialize()

	tool := publicverbs.Table[publicverbs.VerbHealth].MCP
	if status, body := s.post(callToolBody(tool)); status != http.StatusOK || strings.Contains(body, `"isError":true`) {
		t.Fatalf("first %s: status %d, body %q", tool, status, body)
	}
	status, body := s.post(callToolBody(tool))
	if !strings.Contains(body, `"isError":true`) || !strings.Contains(body, "RESOURCE_EXHAUSTED") {
		t.Fatalf("second %s: status %d, body %q; want the rate-limited tool error", tool, status, body)
	}
}
