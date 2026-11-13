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
	"slices"
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

	// Binding lost nothing: the service built over the barrier migrates the
	// legacy record to its own layout and still answers it.
	buf, err := credSvc.RetrieveCredentialBuffer(ctx, "db-password")
	if err != nil {
		t.Fatalf("legacy credential after the bind: %v", err)
	}
	equal, err := buf.EqualBytes(want)
	buf.Destroy()
	if err != nil {
		t.Fatalf("EqualBytes: %v", err)
	}
	if !equal {
		t.Fatalf("legacy credential after the bind does not match %q", want)
	}

	// A new credential is stored by the barrier, which encrypts it.
	fresh := []byte("fresh-secret")
	if err := credSvc.StoreCredential(ctx, "api-key", fresh); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	got, err := barrier.Get(ctx, "__sys:credentials/api-key")
	if err != nil {
		t.Fatalf("barrier.Get new credential: %v", err)
	}
	if !bytes.Equal(got, fresh) {
		t.Fatalf("new credential in the barrier = %q, want %q", got, fresh)
	}
}

// TestBuildServices_MemoryBackendKeepsCredentialsAcrossARestart: a memory
// data plane keeps nothing across a restart, so a seal-enabled memory node
// binds the durable seal barrier as its credential store. A credential and
// its version history written before a restart are there after it, when the
// lifecycle is rebuilt over the same seal store.
func TestBuildServices_MemoryBackendKeepsCredentialsAcrossARestart(t *testing.T) {
	ctx := context.Background()
	barrier, registry, credSvc := newTestBarrier(t)

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

	node := build(credSvc)
	if err := credSvc.StoreCredential(ctx, "db-password", []byte("v1-secret")); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if _, err := credSvc.RotateCredential(ctx, "db-password", credentials.RotateOptions{NewValue: []byte("v2-secret"), GracePeriod: time.Hour}); err != nil {
		t.Fatalf("RotateCredential: %v", err)
	}
	if err := node.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// The restart: a new credential service over the same seal store.
	restarted, err := credentials.NewCredentialService(
		&credentials.Config{Strategy: credentials.StrategyBarrier}, barrier, slog.Default())
	if err != nil {
		t.Fatalf("NewCredentialService: %v", err)
	}
	node = build(restarted)
	defer func() { _ = node.Stop(ctx) }()

	versions, err := restarted.ListCredentialVersions(ctx, "db-password")
	if err != nil {
		t.Fatalf("ListCredentialVersions after the restart: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("versions after the restart = %d, want 2 (%+v)", len(versions), versions)
	}
	names, err := restarted.ListCredentials(ctx)
	if err != nil {
		t.Fatalf("ListCredentials after the restart: %v", err)
	}
	if !slices.Contains(names, "db-password") {
		t.Fatalf("credentials after the restart = %v, want db-password", names)
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
