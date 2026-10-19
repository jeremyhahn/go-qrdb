// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
)

// newRecodeSourceBarrier returns an initialised StrategyNone system barrier
// over an in-memory backend: the only source recodeSystemBarrier accepts.
func newRecodeSourceBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	return newRecodeSourceBarrierOn(t, seal.NewMemoryBackend())
}

// newRecodeSourceBarrierOn is newRecodeSourceBarrier over the given backend, so
// a test can reopen the migrated root key from it.
func newRecodeSourceBarrierOn(t *testing.T, backend seal.StorageBackend) *seal.StorageBarrier {
	t.Helper()
	b, err := seal.NewStorageBarrier(slog.New(slog.DiscardHandler), backend,
		seal.BarrierConfig{RootKeyPath: "system/root-key"}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// writeRecodePebbleStore writes pairs into a Pebble store at dir encrypted by enc.
func writeRecodePebbleStore(t *testing.T, dir string, enc *atrest.Encryptor, pairs map[string]string) {
	t.Helper()
	opts := &pebble.Options{}
	enc.Apply(opts)
	db, err := pebble.Open(dir, opts)
	if err != nil {
		t.Fatalf("open pebble store %s: %v", dir, err)
	}
	for k, v := range pairs {
		if err := db.Set([]byte(k), []byte(v), pebble.Sync); err != nil {
			t.Fatalf("set %q: %v", k, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close pebble store %s: %v", dir, err)
	}
}

// readRecodePebbleStore returns the value of key in the Pebble store at dir,
// decrypted by enc.
func readRecodePebbleStore(t *testing.T, dir string, enc *atrest.Encryptor, key string) string {
	t.Helper()
	opts := &pebble.Options{}
	enc.Apply(opts)
	db, err := pebble.Open(dir, opts)
	if err != nil {
		t.Fatalf("open pebble store %s: %v", dir, err)
	}
	defer func() { _ = db.Close() }()
	v, closer, err := db.Get([]byte(key))
	if err != nil {
		t.Fatalf("get %q: %v", key, err)
	}
	defer func() { _ = closer.Close() }()
	return string(v)
}

// writeRecodeFileStore writes pairs into a file store at dir encrypted by enc.
func writeRecodeFileStore(t *testing.T, dir string, enc *atrest.Encryptor, pairs map[string]string) {
	t.Helper()
	s, err := file.New(dir, file.WithEncryptor(enc))
	if err != nil {
		t.Fatalf("open file store %s: %v", dir, err)
	}
	for k, v := range pairs {
		if err := s.Set([]byte(k), []byte(v)); err != nil {
			t.Fatalf("set %q: %v", k, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close file store %s: %v", dir, err)
	}
}

// TestRecodeSystemBarrier_ReencryptsStores drives the full copy, verify and
// commit sequence over a Pebble store and a file store under both mechanisms:
// afterwards the source barrier holds the keyed root key, the staging and
// backup directories are gone, and the store data reads back under the keyed
// barrier once it is unsealed with the migration's credential.
func TestRecodeSystemBarrier_ReencryptsStores(t *testing.T) {
	for _, mech := range []seal.MigrationMechanism{seal.MechanismSwap, seal.MechanismInPlace} {
		t.Run(string(mech), func(t *testing.T) {
			ctx := context.Background()
			backend := seal.NewMemoryBackend()
			source := newRecodeSourceBarrierOn(t, backend)
			root := t.TempDir()
			pebbleDir := filepath.Join(root, "vector")
			fileDir := filepath.Join(root, "sm", "shard-1-replica-1")
			walDir := filepath.Join(root, "wal")
			if err := os.MkdirAll(walDir, 0o700); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			srcEnc := atrest.New(source)
			writeRecodePebbleStore(t, pebbleDir, srcEnc, map[string]string{"k": "pebble-v"})
			writeRecodeFileStore(t, fileDir, srcEnc, map[string]string{"k": "file-v"})

			opts := validSystemMigrateOpts()
			opts.Mechanism = mech
			stores := service.MigrationStores{Pebble: []string{pebbleDir}, File: []string{fileDir}}
			if err := recodeSystemBarrier(ctx, source, stores, walDir, opts); err != nil {
				t.Fatalf("recodeSystemBarrier: %v", err)
			}
			if got := source.ActiveStrategy(); got != seal.StrategyPassphrase {
				t.Fatalf("active strategy = %q, want %q", got, seal.StrategyPassphrase)
			}
			for _, dir := range []string{pebbleDir, fileDir, walDir} {
				if _, err := os.Stat(dir + database.KeyedDirSuffix); !os.IsNotExist(err) {
					t.Fatalf("staging dir for %s left behind: %v", dir, err)
				}
			}

			// Reboot onto the adopted root key, as the daemon does, with the
			// keyed strategy registered.
			strategy, err := seal.NewSoftwareStrategy(ctx, []byte("daemon-migrate-pw"))
			if err != nil {
				t.Fatalf("NewSoftwareStrategy: %v", err)
			}
			keyed, err := seal.NewStorageBarrier(slog.New(slog.DiscardHandler), backend,
				seal.BarrierConfig{RootKeyPath: "system/root-key"}, strategy)
			if err != nil {
				t.Fatalf("NewStorageBarrier: %v", err)
			}
			t.Cleanup(func() { _ = keyed.Close() })
			if err := keyed.Unseal(ctx, seal.MustCredentials(ctx, []byte("daemon-migrate-pw"))); err != nil {
				t.Fatalf("Unseal keyed barrier: %v", err)
			}
			if got := readRecodePebbleStore(t, pebbleDir, atrest.New(keyed), "k"); got != "pebble-v" {
				t.Fatalf("pebble value = %q, want %q", got, "pebble-v")
			}
		})
	}
}

// TestRecodeSystemBarrier_FailureRollsBack proves a failure after the journal
// is begun leaves it terminal: the source stays unencrypted, no staging
// directory survives, and a second migration is not wedged behind
// ErrMigrationInProgress.
func TestRecodeSystemBarrier_FailureRollsBack(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		stores func(t *testing.T, root string) service.MigrationStores
	}{
		{
			name: "pebble store unreadable",
			stores: func(t *testing.T, root string) service.MigrationStores {
				dir := filepath.Join(root, "vector")
				if err := os.WriteFile(dir, []byte("not a store"), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				return service.MigrationStores{Pebble: []string{dir}}
			},
		},
		{
			name: "file store unreadable",
			stores: func(t *testing.T, root string) service.MigrationStores {
				dir := filepath.Join(root, "sm", "shard-1-replica-1")
				if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				if err := os.WriteFile(dir, []byte("not a store"), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				return service.MigrationStores{File: []string{dir}}
			},
		},
		{
			name: "write-ahead log unreadable",
			stores: func(t *testing.T, root string) service.MigrationStores {
				if err := os.WriteFile(filepath.Join(root, "wal"), []byte("not a wal"), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				return service.MigrationStores{}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := newRecodeSourceBarrier(t)
			root := t.TempDir()
			stores := tt.stores(t, root)
			walDir := filepath.Join(root, "wal")

			if err := recodeSystemBarrier(ctx, source, stores, walDir, validSystemMigrateOpts()); err == nil {
				t.Fatal("recodeSystemBarrier succeeded, want an error")
			}
			if got := source.ActiveStrategy(); got != seal.StrategyNone {
				t.Fatalf("active strategy after failure = %q, want %q", got, seal.StrategyNone)
			}
			for _, dir := range append(stores.Dirs(), walDir) {
				if _, err := os.Stat(dir + database.KeyedDirSuffix); !os.IsNotExist(err) {
					t.Fatalf("staging dir for %s left behind: %v", dir, err)
				}
			}
			if _, err := source.BeginMigration(ctx, validSystemMigrateOpts()); err != nil {
				t.Fatalf("BeginMigration after a failed recode: %v", err)
			}
		})
	}
}

// TestHandleSystemMigration_Recycle drives the boot loop's recycle with a live
// seal subsystem: it cancels the boot context, closes the member record (a
// close error is logged, not fatal), recodes the system stores and reports the
// outcome both on req.done and as its return, asking for a reboot only on
// success.
func TestHandleSystemMigration_Recycle(t *testing.T) {
	tests := []struct {
		name       string
		walIsFile  bool
		wantReboot bool
	}{
		{name: "commits and reboots", wantReboot: true},
		{name: "recode failure does not reboot", walIsFile: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := t.TempDir()
			walDir := filepath.Join(dataDir, "wal")
			if tt.walIsFile {
				if err := os.WriteFile(walDir, []byte("not a wal"), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			} else if err := os.MkdirAll(walDir, 0o700); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			source := newRecodeSourceBarrier(t)
			req := &systemMigrationRequest{opts: validSystemMigrateOpts(), done: make(chan error, 1)}
			f := &startFlags{dataDir: dataDir, smType: "pebble"}
			bootCtx, bootCancel := context.WithCancel(context.Background())
			defer bootCancel()
			member := &countingCloser{err: errors.New("member record close failed")}

			// The data plane is quiesced through every handle the boot loop
			// holds; empty ones close cleanly and leave the recode to decide.
			raftReg := server.NewRaftBackendFactory(nil, 1, "", nil, nil, slog.Default())
			tenantReg := server.NewTenantHostRegistry(nil, nil, slog.Default())
			reboot, err := handleSystemMigration(req, f, nil, &lifecycle.Node{}, nil, raftReg, tenantReg,
				member, bootCancel, &sealSubsystem{SealBarrier: source})

			if reboot != tt.wantReboot {
				t.Fatalf("reboot = %v, want %v (err %v)", reboot, tt.wantReboot, err)
			}
			if (err == nil) != tt.wantReboot {
				t.Fatalf("err = %v, want error = %v", err, !tt.wantReboot)
			}
			if doneErr := <-req.done; !errors.Is(doneErr, err) {
				t.Fatalf("req.done = %v, want %v", doneErr, err)
			}
			if bootCtx.Err() == nil {
				t.Fatal("boot context was not cancelled")
			}
			if member.calls != 1 {
				t.Fatalf("member record Close calls = %d, want 1", member.calls)
			}
			wantStrategy := seal.StrategyNone
			if tt.wantReboot {
				wantStrategy = seal.StrategyPassphrase
			}
			if got := source.ActiveStrategy(); got != wantStrategy {
				t.Fatalf("active strategy = %q, want %q", got, wantStrategy)
			}
		})
	}
}

// errRecodeBackendPut is the write failure recodeFailingBackend injects.
var errRecodeBackendPut = errors.New("injected backend put failure")

// recodeFailingBackend is a seal backend that fails the failAt-th Put to key,
// counting from the first Put after arm. Every other operation reaches the
// wrapped backend.
type recodeFailingBackend struct {
	seal.StorageBackend
	key    string
	failAt int
	armed  bool
	puts   int
}

func (b *recodeFailingBackend) Put(ctx context.Context, key string, value []byte) error {
	if b.armed && key == b.key {
		b.puts++
		if b.puts == b.failAt {
			return errRecodeBackendPut
		}
	}
	return b.StorageBackend.Put(ctx, key, value)
}

// TestRecodeSystemBarrier_BackendWriteFailure fails, in turn, each journal
// checkpoint the recode writes before the commit and the adoption of the
// keyed root key: every one surfaces the backend's error and none leaves the
// source barrier keyed.
func TestRecodeSystemBarrier_BackendWriteFailure(t *testing.T) {
	const rootKeyPath = "system/root-key"
	tests := []struct {
		name   string
		key    string
		failAt int
	}{
		{name: "pending checkpoint", key: rootKeyPath + ".migration", failAt: 2},
		{name: "copying checkpoint", key: rootKeyPath + ".migration", failAt: 3},
		{name: "staged snapshots checkpoint", key: rootKeyPath + ".migration", failAt: 4},
		{name: "verifying checkpoint", key: rootKeyPath + ".migration", failAt: 5},
		{name: "committing checkpoint", key: rootKeyPath + ".migration", failAt: 6},
		{name: "root key adoption", key: rootKeyPath, failAt: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &recodeFailingBackend{StorageBackend: seal.NewMemoryBackend(), key: tt.key, failAt: tt.failAt}
			source := newRecodeSourceBarrierOn(t, backend)
			backend.armed = true
			walDir := filepath.Join(t.TempDir(), "wal")
			if err := os.MkdirAll(walDir, 0o700); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}

			err := recodeSystemBarrier(context.Background(), source, service.MigrationStores{}, walDir, validSystemMigrateOpts())
			if !errors.Is(err, errRecodeBackendPut) {
				t.Fatalf("err = %v, want %v", err, errRecodeBackendPut)
			}
			if got := source.ActiveStrategy(); got != seal.StrategyNone {
				t.Fatalf("active strategy = %q, want %q", got, seal.StrategyNone)
			}
		})
	}
}

func TestRecodeSystemBarrier_EmptyWALCommits(t *testing.T) {
	ctx := context.Background()
	source := newRecodeSourceBarrier(t)
	walDir := filepath.Join(t.TempDir(), "wal")
	if err := os.MkdirAll(walDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if err := recodeSystemBarrier(ctx, source, service.MigrationStores{}, walDir, validSystemMigrateOpts()); err != nil {
		t.Fatalf("recodeSystemBarrier: %v", err)
	}
	if got := source.ActiveStrategy(); got != seal.StrategyPassphrase {
		t.Fatalf("active strategy = %q, want %q", got, seal.StrategyPassphrase)
	}
}

func TestRecodeSystemBarrier_Errors(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		source func(t *testing.T) *seal.StorageBarrier
		opts   seal.MigrationOptions
		want   error
	}{
		{
			name:   "nil source",
			source: func(*testing.T) *seal.StorageBarrier { return nil },
			opts:   validSystemMigrateOpts(),
			want:   service.ErrMigrationProviderUnavailable,
		},
		{
			name:   "none target",
			source: newRecodeSourceBarrier,
			opts: seal.MigrationOptions{
				TargetStrategy: seal.StrategyNone,
				Mechanism:      seal.MechanismSwap,
				Availability:   seal.AvailabilityQuiesced,
			},
			want: seal.ErrMigrationInvalidTarget,
		},
		{
			name:   "shamir target without a ceremony distributor",
			source: newRecodeSourceBarrier,
			opts: seal.MigrationOptions{
				TargetStrategy:  seal.StrategyShamir,
				Mechanism:       seal.MechanismSwap,
				Availability:    seal.AvailabilityQuiesced,
				ShamirThreshold: 2,
				ShamirTotal:     3,
			},
			want: seal.ErrCeremonyDistributorNotConfigured,
		},
		{
			name:   "keyed target with no system strategy",
			source: newRecodeSourceBarrier,
			opts: seal.MigrationOptions{
				TargetStrategy: seal.StrategyID("hsm-only"),
				Mechanism:      seal.MechanismSwap,
				Availability:   seal.AvailabilityQuiesced,
			},
			want: seal.ErrMigrationInvalidTarget,
		},
		{
			name:   "passphrase target without a passphrase",
			source: newRecodeSourceBarrier,
			opts: seal.MigrationOptions{
				TargetStrategy: seal.StrategyPassphrase,
				Mechanism:      seal.MechanismSwap,
				Availability:   seal.AvailabilityQuiesced,
			},
			want: quicraftseal.ErrEmptyCredentials,
		},
		{
			name: "migration already in progress",
			source: func(t *testing.T) *seal.StorageBarrier {
				b := newRecodeSourceBarrier(t)
				if _, err := b.BeginMigration(context.Background(), validSystemMigrateOpts()); err != nil {
					t.Fatalf("BeginMigration: %v", err)
				}
				return b
			},
			opts: validSystemMigrateOpts(),
			want: seal.ErrMigrationInProgress,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := recodeSystemBarrier(ctx, tt.source(t), service.MigrationStores{}, t.TempDir(), tt.opts)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}
