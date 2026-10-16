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
	"maps"
	"slices"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
)

// fakeMigrationProvider drives the real migration engine over temp store dirs,
// minting a keyed target over a caller-supplied seal backend. It records the
// Quiesce/Reopen lifecycle calls so tests can assert the online sequence.
type fakeMigrationProvider struct {
	stores      MigrationStores
	sealBackend seal.StorageBackend
	quiesced    int
	reopened    int
}

func (p *fakeMigrationProvider) StoreDirs(_ context.Context, _ BarrierScope) (MigrationStores, error) {
	return p.stores, nil
}

func (p *fakeMigrationProvider) MintKeyedTarget(ctx context.Context, _ BarrierScope, opts seal.MigrationOptions) (*seal.StorageBarrier, error) {
	// Deferred, as the production provider mints it (sdk/go/node
	// keyedStrategyForStrategy): the passphrase is read from the credential
	// at each seal and unseal, so the fake never materializes it.
	strategy := seal.NewDeferredSoftwareStrategy()
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

// newNoneBarrier returns an initialized none barrier over its own backend.
func newNoneBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	nb, err := seal.NewStorageBarrier(testLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "n", DeploymentID: 1}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("none barrier: %v", err)
	}
	if err := nb.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("init none: %v", err)
	}
	return nb
}

// writeNoneFileStore writes a file-backend store at dir under a none barrier
// for migration.
func writeNoneFileStore(t *testing.T, dir string, pairs map[string]string) {
	t.Helper()
	st, err := file.New(dir, file.WithEncryptor(atrest.New(newNoneBarrier(t))))
	if err != nil {
		t.Fatalf("open none file store: %v", err)
	}
	for k, v := range pairs {
		if err := st.Set([]byte(k), []byte(v)); err != nil {
			t.Fatalf("set: %v", err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// readFileStore returns every record of the file store at dir read under enc.
func readFileStore(t *testing.T, dir string, enc *atrest.Encryptor) map[string]string {
	t.Helper()
	st, err := file.New(dir, file.WithEncryptor(enc))
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	defer func() { _ = st.Close() }()
	out := map[string]string{}
	it := st.NewIterator(nil)
	defer func() { _ = it.Close() }()
	for valid := it.SeekToFirst(); valid; valid = it.Next() {
		out[string(it.Key())] = string(it.Value())
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterate file store: %v", err)
	}
	return out
}

// writeNoneStore writes a none-encrypted Pebble store at dir for migration.
func writeNoneStore(t *testing.T, dir string, pairs map[string]string) {
	t.Helper()
	opts := &pebble.Options{}
	atrest.New(newNoneBarrier(t)).Apply(opts)
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
		Credentials:    seal.MustCredentials(context.Background(), []byte("migrate-pass")),
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

	prov := &fakeMigrationProvider{stores: MigrationStores{Pebble: []string{dir}}, sealBackend: backend}
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

// TestEncryptionMigration_SystemMovesFileStores migrates a node holding a
// Pebble store and a file-backend store, by each mechanism: both read back
// whole under the keyed root key afterwards.
func TestEncryptionMigration_SystemMovesFileStores(t *testing.T) {
	for _, mech := range []seal.MigrationMechanism{seal.MechanismSwap, seal.MechanismInPlace} {
		t.Run(string(mech), func(t *testing.T) {
			ctx := context.Background()
			reg, backend := initNoneSystemRegistry(t)
			root := t.TempDir()
			pebbleDir, fileDir := root+"/pebble", root+"/file"
			data := map[string]string{"a": "1", "b": "2", "c": "3"}
			writeNoneStore(t, pebbleDir, data)
			writeNoneFileStore(t, fileDir, data)

			prov := &fakeMigrationProvider{
				stores:      MigrationStores{Pebble: []string{pebbleDir}, File: []string{fileDir}},
				sealBackend: backend,
			}
			opts := validSwapOpts()
			opts.Mechanism = mech
			if err := NewEncryptionMigrationService(reg, prov, testLogger()).EnableSystemEncryption(ctx, opts); err != nil {
				t.Fatalf("EnableSystemEncryption: %v", err)
			}
			enc := atrest.New(initReopenKeyedService(t, backend, "root-key", "migrate-pass"))
			if got := readStore(t, pebbleDir, enc); !maps.Equal(got, data) {
				t.Errorf("pebble store after migration = %v, want %v", got, data)
			}
			if got := readFileStore(t, fileDir, enc); !maps.Equal(got, data) {
				t.Errorf("file store after migration = %v, want %v", got, data)
			}
		})
	}
}

func TestMigrationStores_Dirs(t *testing.T) {
	if dirs := (MigrationStores{}).Dirs(); len(dirs) != 0 {
		t.Fatalf("empty Dirs = %v, want none", dirs)
	}
	s := MigrationStores{Pebble: []string{"/p"}, File: []string{"/f"}}
	if got, want := s.Dirs(), []string{"/p", "/f"}; !slices.Equal(got, want) {
		t.Fatalf("Dirs = %v, want %v", got, want)
	}
}

func TestEncryptionMigration_TenantSharedBarrierRejected(t *testing.T) {
	ctx := context.Background()
	reg, backend := initNoneSystemRegistry(t)
	prov := &fakeMigrationProvider{stores: MigrationStores{Pebble: []string{t.TempDir()}}, sealBackend: backend}
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

	prov := &fakeMigrationProvider{stores: MigrationStores{Pebble: []string{dir}}, sealBackend: backend}
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
	prov := &fakeMigrationProvider{stores: MigrationStores{Pebble: []string{t.TempDir()}}, sealBackend: backend}
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
	strategy, err := seal.NewSoftwareStrategy(context.Background(), []byte(passphrase))
	if err != nil {
		t.Fatalf("strategy: %v", err)
	}
	b, err := seal.NewStorageBarrier(testLogger(), backend,
		seal.BarrierConfig{RootKeyPath: rootKeyPath, DeploymentID: 1}, strategy)
	if err != nil {
		t.Fatalf("barrier: %v", err)
	}
	if err := b.Unseal(context.Background(), seal.MustCredentials(context.Background(), []byte(passphrase))); err != nil {
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

func TestDataPlaneRecyclingError_NamesTheService(t *testing.T) {
	tests := []struct {
		service string
		want    string
	}{
		{service: "tenant", want: "service: tenant service unavailable during data-plane recycle"},
		{service: "kv", want: "service: kv service unavailable during data-plane recycle"},
	}
	for _, tt := range tests {
		err := error(&DataPlaneRecyclingError{Service: tt.service})
		if got := err.Error(); got != tt.want {
			t.Errorf("Error() = %q, want %q", got, tt.want)
		}
		if errors.Is(err, ErrMigrationProviderUnavailable) {
			t.Errorf("a recycling error must not match ErrMigrationProviderUnavailable")
		}
	}
}
