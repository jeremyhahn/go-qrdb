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
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestBuildServices_RefusesAnUnencryptedCredentialStore proves a node whose
// data plane does not encrypt at rest (a standalone objstore backend) refuses
// to bind it as the credential store, and leaves the barrier-encrypted legacy
// credentials where they are: none is migrated or deleted.
func TestBuildServices_RefusesAnUnencryptedCredentialStore(t *testing.T) {
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
	if err == nil {
		_ = node.Stop(ctx)
		t.Fatal("BuildServices bound an objstore data plane as the credential store")
	}
	var refused *lifecycle.UnencryptedCredentialStoreError
	if !errors.As(err, &refused) {
		t.Fatalf("BuildServices error = %v, want *UnencryptedCredentialStoreError", err)
	}
	if refused.Backend != database.BackendObjstore {
		t.Fatalf("refused backend = %q, want %q", refused.Backend, database.BackendObjstore)
	}

	got, err := barrier.Get(ctx, legacyKey)
	if err != nil {
		t.Fatalf("legacy credential after the refusal: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("legacy credential = %q, want %q", got, want)
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
