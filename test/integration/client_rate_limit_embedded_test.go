// Copyright 2026 Jeremy Hahn
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

//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestClientRateLimit_Embedded_PerPermissionPolicy drives the embedded
// transport's per-permission limiter end to end (go-qrdb#1197): an
// in-process node built with node.WithRateLimitPolicies admits the burst of
// a policy's permission through the node's own client and refuses the next
// call with the typed *authmw.RateLimitedError, carrying the permission and
// a positive RetryAfter. The embedded transport has no client address, so
// the per-permission Policy is its only limiter; the client-address limit
// is driven over the network listeners by TestClientRateLimit_CLI_PerProtocol.
func TestClientRateLimit_Embedded_PerPermissionPolicy(t *testing.T) {
	ctx := context.Background()

	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	// One challenge an hour: the first BeginLogin spends the burst and the
	// second is refused long before a token refills.
	policies := ratelimit.NewRegistry()
	policies.Register(permission.LoginBeginAttempt, ratelimit.Policy{RatePerSecond: 1.0 / 3600, Burst: 1})

	n, err := node.NewStandaloneNode(ctx, cfg, nil, node.WithRateLimitPolicies(policies))
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	login := n.Client().Login()
	req := transport.BeginLoginRequest{Type: transport.FactorTypePIV, TenantID: tenantid.System}
	if _, err := login.BeginLogin(ctx, req); err != nil {
		t.Fatalf("first BeginLogin within the burst: %v", err)
	}

	_, err = login.BeginLogin(ctx, req)
	var limited *authmw.RateLimitedError
	if !errors.As(err, &limited) {
		t.Fatalf("second BeginLogin over the burst: err = %v (%T), want *authmw.RateLimitedError", err, err)
	}
	if limited.Permission != permission.LoginBeginAttempt {
		t.Errorf("RateLimitedError.Permission = %q, want %q", limited.Permission, permission.LoginBeginAttempt)
	}
	if limited.RetryAfter <= 0 {
		t.Errorf("RateLimitedError.RetryAfter = %v, want > 0", limited.RetryAfter)
	}
}
