// Copyright (c) 2026 Jeremy Hahn
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

package node

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// The SDK node's system barrier and every tenant barrier its registry builds
// record their events on the caller's trail, keyed by their tenant.
func TestBuildNodeSealSubsystem_BarriersRecordOnTheCallersTrail(t *testing.T) {
	ctx := context.Background()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = string(seal.StrategyNone)
	cfg.Seal.RootKeyPath = "root-key"
	trail := audit.NewRingBuffer(32)

	sub, err := buildNodeSealSubsystem(ctx, cfg, t.TempDir(), slog.Default(), nil, server.CryptoOptions{AuditLogger: trail})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.close() })
	if err := sub.barrier.Initialize(ctx, seal.Credentials{}); err != nil && !errors.Is(err, seal.ErrAlreadyInitialized) {
		t.Fatal(err)
	}
	if _, err := sub.barrierRegistry.RegisterTenant(ctx, "acme", seal.NewMemoryBackend(), seal.BarrierConfig{}, seal.NewNoneStrategy()); err != nil {
		t.Fatal(err)
	}
	if err := sub.barrierRegistry.InitializeTenant(ctx, "acme", seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, ev := range trail.Snapshot() {
		if ev.Resource == seal.BarrierAuditResource {
			name, _ := ev.Attributes["barrier"].(string)
			seen[ev.TenantID+"|"+name] = true
		}
	}
	if !seen[tenantid.System+"|system"] || !seen["acme|tenant:acme"] {
		t.Fatalf("barrier records = %v, want the system barrier's and acme's", seen)
	}
}
