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

package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// The data-plane services of a store many databases share refuse the empty
// compaction prefix -- it would be every database at once -- and those of a
// dedicated raft group, which is one database's whole keyspace, take it.
func TestBuildDataPlaneServices_EmptyPrefixOnlyForADedicatedKeyspace(t *testing.T) {
	eng, err := engine.NewMVCCEngine(memory.NewStorage())
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	t.Cleanup(func() { _ = host.Close() })
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	ctx := context.Background()

	var unsafe *service.CompactPrefixInvalidError
	shared := buildDataPlaneServices(host, 1, hub, qrdbconfig.ImportSessionConfig{}).MVCC.(service.PrefixCompactor)
	if err := shared.CompactPrefix(ctx, "", 1); !errors.As(err, &unsafe) {
		t.Fatalf("a shared store's CompactPrefix(\"\") = %v, want *service.CompactPrefixInvalidError", err)
	}

	dedicated := buildDataPlaneServices(host, 1, hub, qrdbconfig.ImportSessionConfig{}, service.WithDedicatedKeyspace()).MVCC.(service.PrefixCompactor)
	if err := dedicated.CompactPrefix(ctx, "", 1); errors.As(err, &unsafe) {
		t.Fatalf("a dedicated store refused the empty prefix: %v", err)
	}
}

// TestBuildDataPlaneServices_ImportSessionConfigWired proves the
// importSessionCfg parameter actually reaches the constructed BatchService
// (go-qrdb#576: previously nothing called WithImportSessionLimits in
// production, so config.ImportSessionConfig -- "import_session:" in YAML --
// did nothing). A tenant limited to one concurrent session is refused a
// second ImportBegin only when that limit was actually threaded through.
func TestBuildDataPlaneServices_ImportSessionConfigWired(t *testing.T) {
	lr := lease.NewLessor()
	eng, err := engine.NewMVCCEngine(memory.NewStorage(), engine.WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	t.Cleanup(func() { _ = host.Close() })
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{TenantID: "acme", Subject: "alice"})

	svcs := buildDataPlaneServices(host, 1, hub, qrdbconfig.ImportSessionConfig{MaxSessionsPerTenant: 1})
	opts := service.ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row"}

	if _, err := svcs.Batch.ImportBegin(ctx, opts); err != nil {
		t.Fatalf("first ImportBegin: %v", err)
	}
	if _, err := svcs.Batch.ImportBegin(ctx, opts); !errors.As(err, new(*service.ImportQuotaExceededError)) {
		t.Fatalf("second ImportBegin error = %T (%v), want *service.ImportQuotaExceededError -- the configured MaxSessionsPerTenant was not applied", err, err)
	}
}

// TestBuildDataPlaneServices_ImportSession_LeaseExpiryReclaims proves an
// abandoned import session is reclaimed by the SAME lease-expiry mechanism
// every other lease-bound key already has, not by anything import-specific:
// once the session's lease's TTL genuinely elapses, revoking it -- exactly
// what the leader-side expiry sweeper (lease.Lessor.RunExpiry) does for any
// expired lease, driven here directly rather than by waiting out the
// sweeper's own poll interval -- deletes the staged chunk, and the session
// then reads back as gone (go-qrdb#576's also-fix list).
func TestBuildDataPlaneServices_ImportSession_LeaseExpiryReclaims(t *testing.T) {
	lr := lease.NewLessor()
	eng, err := engine.NewMVCCEngine(memory.NewStorage(), engine.WithMVCCLessor(lr))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	t.Cleanup(func() { _ = host.Close() })
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{TenantID: "acme", Subject: "alice"})

	// The minimum TTL Lessor.Grant accepts (whole seconds, > 0).
	svcs := buildDataPlaneServices(host, 1, hub, qrdbconfig.ImportSessionConfig{SessionTTLSeconds: 1})

	sessionID, err := svcs.Batch.ImportBegin(ctx, service.ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row"})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}
	if err := svcs.Batch.ImportAppend(ctx, sessionID, 0, []byte("id\nk1\n")); err != nil {
		t.Fatalf("ImportAppend: %v", err)
	}

	// Wait out the 1-second TTL -- no KeepAlive is sent, exactly as an
	// abandoned session (the caller crashed or never called
	// Commit/Abort/Append again) never renews one.
	time.Sleep(1100 * time.Millisecond)

	expired := lr.ExpiredLeaseIDs()
	if len(expired) != 1 {
		t.Fatalf("ExpiredLeaseIDs() = %v, want exactly the session's one lease", expired)
	}

	// Drive the SAME Revoke call RunExpiry's sweeper would issue for an
	// expired lease, through the ordinary LeaseServicer -- not ImportAbort.
	if err := svcs.Lease.Revoke(ctx, expired[0]); err != nil {
		t.Fatalf("Revoke(expired lease): %v", err)
	}

	// The session, and its staged chunk, are gone: a further Append reports
	// not-found, the same answer an explicit Abort's cascade gives.
	if err := svcs.Batch.ImportAppend(ctx, sessionID, 1, []byte("x")); !errors.As(err, new(*service.ImportSessionNotFoundError)) {
		t.Fatalf("ImportAppend after lease expiry error = %T (%v), want *service.ImportSessionNotFoundError", err, err)
	}
}
