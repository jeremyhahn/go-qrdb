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

package node

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
)

// TestCredentialRotationScheduler_SealedNodeRotatesNothing proves the
// rotation scheduler of a running standalone memory node stops at a seal:
// once the system barrier is sealed, a policy that falls due draws no value
// from the rotation source and writes no version. The first rotation, before
// the seal, proves the scheduler is live and its period reached it. The
// scheduler's own seal gate is pinned by the pkg/server unit tests; here the
// whole node is held to the outcome.
func TestCredentialRotationScheduler_SealedNodeRotatesNothing(t *testing.T) {
	ctx := context.Background()
	const period = 20 * time.Millisecond
	const interval = 200 * time.Millisecond

	var drawn atomic.Int64
	src := func(context.Context, string) (*secure.Buffer, error) {
		drawn.Add(1)
		return secure.NewBufferFromBytes([]byte("rotated-value"))
	}
	cfg := sealedStandaloneConfig(t, t.TempDir())
	cfg.Databases[0].Backend = string(database.BackendMemory)
	n, err := NewStandaloneNode(ctx, cfg,
		[]server.ServerOption{server.WithCredentialRotationPeriod(period)},
		WithSealPassphrase([]byte(nodeSealPass)), WithCredentialRotationValueSource(src))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })

	svc := n.Services().Credential
	if err := svc.CredentialStore(ctx, "db-pass", []byte("initial-value")); err != nil {
		t.Fatalf("CredentialStore: %v", err)
	}
	policy := credentials.RotationPolicy{Enabled: true, Interval: interval, RetainVersions: 1}
	if err := svc.CredentialSetRotationPolicy(ctx, "db-pass", policy); err != nil {
		t.Fatalf("CredentialSetRotationPolicy: %v", err)
	}

	deadline := time.After(30 * time.Second)
	for drawn.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("the scheduler never rotated the due policy on the unsealed node")
		case <-time.After(period):
		}
	}

	if err := n.BarrierRegistry().SystemBarrier().Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Seal returns once an in-flight pass has finished; from here on no
	// pass may draw, though the policy falls due again within interval.
	sealedAt := drawn.Load()
	<-time.After(3 * interval)
	if got := drawn.Load(); got != sealedAt {
		t.Fatalf("rotation source drawn %d times after the seal, want 0", got-sealedAt)
	}
}
