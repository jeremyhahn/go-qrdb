// Copyright (c) 2026 Jeremy Hahn
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

// Package remotelsm binds a pkg/storage/pebble store to remote SSTable
// storage (pebble's objstorage/remote). It lives outside pkg/storage/pebble
// so that package keeps no objstorage/remote import and a lean standalone
// embedder does not compile it; only the objstore opener imports remotelsm.
package remotelsm

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/objstorage/remote"
	"github.com/cockroachdb/pebble/v2/vfs"

	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// creatorIDFile is the local marker file, in Config.DataDir beside the WAL
// and MANIFEST, that holds the store's CreatorID: one decimal non-zero
// uint64 and a newline.
const creatorIDFile = "QRDB-CREATORID"

// creatorIDPanicPrefix starts the message of the assertion (an error, or a
// panic with pebble's invariants enabled) pebble raises when SetCreatorID is
// given an id other than the one the MANIFEST records.
const creatorIDPanicPrefix = "attempt to change CreatorID"

// remoteHook is the pebbleStorage.OpenHook WithRemote installs.
type remoteHook struct {
	store    remote.Storage
	locator  remote.Locator
	strategy remote.CreateOnSharedStrategy
}

// WithRemote configures the Engine to keep its SSTables on remote storage
// reached through store, under locator, using strategy to decide which
// levels are remote (CreateOnSharedAll for a local binding — every level
// remote; CreateOnSharedLower is available but not used by phase 1).
// The local dir passed to pebble.New still roots the WAL, MANIFEST and
// block cache (OS-2) — WithRemote narrows what the local dir holds, it does
// not replace it.
//
// WithRemote lives in package pkg/storage/pebble/remotelsm, not in
// pkg/storage/pebble, so pkg/storage/pebble keeps no objstorage/remote
// import (lean standalone gate); it installs itself through
// pebbleStorage.WithOpenHook.
//
// New refuses a nil store, an empty locator, CreateOnSharedNone and a
// Config with DisableWAL with a *pebbleStorage.ConfigError, before it
// creates any file. The store's CreatorID is generated once and persisted
// in the file QRDB-CREATORID in Config.DataDir; a marker that differs from
// the id the MANIFEST records is refused with a *CreatorIDError. WithRemote
// has no effect on NewFromDB.
func WithRemote(store remote.Storage, locator remote.Locator, strategy remote.CreateOnSharedStrategy) pebbleStorage.Option {
	return pebbleStorage.WithOpenHook(&remoteHook{store: store, locator: locator, strategy: strategy})
}

// Validate refuses a remote binding New cannot open, and a configuration
// that keeps no durable local WAL (OS-2 A).
func (r *remoteHook) Validate(config *pebbleStorage.Config) error {
	switch {
	case r.store == nil:
		return &pebbleStorage.ConfigError{Field: "Remote", Message: "store is required"}
	case r.locator == "":
		return &pebbleStorage.ConfigError{Field: "Remote", Message: "locator is required"}
	case r.strategy == remote.CreateOnSharedNone:
		return &pebbleStorage.ConfigError{Field: "Remote", Message: "strategy must not be CreateOnSharedNone"}
	case config.DisableWAL:
		return &pebbleStorage.ConfigError{Field: "DisableWAL", Message: "must be false with a remote store (OS-2 A)"}
	}
	return nil
}

// Apply points o's remote storage at the binding and raises its format
// major version to the minimum that supports shared objects.
func (r *remoteHook) Apply(o *pebble.Options) {
	o.Experimental.RemoteStorage = remote.MakeSimpleFactory(map[remote.Locator]remote.Storage{r.locator: r.store})
	o.Experimental.CreateOnShared = r.strategy
	o.Experimental.CreateOnSharedLocator = r.locator
	if o.FormatMajorVersion < pebble.FormatMinForSharedObjects {
		o.FormatMajorVersion = pebble.FormatMinForSharedObjects
	}
}

// AfterOpen loads (or creates) the store's CreatorID marker and sets the id
// on db.
func (r *remoteHook) AfterOpen(db *pebble.DB, fs vfs.FS, dir string) error {
	id, err := loadCreatorID(fs, dir)
	if err != nil {
		return err
	}
	return setCreatorID(db, fs.PathJoin(dir, creatorIDFile), id)
}

// setCreatorID sets id on db. An id that differs from the one the MANIFEST
// already records is an assertion failure in pebble's (*DB).SetCreatorID:
// pebble gives it no typed error, and with pebble's invariants enabled (race
// and invariants builds, as the coverage leg runs) it panics instead of
// returning, so a tampered or swapped marker file would crash the process.
// setCreatorID turns that assertion, returned or raised, into a
// *CreatorIDError naming the marker file at path, and re-panics any other
// panic.
func setCreatorID(db *pebble.DB, path string, id uint64) (err error) {
	changed := &CreatorIDError{Path: path, Reason: "differs from the creator id the store's MANIFEST records"}
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if !strings.HasPrefix(panicMessage(r), creatorIDPanicPrefix) {
			panic(r)
		}
		err = changed
	}()
	if err := db.SetCreatorID(id); err != nil {
		if strings.HasPrefix(err.Error(), creatorIDPanicPrefix) {
			return changed
		}
		return err
	}
	return nil
}

// panicMessage returns the text of a recovered panic value.
func panicMessage(r any) string {
	switch v := r.(type) {
	case error:
		return v.Error()
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

// CreatorIDError reports a creator-ID marker file that cannot be read, does
// not hold one decimal non-zero uint64, or differs from the id the store's
// MANIFEST records.
type CreatorIDError struct {
	Path   string
	Reason string
}

func (e *CreatorIDError) Error() string {
	return "pebble creator id: " + e.Path + ": " + e.Reason
}

// loadCreatorID returns the CreatorID recorded in dir's marker file, or
// generates a random non-zero one and writes it durably when the file is
// absent.
func loadCreatorID(fs vfs.FS, dir string) (uint64, error) {
	path := fs.PathJoin(dir, creatorIDFile)
	f, err := fs.Open(path)
	switch {
	case err == nil:
		data, readErr := io.ReadAll(f)
		_ = f.Close()
		if readErr != nil {
			return 0, &CreatorIDError{Path: path, Reason: readErr.Error()}
		}
		return parseCreatorID(path, data)
	case !errors.Is(err, os.ErrNotExist):
		return 0, err
	}

	id, err := newCreatorID()
	if err != nil {
		return 0, err
	}
	if err := writeCreatorID(fs, dir, path, id); err != nil {
		return 0, err
	}
	return id, nil
}

// parseCreatorID parses the marker file's contents.
func parseCreatorID(path string, data []byte) (uint64, error) {
	text, ok := strings.CutSuffix(string(data), "\n")
	if !ok {
		return 0, &CreatorIDError{Path: path, Reason: "missing trailing newline"}
	}
	id, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, &CreatorIDError{Path: path, Reason: "not a decimal uint64"}
	}
	if id == 0 {
		return 0, &CreatorIDError{Path: path, Reason: "zero creator id"}
	}
	return id, nil
}

// newCreatorID returns a random non-zero uint64.
func newCreatorID() (uint64, error) {
	var b [8]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		if id := binary.BigEndian.Uint64(b[:]); id != 0 {
			return id, nil
		}
	}
}

// writeCreatorID writes id to path through a synced temporary file renamed
// into place, then syncs dir so the rename survives a crash.
func writeCreatorID(fs vfs.FS, dir, path string, id uint64) error {
	tmp := path + ".tmp"
	f, err := fs.Create(tmp, vfs.WriteCategoryUnspecified)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(strconv.FormatUint(id, 10) + "\n")); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := fs.Rename(tmp, path); err != nil {
		return err
	}
	d, err := fs.OpenDir(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}
