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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/objstorage/remote"
	"github.com/jeremyhahn/go-objstore/pkg/memory"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

const remoteTestKeys = 50

// writeRemoteKeys writes the test keys and flushes them to SSTables.
func writeRemoteKeys(t *testing.T, s *PebbleStorage) {
	t.Helper()
	for i := 0; i < remoteTestKeys; i++ {
		if err := s.Set([]byte(fmt.Sprintf("key-%03d", i)), []byte(fmt.Sprintf("value-%03d", i))); err != nil {
			t.Fatalf("Set %d: %v", i, err)
		}
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

// checkRemoteKeys reads every test key back.
func checkRemoteKeys(t *testing.T, s *PebbleStorage) {
	t.Helper()
	for i := 0; i < remoteTestKeys; i++ {
		got, err := s.Get([]byte(fmt.Sprintf("key-%03d", i)))
		if err != nil {
			t.Fatalf("Get %d: %v", i, err)
		}
		if want := fmt.Sprintf("value-%03d", i); string(got) != want {
			t.Fatalf("Get %d = %q, want %q", i, got, want)
		}
	}
}

// hasSST reports whether any name ends in ".sst".
func hasSST(names []string) bool {
	for _, n := range names {
		if strings.HasSuffix(n, ".sst") {
			return true
		}
	}
	return false
}

// readCreatorID parses dir's creator-ID marker file.
func readCreatorID(t *testing.T, dir string) uint64 {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, creatorIDFile))
	if err != nil {
		t.Fatalf("read creator id: %v", err)
	}
	id, err := strconv.ParseUint(strings.TrimSuffix(string(data), "\n"), 10, 64)
	if err != nil || id == 0 {
		t.Fatalf("creator id file holds %q", data)
	}
	return id
}

func openRemote(t *testing.T, dir string, rs remote.Storage) (*PebbleStorage, error) {
	t.Helper()
	return New(TestConfig(dir), WithRemote(rs, "qrdb", remote.CreateOnSharedAll))
}

func TestWithRemote_RoundTripAndCreatorID(t *testing.T) {
	dir := t.TempDir()
	rs := remote.NewInMem()

	s, err := openRemote(t, dir, rs)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeRemoteKeys(t, s)
	names, err := rs.List("", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !hasSST(names) {
		t.Fatalf("remote storage holds no .sst object: %v", names)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	id := readCreatorID(t, dir)

	s, err = openRemote(t, dir, rs)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	checkRemoteKeys(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := readCreatorID(t, dir); got != id {
		t.Fatalf("creator id changed across reopen: %d -> %d", id, got)
	}
}

func TestWithRemote_ChangedCreatorIDRefused(t *testing.T) {
	dir := t.TempDir()
	rs := remote.NewInMem()
	s, err := openRemote(t, dir, rs)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeRemoteKeys(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	other := readCreatorID(t, dir) + 1
	if other == 0 {
		other = 1
	}
	if err := os.WriteFile(filepath.Join(dir, creatorIDFile), []byte(strconv.FormatUint(other, 10)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := openRemote(t, dir, rs); err == nil {
		_ = s.Close()
		t.Fatal("reopen with a changed creator id succeeded")
	}
}

func TestWithRemote_BadCreatorIDFile(t *testing.T) {
	for _, contents := range []string{"garbage\n", "0\n", "12", ""} {
		t.Run(strconv.Quote(contents), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, creatorIDFile), []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := openRemote(t, dir, remote.NewInMem())
			if err == nil {
				_ = s.Close()
				t.Fatal("New accepted a bad creator id file")
			}
			var cerr *CreatorIDError
			if !errors.As(err, &cerr) {
				t.Fatalf("err = %v, want *CreatorIDError", err)
			}
		})
	}
}

func TestWithRemote_Refusals(t *testing.T) {
	tests := []struct {
		name     string
		store    remote.Storage
		locator  remote.Locator
		strategy remote.CreateOnSharedStrategy
		noWAL    bool
		field    string
	}{
		{"nil store", nil, "qrdb", remote.CreateOnSharedAll, false, "Remote"},
		{"empty locator", remote.NewInMem(), "", remote.CreateOnSharedAll, false, "Remote"},
		{"strategy none", remote.NewInMem(), "qrdb", remote.CreateOnSharedNone, false, "Remote"},
		{"wal disabled", remote.NewInMem(), "qrdb", remote.CreateOnSharedAll, true, "DisableWAL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := TestConfig(dir)
			cfg.DisableWAL = tt.noWAL
			s, err := New(cfg, WithRemote(tt.store, tt.locator, tt.strategy))
			if err == nil {
				_ = s.Close()
				t.Fatal("New succeeded")
			}
			var cerr *ConfigError
			if !errors.As(err, &cerr) || cerr.Field != tt.field {
				t.Fatalf("err = %v, want *ConfigError on %s", err, tt.field)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("DataDir holds %d entries after a refusal", len(entries))
			}
		})
	}
}

func TestWithoutRemote_NoCreatorIDFile(t *testing.T) {
	dir := t.TempDir()
	s, err := New(TestConfig(dir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, creatorIDFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("creator id file stat err = %v, want not exist", err)
	}
}

// fakeBarrier stands in for a seal barrier (pkg/seal imports this package):
// it prefixes a fixed byte and refuses input without it.
type fakeBarrier struct{}

const fakeBarrierTag = 0x5a

func (fakeBarrier) Encrypt(p []byte) ([]byte, error) {
	return append([]byte{fakeBarrierTag}, p...), nil
}

func (fakeBarrier) Decrypt(c []byte) ([]byte, error) {
	if len(c) == 0 || c[0] != fakeBarrierTag {
		return nil, errors.New("fake barrier: not sealed")
	}
	return bytes.Clone(c[1:]), nil
}

func TestWithRemote_EncryptedRemoteRoundTrip(t *testing.T) {
	raw := memory.New()
	inner, err := objstore.NewRemote(raw, "tenant/db1")
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}
	rs, err := objstore.NewEncryptedRemote(inner, fakeBarrier{})
	if err != nil {
		t.Fatalf("NewEncryptedRemote: %v", err)
	}
	dir := t.TempDir()

	s, err := openRemote(t, dir, rs)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	writeRemoteKeys(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	keys, err := raw.ListWithContext(context.Background(), "tenant/db1/")
	if err != nil {
		t.Fatalf("raw list: %v", err)
	}
	if !hasSST(keys) {
		t.Fatalf("raw store holds no .sst object under tenant/db1/: %v", keys)
	}
	for _, k := range keys {
		if !strings.HasPrefix(k, "tenant/db1/") {
			t.Fatalf("raw object %q outside tenant/db1/", k)
		}
	}

	s, err = openRemote(t, dir, rs)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	checkRemoteKeys(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
