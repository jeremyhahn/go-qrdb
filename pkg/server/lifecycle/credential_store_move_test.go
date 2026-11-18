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
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// A node first built without a data plane that encrypts at rest keeps its
// credentials in the seal barrier. A later build of the same credential
// core on an encrypting data plane moves the credentials, their rotation
// policy and their version history onto the data plane before it switches
// stores, although the core's node-local migration already ran.
func TestBuildServices_FallbackCredentialsMoveToTheDataPlane(t *testing.T) {
	ctx := context.Background()
	barrier, registry, credSvc := newTestBarrier(t)

	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	fallbackHost := newLocalHost()
	t.Cleanup(func() { _ = fallbackHost.Close() })
	node, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:              fallbackHost,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
	})
	if err != nil {
		t.Fatalf("BuildServices on the barrier fallback: %v", err)
	}
	if err := node.Credential.CredentialStore(ctx, "db-password", []byte("v1-secret")); err != nil {
		t.Fatalf("CredentialStore: %v", err)
	}
	if _, err := credSvc.RotateCredential(ctx, "db-password", credentials.RotateOptions{NewValue: []byte("v2-secret"), GracePeriod: time.Hour}); err != nil {
		t.Fatalf("RotateCredential: %v", err)
	}
	policy := credentials.RotationPolicy{Enabled: true, Interval: 24 * time.Hour, RetainVersions: 3}
	if err := node.Credential.CredentialSetRotationPolicy(ctx, "db-password", policy); err != nil {
		t.Fatalf("CredentialSetRotationPolicy: %v", err)
	}
	if err := node.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	dataDir := t.TempDir()
	enc := atrest.New(barrier)
	stg, _, err := lifecycle.NewLocalStorage(database.BackendPebble, dataDir, enc, qrdbconfig.StorageConfig{})
	if err != nil {
		t.Fatalf("NewLocalStorage(pebble): %v", err)
	}
	host := backend.NewLocalBackend(engine.NewKVEngine(stg))
	t.Cleanup(func() { _ = host.Close() })
	node, err = lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           dataDir,
		Backend:           database.BackendPebble,
		Encryptor:         enc,
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
	})
	if err != nil {
		t.Fatalf("BuildServices on the data plane: %v", err)
	}
	t.Cleanup(func() { _ = node.Stop(ctx) })

	cur, err := node.Credential.CredentialRetrieveBuffer(ctx, "db-password")
	if err != nil {
		t.Fatalf("CredentialRetrieveBuffer after the switch: %v", err)
	}
	if ok, err := cur.EqualBytes([]byte("v2-secret")); err != nil || !ok {
		t.Fatalf("current value is not v2-secret (err %v)", err)
	}
	cur.Destroy()
	old, err := node.Credential.CredentialRetrieveVersionBuffer(ctx, "db-password", 1)
	if err != nil {
		t.Fatalf("CredentialRetrieveVersionBuffer after the switch: %v", err)
	}
	if ok, err := old.EqualBytes([]byte("v1-secret")); err != nil || !ok {
		t.Fatalf("version 1 is not v1-secret (err %v)", err)
	}
	old.Destroy()
	versions, err := node.Credential.CredentialListVersions(ctx, "db-password")
	if err != nil {
		t.Fatalf("CredentialListVersions after the switch: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("versions after the switch = %d, want 2 (%+v)", len(versions), versions)
	}
	status, err := node.Credential.CredentialRotationPolicy(ctx, "db-password")
	if err != nil {
		t.Fatalf("CredentialRotationPolicy after the switch: %v", err)
	}
	if status.Policy != policy {
		t.Fatalf("policy after the switch = %+v, want %+v", status.Policy, policy)
	}

	// The barrier no longer holds the moved records, so a later build does
	// not bring back one deleted since.
	keys, err := barrier.List(ctx, "__sys:credential")
	if err != nil {
		t.Fatalf("barrier.List: %v", err)
	}
	for _, k := range keys {
		if strings.Contains(k, "db-password") || strings.Contains(k, "ZGItcGFzc3dvcmQ") {
			t.Fatalf("record %q is still in the seal barrier", k)
		}
	}
}
