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

// Package sealretire opens the seal store's Pebble and retires the plaintext
// history an enable-encryption migration leaves in its files once the seal
// store's records are re-encrypted. qrdbd and the embedded node share it.
package sealretire

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// StagingPrefix is the key prefix an enable-encryption migration stages the
// seal store's re-encrypted records under before it commits them.
var StagingPrefix = strings.TrimSuffix(seal.SealStoreDefaultPrefix, "/") + ".migrating/"

// StagedRecords is the seal store's keyspace as an enable-encryption
// migration journals, stages and commits it.
func StagedRecords() seal.StagedRecords {
	return seal.StagedRecords{Prefix: seal.SealStoreDefaultPrefix, StagingPrefix: StagingPrefix}
}

// JournalNamesSealStore reports whether a migration journal stages the seal
// store's records.
func JournalNamesSealStore(j *seal.MigrationJournal) bool {
	if j == nil {
		return false
	}
	for _, rec := range j.StagedRecords {
		if rec.Prefix == seal.SealStoreDefaultPrefix {
			return true
		}
	}
	return false
}

// Compactor returns the step that retires the seal store's plaintext history
// once enable-encryption has replaced its records: a whole-store compaction,
// which flushes the memtable (rotating the WAL that holds the old writes) and
// rewrites every SST without the shadowed versions. A nil store has no
// history to retire.
func Compactor(store *pebblestore.PebbleStorage) func() error {
	if store == nil {
		return func() error { return nil }
	}
	return func() error {
		// Pebble refuses an unbounded manual compaction, so the range ends
		// just past the largest key any table holds once the memtable is
		// flushed (deleted keys included: their tombstones are in a table).
		if err := store.Flush(); err != nil {
			return err
		}
		levels, err := store.DB().SSTables()
		if err != nil {
			return err
		}
		var largest []byte
		for _, level := range levels {
			for _, table := range level {
				if k := table.Largest.UserKey; largest == nil || bytes.Compare(k, largest) > 0 {
					largest = k
				}
			}
		}
		if largest == nil {
			return nil
		}
		return store.Compact(nil, append(append([]byte(nil), largest...), 0x00))
	}
}

// Open opens the seal store's Pebble in dir on base (the host file system
// when nil) through a pebblestore.NoRecycleFS, so its WAL is never recycled
// and plaintext written before an enable-encryption migration cannot survive
// in a reused log's tail. SyncWrites is on: a seal-store write
// is durable when it returns.
func Open(dir string, base filesys.FS) (*pebblestore.PebbleStorage, *Retirer, error) {
	return OpenConfig(&pebblestore.Config{DataDir: dir}, base)
}

// OpenConfig is Open with the rest of the Pebble configuration taken from
// cfg; it forces SyncWrites on and sets cfg.FS to the retirement wrapper over
// base (the host file system when nil).
func OpenConfig(cfg *pebblestore.Config, base filesys.FS) (*pebblestore.PebbleStorage, *Retirer, error) {
	f := pebblestore.NewNoRecycleFS(base)
	c := *cfg
	c.SyncWrites = true
	c.FS = f
	store, err := pebblestore.New(&c)
	if err != nil {
		return nil, nil, err
	}
	return store, &Retirer{store: store, fs: f, dir: c.DataDir}, nil
}

// rotations is how many times the retirement rotates the WAL: more than the
// LogRecycler holds (MemTableStopWritesThreshold + 1), so every log written
// before the retirement leaves the recycler. rotationKey is the scratch key
// the retirement writes to rotate the seal store's WAL; it is deleted when
// the rotations end.
var rotationKey = []byte("qrdbd/seal-store/retire-rotate")

const rotations = 8

// retireTimeout bounds the wait for Pebble's asynchronous deletion of the
// obsolete files.
const retireTimeout = time.Minute

// marker is the seal-store key that records a plaintext retirement still
// owed. It is written (synced) before the retirement starts, and at boot
// before RecoverInterruptedMigration clears a committed journal naming the
// seal store, and deleted only once the retirement succeeds, so each boot
// retries while it exists.
var marker = []byte("qrdbd/seal-store/retire-plaintext")

// Retirer retires the plaintext history an enable-encryption leaves in the
// seal store's files once its records are replaced.
type Retirer struct {
	store *pebblestore.PebbleStorage
	fs    *pebblestore.NoRecycleFS
	dir   string
	// fault, set only by tests, fails Retire right after the marker is written.
	fault func() error
}

// plaintextFiles lists the WAL and table files in dir through the
// retirer's vfs.
func (r *Retirer) plaintextFiles() ([]string, error) {
	names, err := r.fs.List(r.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, name := range names {
		if !strings.HasSuffix(name, ".log") && !strings.HasSuffix(name, ".sst") {
			continue
		}
		info, serr := r.fs.Stat(r.fs.PathJoin(r.dir, name))
		if serr != nil {
			if errors.Is(serr, os.ErrNotExist) {
				continue
			}
			return nil, serr
		}
		if !info.IsDir() {
			out = append(out, name)
		}
	}
	return out, nil
}

// Retire marks the retirement owed, retires the seal store's plaintext
// history (compaction, WAL rotation, then a wait for Pebble to delete every
// file written before it) and clears the marker. A nil Retirer or store is a
// no-op.
func (r *Retirer) Retire(ctx context.Context) error {
	if r == nil || r.store == nil {
		return nil
	}
	if err := r.Mark(); err != nil {
		return err
	}
	if r.fault != nil {
		if err := r.fault(); err != nil {
			return err
		}
	}
	if err := r.retireFiles(ctx); err != nil {
		return err
	}
	if err := r.store.Delete(marker); err != nil {
		return fmt.Errorf("clear the plaintext-retirement marker: %w", err)
	}
	return nil
}

// Mark records (synced) that a plaintext retirement is owed.
func (r *Retirer) Mark() error {
	if r == nil || r.store == nil {
		return nil
	}
	if err := r.store.Set(marker, []byte{1}); err != nil {
		return fmt.Errorf("write the plaintext-retirement marker: %w", err)
	}
	return nil
}

// Marked reports whether a plaintext retirement is owed.
func (r *Retirer) Marked() (bool, error) {
	if r == nil || r.store == nil {
		return false, nil
	}
	return r.store.Exists(marker)
}

// RetirePending runs Retire when the marker records one owed.
func (r *Retirer) RetirePending(ctx context.Context) error {
	owed, err := r.Marked()
	if err != nil || !owed {
		return err
	}
	return r.Retire(ctx)
}

func (r *Retirer) retireFiles(ctx context.Context) error {
	before, err := r.plaintextFiles()
	if err != nil {
		return err
	}
	if err := Compactor(r.store)(); err != nil {
		return fmt.Errorf("compact: %w", err)
	}
	for i := 0; i < rotations; i++ {
		if err := r.store.Set(rotationKey, []byte{byte(i)}); err != nil {
			return fmt.Errorf("rotate write-ahead log: %w", err)
		}
		if err := r.store.Flush(); err != nil {
			return fmt.Errorf("rotate write-ahead log: %w", err)
		}
	}
	if err := r.store.Delete(rotationKey); err != nil {
		return fmt.Errorf("rotate write-ahead log: %w", err)
	}
	levels, err := r.store.DB().SSTables()
	if err != nil {
		return err
	}
	live := map[string]bool{}
	for _, level := range levels {
		for _, table := range level {
			live[fmt.Sprintf("%06d.sst", uint64(table.BackingSSTNum))] = true
		}
	}
	var targets []string
	for _, name := range before {
		if !live[name] {
			targets = append(targets, r.fs.PathJoin(r.dir, name))
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, retireTimeout)
	defer cancel()
	for {
		removed := r.fs.Removal()
		left := 0
		for _, path := range targets {
			if _, serr := r.fs.Stat(path); serr == nil {
				left++
			} else if !errors.Is(serr, os.ErrNotExist) {
				return serr
			}
		}
		if left == 0 {
			return nil
		}
		select {
		case <-removed:
		case <-waitCtx.Done():
			return fmt.Errorf("%d seal-store files written before the retirement are not deleted: %w", left, waitCtx.Err())
		}
	}
}
