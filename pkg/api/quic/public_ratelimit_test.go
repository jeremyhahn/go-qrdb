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

package quic

import (
	"crypto/tls"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/publicverbs"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// TestPublicHealth_RateLimited_QUIC drives admin.Health on a real QUIC
// listener from a caller with no client certificate: the method needs no
// login (publicverbs.Table), and with a one-token bucket on the table's
// RateLimit dimension the first call is served and the second is refused
// StatusResourceExhausted.
func TestPublicHealth_RateLimited_QUIC(t *testing.T) {
	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	bundle := generateMutualTLS(t)
	serverCfg := bundle.serverCfg.Clone()
	serverCfg.ClientAuth = tls.VerifyClientCertIfGiven
	bundle.serverCfg = serverCfg
	mw := authmw.New(
		&noopAuthorizer{},
		authmw.WithRoleResolver(authtest.BuiltInRoles(t)),
		authmw.WithAuthenticator(auth.NewMTLSAuthenticator()),
		authmw.WithRateLimiter(limiter),
		authmw.WithRateLimitPolicies(authtest.OneToken(publicverbs.Table[publicverbs.VerbHealth].RateLimit)),
	)
	env := startMutualTLSServer(t, &mockKVService{}, &mockAdminService{}, bundle, WithAuthMiddleware(mw))
	certless := bundle.clientCfg.Clone()
	certless.Certificates = nil
	conn := (&integrationEnv{srv: env.srv, clientCfg: certless}).dialServer(t)

	method := publicverbs.Table[publicverbs.VerbHealth].QUIC
	if _, status, _ := roundTrip(t, conn, method, 1, nil); status != wire.StatusOK {
		t.Fatalf("first %s status = %d, want StatusOK (%d)", method, status, wire.StatusOK)
	}
	if _, status, _ := roundTrip(t, conn, method, 2, nil); status != wire.StatusResourceExhausted {
		t.Fatalf("second %s status = %d, want StatusResourceExhausted (%d)", method, status, wire.StatusResourceExhausted)
	}
}
