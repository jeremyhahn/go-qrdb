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
	"context"
	"crypto/tls"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/publicverbs"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestPublicHealth_RateLimited_REST drives GET /health on a real TLS
// listener with no client certificate: the route needs no login
// (publicverbs.Table), and the first call within the one-token bucket of the
// table's RateLimit dimension is served while the second is refused with 429
// and a Retry-After header before the handler runs.
func TestPublicHealth_RateLimited_REST(t *testing.T) {
	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	mw := authmw.New(allowAllAuthorizer{},
		authmw.WithRoleResolver(authtest.BuiltInRoles(t)),
		authmw.WithRateLimiter(limiter),
		authmw.WithRateLimitPolicies(authtest.OneToken(publicverbs.Table[publicverbs.VerbHealth].RateLimit)))

	served := 0
	adminSvc := &mockAdminService{
		healthFn: func(context.Context) (*service.HealthStatus, error) {
			served++
			return &service.HealthStatus{Status: "ok"}, nil
		},
	}
	tlsCfg, pool := generateTestTLSConfig(t)
	srv := NewServer(&mockKVService{}, adminSvc, dualStackAddr(t), WithTLS(tlsCfg), WithAuthMiddleware(mw))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	t.Cleanup(srv.Stop)

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13},
	}}
	_, path := publicverbs.RESTRoute(publicverbs.VerbHealth)
	get := func() (int, http.Header) {
		t.Helper()
		resp, err := client.Get("https://" + srv.Addr() + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		status, header := resp.StatusCode, resp.Header.Clone()
		_ = resp.Body.Close()
		return status, header
	}

	if status, _ := get(); status != http.StatusOK {
		t.Fatalf("first GET %s status = %d, want %d", path, status, http.StatusOK)
	}
	status, header := get()
	if status != http.StatusTooManyRequests {
		t.Fatalf("second GET %s status = %d, want %d", path, status, http.StatusTooManyRequests)
	}
	if header.Get("Retry-After") == "" {
		t.Fatal("refused GET carries no Retry-After header")
	}
	if served != 1 {
		t.Fatalf("handler served %d calls, want 1", served)
	}
}
