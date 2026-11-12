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

package lifecycle_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestBuildServices_BindsTheSealBarrierOnAnUnencryptedDataPlane proves a
// node whose data plane does not encrypt at rest (a standalone objstore
// backend) boots with the system seal barrier, which encrypts, bound as the
// credential store: its legacy records are kept and new credentials are
// written through the barrier.
func TestBuildServices_BindsTheSealBarrierOnAnUnencryptedDataPlane(t *testing.T) {
	ctx := context.Background()
	host := newLocalHost()
	defer func() { _ = host.Close() }()
	hub := watch.NewHub()
	defer hub.Close()
	barrier, registry, credSvc := newTestBarrier(t)

	const legacyKey = "credentials/db-password"
	want := []byte("legacy-secret")
	if err := barrier.Put(ctx, legacyKey, want); err != nil {
		t.Fatalf("barrier.Put: %v", err)
	}

	node, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		Backend:           database.BackendObjstore,
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
	})
	if err != nil {
		t.Fatalf("BuildServices on an objstore data plane: %v", err)
	}
	defer func() { _ = node.Stop(ctx) }()

	// The legacy record is still in the barrier: binding it lost nothing.
	got, err := barrier.Get(ctx, legacyKey)
	if err != nil {
		t.Fatalf("legacy credential after the bind: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("legacy credential = %q, want %q", got, want)
	}

	// A new credential is stored by the barrier, which encrypts it.
	fresh := []byte("fresh-secret")
	if err := credSvc.StoreCredential(ctx, "api-key", fresh); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	got, err = barrier.Get(ctx, "__sys:credentials/api-key")
	if err != nil {
		t.Fatalf("barrier.Get new credential: %v", err)
	}
	if !bytes.Equal(got, fresh) {
		t.Fatalf("new credential in the barrier = %q, want %q", got, fresh)
	}
}

// TestBuildServices_MemoryBackendCredentialsSurviveARebuild proves a
// standalone sealed memory node binds the seal barrier as the credential
// store: a credential already in the seal store at the bind, and the
// versions rotated after it, are all there when a node is rebuilt from the
// same seal store. Bound to the ephemeral system KV, the bind's legacy
// migration moved the seal-store record out and lost it.
func TestBuildServices_MemoryBackendCredentialsSurviveARebuild(t *testing.T) {
	ctx := context.Background()
	barrier, registry, credSvc := newTestBarrier(t)

	const name = "db-password"
	// Written before the bind, the credential is in the durable seal store.
	legacy := []byte("legacy-secret")
	if err := credSvc.StoreCredential(ctx, name, legacy); err != nil {
		t.Fatalf("StoreCredential before the bind: %v", err)
	}

	build := func(svc *credentials.CredentialService) *lifecycle.Node {
		t.Helper()
		host := newLocalHost()
		t.Cleanup(func() { _ = host.Close() })
		hub := watch.NewHub()
		t.Cleanup(hub.Close)
		node, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
			Host:              host,
			WatchHub:          hub,
			DataDir:           t.TempDir(),
			Backend:           database.BackendMemory,
			SealBarrier:       barrier,
			BarrierRegistry:   registry,
			CredentialCoreSvc: svc,
		})
		if err != nil {
			t.Fatalf("BuildServices on a memory data plane: %v", err)
		}
		return node
	}
	holds := func(svc *credentials.CredentialService, version uint64, want []byte) {
		t.Helper()
		buf, err := svc.RetrieveCredentialVersionBuffer(ctx, name, version)
		if err != nil {
			t.Fatalf("version %d: %v", version, err)
		}
		ok, err := buf.EqualBytes(want)
		buf.Destroy()
		if err != nil || !ok {
			t.Fatalf("version %d does not hold %q (err %v)", version, want, err)
		}
	}

	first := build(credSvc)
	// The bound store still serves the record written before the bind.
	holds(credSvc, 0, legacy)

	values := map[uint64][]byte{1: legacy}
	for _, v := range [][]byte{[]byte("rotated-1"), []byte("rotated-2")} {
		res, err := credSvc.RotateCredential(ctx, name, credentials.RotateOptions{
			NewValue:    v,
			GracePeriod: time.Hour,
		})
		if err != nil {
			t.Fatalf("RotateCredential: %v", err)
		}
		values[res.NewVersion] = v
	}
	before, err := credSvc.ListCredentialVersions(ctx, name)
	if err != nil {
		t.Fatalf("ListCredentialVersions before the rebuild: %v", err)
	}
	if len(before) != len(values) {
		t.Fatalf("versions before the rebuild = %+v, want %d", before, len(values))
	}
	if err := first.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	rebuilt, err := credentials.NewCredentialService(
		&credentials.Config{Strategy: "barrier"}, barrier, slog.Default())
	if err != nil {
		t.Fatalf("NewCredentialService: %v", err)
	}
	second := build(rebuilt)
	defer func() { _ = second.Stop(ctx) }()

	after, err := rebuilt.ListCredentialVersions(ctx, name)
	if err != nil {
		t.Fatalf("ListCredentialVersions after the rebuild: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("versions after the rebuild = %+v, want %d", after, len(before))
	}
	for _, v := range after {
		want, ok := values[v.Version]
		if !ok {
			t.Fatalf("unexpected version %d after the rebuild", v.Version)
		}
		holds(rebuilt, v.Version, want)
	}
}

// TestBuildServices_ManualStrategyStartsOnObjstore proves the refusal is the
// barrier strategy's: manual credentials stay in protected memory and write
// nothing to the data plane, so a seal-enabled objstore node configured with
// credential_strategy: manual builds and serves credentials.
func TestBuildServices_ManualStrategyStartsOnObjstore(t *testing.T) {
	ctx := context.Background()
	host := newLocalHost()
	defer func() { _ = host.Close() }()
	hub := watch.NewHub()
	defer hub.Close()
	barrier, registry, _ := newTestBarrier(t)
	credSvc, err := credentials.NewCredentialService(
		&credentials.Config{Strategy: credentials.StrategyManual}, barrier, slog.Default())
	if err != nil {
		t.Fatalf("NewCredentialService: %v", err)
	}

	node, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		Backend:           database.BackendObjstore,
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
	})
	if err != nil {
		t.Fatalf("BuildServices (manual strategy, objstore): %v", err)
	}
	defer func() { _ = node.Stop(ctx) }()
	if node.Credential == nil {
		t.Fatal("BuildServices built no credential service")
	}

	want := []byte("manual-secret")
	if err := credSvc.StoreCredential(ctx, "db-password", want); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if _, err := barrier.Get(ctx, "credentials/db-password"); err == nil {
		t.Fatal("the manual strategy wrote a credential to the barrier")
	}
}
