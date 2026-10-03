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
	"context"
	"testing"
	"time"
)

// mintJoinTokenAdmin is an admin double implementing
// service.ClusterJoinTokenMinter, recording the ttl it was called with so
// a test can prove whether the tool handler called it at all.
type mintJoinTokenAdmin struct {
	mockAdminService
	called  bool
	gotTTL  time.Duration
	token   string
	expires time.Time
	err     error
}

func (a *mintJoinTokenAdmin) MintJoinToken(_ context.Context, _ string, ttl time.Duration) (string, time.Time, error) {
	a.called = true
	a.gotTTL = ttl
	return a.token, a.expires, a.err
}

// TestMintJoinTokenTool_TTLOverflowRefused proves a ttl_seconds value
// large enough to overflow the seconds-to-duration conversion is refused
// BEFORE the tool handler ever calls the minter.
func TestMintJoinTokenTool_TTLOverflowRefused(t *testing.T) {
	admin := &mintJoinTokenAdmin{token: "tok", expires: time.Now().Add(time.Hour)}
	_, err := handleMintJoinToken(admin)(context.Background(), buildCallToolRequest(t, "qrdb_mint_join_token",
		map[string]any{"node_id": "node-1", "ttl_seconds": 9223372036854775807}))
	if err == nil {
		t.Fatal("qrdb_mint_join_token with an overflowing ttl_seconds succeeded, want a refusal")
	}
	if admin.called {
		t.Error("minter was called with an overflowing ttl_seconds; the guard did not refuse it first")
	}
}

// TestMintJoinTokenTool_OrdinaryTTLConverted proves an ordinary
// ttl_seconds value converts to the equivalent time.Duration and reaches
// the minter.
func TestMintJoinTokenTool_OrdinaryTTLConverted(t *testing.T) {
	admin := &mintJoinTokenAdmin{token: "tok", expires: time.Now().Add(time.Hour)}
	res, err := handleMintJoinToken(admin)(context.Background(), buildCallToolRequest(t, "qrdb_mint_join_token",
		map[string]any{"node_id": "node-1", "ttl_seconds": 3600}))
	if err != nil {
		t.Fatalf("qrdb_mint_join_token: %v", err)
	}
	if !admin.called || admin.gotTTL != time.Hour {
		t.Fatalf("minter called=%v ttl=%v, want called with 1h", admin.called, admin.gotTTL)
	}
	if got := resultText(t, res); got == "" {
		t.Fatal("expected a non-empty result text")
	}
}
