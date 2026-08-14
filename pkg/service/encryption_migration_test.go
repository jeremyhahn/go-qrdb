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

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// fakeMigrationProvider drives the real migration engine over temp store dirs,
// minting a keyed target over a caller-supplied seal backend. It records the
// Quiesce/Reopen lifecycle calls so tests can assert the online sequence.
type fakeMigrationProvider struct {
	dirs        []string
	sealBackend seal.StorageBackend
	quiesced    int
	reopened    int
}

func (p *fakeMigrationProvider) StoreDirs(_ context.Context, _ BarrierScope) ([]string, error) {
	return p.dirs, nil
}

func (p *fakeMigrationProvider) MintKeyedTarget(ctx context.Context, _ BarrierScope, opts seal.MigrationOptions) (*seal.StorageBarrier, error) {
	strategy, err := seal.NewSoftwareStrategy(opts.Credentials.Secret)
	if err != nil {
		return nil, err
	}
	b, err := seal.NewStorageBarrier(testLogger(), p.sealBackend,
		seal.BarrierConfig{RootKeyPath: "root-key.migrating", DeploymentID: 1}, strategy)
	if err != nil {
		return nil, err
	}
	if err := b.Initialize(ctx, opts.Credentials); err != nil {
		return nil, err
	}
	return b, nil
}

func (p *fakeMigrationProvider) Quiesce(_ context.Context, _ BarrierScope, _ []string) error {
	p.quiesced++
	return nil
}

func (p *fakeMigrationProvider) Reopen(_ context.Context, _ BarrierScope) error {
	p.reopened++
	return nil
}

// initNoneSystemRegistry returns a registry whose system barrier is an
// initialized none barrier over the shared backend (so a system migration has
// a valid none source), plus that backend.
func initNoneSystemRegistry(t *testing.T) (*seal.BarrierRegistry, seal.StorageBackend) {
	t.Helper()
	backend := seal.NewMemoryBackend()
	sys, err := seal.NewStorageBarrier(testLogger(), backend,
		seal.BarrierConfig{RootKeyPath: "root-key", DeploymentID: 1}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("system barrier: %v", err)
	}
	if err := sys.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("init system: %v", err)
	}
	reg, err := seal.NewBarrierRegistry(testLogger(), sys)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	return reg, backend
}

// writeNoneStore writes a none-encrypted Pebble store at dir for migration.
func writeNoneStore(t *testing.T, dir string, pairs map[string]string) {
	t.Helper()
	ctx := context.Background()
	nb, err := seal.NewStorageBarrier(testLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "n", DeploymentID: 1}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("none barrier: %v", err)
	}
	if err := nb.Initialize(ctx, seal.Credentials{}); err != nil {
		t.Fatalf("init none: %v", err)
	}
	opts := &pebble.Options{}
	atrest.New(nb).Apply(opts)
	db, err := pebble.Open(dir, opts)
	if err != nil {
		t.Fatalf("open none store: %v", err)
	}
	for k, v := range pairs {
		if err := db.Set([]byte(k), []byte(v), pebble.Sync); err != nil {
			t.Fatalf("set: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func validSwapOpts() seal.MigrationOptions {
	return seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.Credentials{Secret: []byte("migrate-pass")},
	}
}

func TestEncryptionMigration_System(t *testing.T) {
	ctx := context.Background()
	reg, backend := initNoneSystemRegistry(t)

	dir := t.TempDir() + "/pebble"
	data := map[string]string{}
	for i := 0; i < 50; i++ {
		data[fmt.Sprintf("k/%02d", i)] = fmt.Sprintf("v-%d", i)
	}
	writeNoneStore(t, dir, data)

	prov := &fakeMigrationProvider{dirs: []string{dir}, sealBackend: backend}
	svc := NewEncryptionMigrationService(reg, prov, testLogger())

	if err := svc.EnableSystemEncryption(ctx, validSwapOpts()); err != nil {
		t.Fatalf("EnableSystemEncryption: %v", err)
	}
	if prov.quiesced != 1 || prov.reopened != 1 {
		t.Fatalf("lifecycle calls: quiesced=%d reopened=%d, want 1/1", prov.quiesced, prov.reopened)
	}
	// The system barrier's persisted root key is now keyed: reopen and read.
	reopened := initReopenKeyedService(t, backend, "root-key", "migrate-pass")
	got := readStore(t, dir, atrest.New(reopened))
	if len(got) != len(data) {
		t.Fatalf("got %d records, want %d", len(got), len(data))
	}
}

func TestEncryptionMigration_TenantSharedBarrierRejected(t *testing.T) {
	ctx := context.Background()
	reg, backend := initNoneSystemRegistry(t)
	prov := &fakeMigrationProvider{dirs: []string{t.TempDir()}, sealBackend: backend}
	svc := NewEncryptionMigrationService(reg, prov, testLogger())

	// "shared-tenant" was never registered as a dedicated barrier → it is on
	// the shared system barrier and must be rejected.
	err := svc.EnableTenantEncryption(ctx, "shared-tenant", validSwapOpts())
	if !errors.Is(err, ErrSharedBarrierImmutable) {
		t.Fatalf("EnableTenantEncryption(shared) = %v, want ErrSharedBarrierImmutable", err)
	}
	if prov.quiesced != 0 {
		t.Fatal("a rejected migration must not quiesce any stores")
	}
}

func TestEncryptionMigration_TenantDedicatedBarrier(t *testing.T) {
	ctx := context.Background()
	reg, backend := initNoneSystemRegistry(t)

	// Register a dedicated none barrier for the tenant and initialize it (a
	// real none tenant barrier is initialized to the none strategy).
	tenantBackend := seal.NewMemoryBackend()
	tb, err := reg.RegisterTenant(ctx, "acme", tenantBackend, seal.BarrierConfig{}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}
	if err := tb.Initialize(ctx, seal.Credentials{}); err != nil {
		t.Fatalf("Initialize tenant barrier: %v", err)
	}

	dir := t.TempDir() + "/tenant-pebble"
	data := map[string]string{"x": "1", "y": "2", "z": "3"}
	writeNoneStore(t, dir, data)

	prov := &fakeMigrationProvider{dirs: []string{dir}, sealBackend: backend}
	svc := NewEncryptionMigrationService(reg, prov, testLogger())

	if err := svc.EnableTenantEncryption(ctx, "acme", validSwapOpts()); err != nil {
		t.Fatalf("EnableTenantEncryption(dedicated): %v", err)
	}
	if prov.quiesced != 1 || prov.reopened != 1 {
		t.Fatalf("lifecycle: quiesced=%d reopened=%d", prov.quiesced, prov.reopened)
	}
}

func TestEncryptionMigration_NilProvider(t *testing.T) {
	reg, _ := initNoneSystemRegistry(t)
	svc := NewEncryptionMigrationService(reg, nil, testLogger())
	if err := svc.EnableSystemEncryption(context.Background(), validSwapOpts()); !errors.Is(err, ErrMigrationProviderUnavailable) {
		t.Fatalf("nil provider = %v, want ErrMigrationProviderUnavailable", err)
	}
}

func TestEncryptionMigration_InvalidOptionsRejected(t *testing.T) {
	reg, backend := initNoneSystemRegistry(t)
	prov := &fakeMigrationProvider{dirs: []string{t.TempDir()}, sealBackend: backend}
	svc := NewEncryptionMigrationService(reg, prov, testLogger())
	bad := seal.MigrationOptions{TargetStrategy: seal.StrategyNone, Mechanism: seal.MechanismSwap, Availability: seal.AvailabilityQuiesced}
	if err := svc.EnableSystemEncryption(context.Background(), bad); !errors.Is(err, seal.ErrMigrationInvalidTarget) {
		t.Fatalf("invalid opts = %v, want ErrMigrationInvalidTarget", err)
	}
	if prov.quiesced != 0 {
		t.Fatal("invalid options must reject before quiescing")
	}
}

// initReopenKeyedService unseals an existing keyed root key (post-migration).
func initReopenKeyedService(t *testing.T, backend seal.StorageBackend, rootKeyPath, passphrase string) *seal.StorageBarrier {
	t.Helper()
	strategy, err := seal.NewSoftwareStrategy([]byte(passphrase))
	if err != nil {
		t.Fatalf("strategy: %v", err)
	}
	b, err := seal.NewStorageBarrier(testLogger(), backend,
		seal.BarrierConfig{RootKeyPath: rootKeyPath, DeploymentID: 1}, strategy)
	if err != nil {
		t.Fatalf("barrier: %v", err)
	}
	if err := b.Unseal(context.Background(), seal.Credentials{Secret: []byte(passphrase)}); err != nil {
		t.Fatalf("unseal: %v", err)
	}
	return b
}

func readStore(t *testing.T, dir string, enc *atrest.Encryptor) map[string]string {
	t.Helper()
	opts := &pebble.Options{ReadOnly: true}
	enc.Apply(opts)
	db, err := pebble.Open(dir, opts)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	out := map[string]string{}
	it, err := db.NewIter(nil)
	if err != nil {
		t.Fatalf("iter: %v", err)
	}
	defer func() { _ = it.Close() }()
	for valid := it.First(); valid; valid = it.Next() {
		out[string(it.Key())] = string(it.Value())
	}
	return out
}
