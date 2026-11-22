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

package pebble

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/objstorage/remote"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// creatorIDFile is the local marker file, in Config.DataDir beside the WAL
// and MANIFEST, that holds the store's CreatorID: one decimal non-zero
// uint64 and a newline.
const creatorIDFile = "QRDB-CREATORID"

// remoteOptions is what WithRemote records for New.
type remoteOptions struct {
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
// New refuses a nil store, an empty locator, CreateOnSharedNone and a
// Config with DisableWAL with a *ConfigError, before it creates any file.
// The store's CreatorID is generated once and persisted in the file
// QRDB-CREATORID in Config.DataDir. WithRemote has no effect on NewFromDB.
func WithRemote(store remote.Storage, locator remote.Locator, strategy remote.CreateOnSharedStrategy) Option {
	return func(s *PebbleStorage) {
		s.remote = &remoteOptions{store: store, locator: locator, strategy: strategy}
	}
}

// validate refuses a remote binding New cannot open, and a configuration
// that keeps no durable local WAL (OS-2 A).
func (r *remoteOptions) validate(config *Config) error {
	switch {
	case r.store == nil:
		return &ConfigError{Field: "Remote", Message: "store is required"}
	case r.locator == "":
		return &ConfigError{Field: "Remote", Message: "locator is required"}
	case r.strategy == remote.CreateOnSharedNone:
		return &ConfigError{Field: "Remote", Message: "strategy must not be CreateOnSharedNone"}
	case config.DisableWAL:
		return &ConfigError{Field: "DisableWAL", Message: "must be false with a remote store (OS-2 A)"}
	}
	return nil
}

// applyTo points o's remote storage at the binding and raises its format
// major version to the minimum that supports shared objects.
func (r *remoteOptions) applyTo(o *pebble.Options) {
	o.Experimental.RemoteStorage = remote.MakeSimpleFactory(map[remote.Locator]remote.Storage{r.locator: r.store})
	o.Experimental.CreateOnShared = r.strategy
	o.Experimental.CreateOnSharedLocator = r.locator
	if o.FormatMajorVersion < pebble.FormatMinForSharedObjects {
		o.FormatMajorVersion = pebble.FormatMinForSharedObjects
	}
}

// CreatorIDError reports a creator-ID marker file that cannot be read or
// does not hold one decimal non-zero uint64.
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
