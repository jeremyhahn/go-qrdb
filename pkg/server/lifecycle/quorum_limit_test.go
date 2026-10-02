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

package lifecycle_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestQuorumBeginLimit_PoliciesByCaller pins the throttle every composer
// installs: the anonymous bucket admits its burst of 20 and then refuses
// with a retry delay, while an identified caller has a bucket of its own of
// 10. Keys are distinct per test run, so no other test spends them.
func TestQuorumBeginLimit_PoliciesByCaller(t *testing.T) {
	anon := t.Name() + "\x00anonymous"
	for i := 0; i < 20; i++ {
		if ok, _ := lifecycle.QuorumLimit(anon, true); !ok {
			t.Fatalf("anonymous request %d refused inside the burst", i+1)
		}
	}
	ok, retry := lifecycle.QuorumLimit(anon, true)
	if ok || retry <= 0 {
		t.Fatalf("anonymous request past the burst = %v, retry %v", ok, retry)
	}

	caller := t.Name() + "\x00tls\x00aa"
	for i := 0; i < 10; i++ {
		if ok, _ := lifecycle.QuorumLimit(caller, false); !ok {
			t.Fatalf("identified request %d refused inside the burst", i+1)
		}
	}
	if ok, _ := lifecycle.QuorumLimit(caller, false); ok {
		t.Fatal("identified request past the burst admitted")
	}
}

// TestBuildSealServicesOnly_InstallsQuorumLimiter pins that the
// composer gives the ceremony service, and so every holder authority built
// from its HolderDeps, the quorum-challenge throttle.
func TestBuildSealServicesOnly_InstallsQuorumLimiter(t *testing.T) {
	barrier, registry, credSvc := newTestBarrier(t)
	node, err := lifecycle.BuildSealServicesOnly(context.Background(), lifecycle.Infra{
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		Logger:            slog.Default(),
	})
	if err != nil {
		t.Fatalf("BuildSealServicesOnly: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()
	core, ok := service.CeremonyServiceCore(node.Ceremony)
	if !ok {
		t.Fatal("core ceremony service not exposed")
	}
	if core.HolderDeps().QuorumLimiter == nil {
		t.Fatal("no quorum-challenge throttle installed")
	}
}
