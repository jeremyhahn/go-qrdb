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

package statemachine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
)

// TestResolveTypedWithStorage_UnderTheSealBarrier pins that a storage-generic
// state machine may compose every storage while the seal barrier is enabled:
// pebble and file storage, which are opened on the barrier's encfs, and memory
// storage, which persists nothing.
func TestResolveTypedWithStorage_UnderTheSealBarrier(t *testing.T) {
	sealed := atrest.New(sealtest.UnsealedBarrier(t))
	root := t.TempDir()

	for _, tc := range []struct{ smType, kind string }{
		{"disk", "file"},
		{"disk", "pebble"},
		{"disk", ""},
		{"fast", "pebble"},
		{"fast", "file"},
		{"disk", "memory"},
		{"fast", "memory"},
		{"memory", ""},
		{"highperf", ""},
	} {
		if _, err := ResolveTypedWithStorage(tc.smType, "", StorageConfig{Root: root, Kind: tc.kind, Encryptor: sealed}); err != nil {
			t.Errorf("%s over %q under the barrier: %v", tc.smType, tc.kind, err)
		}
	}

	// Nor with the barrier off.
	for _, enc := range []*atrest.Encryptor{nil, atrest.New(nil)} {
		if _, err := ResolveTypedWithStorage("disk", "", StorageConfig{Root: root, Kind: "file", Encryptor: enc}); err != nil {
			t.Errorf("disk over file with the barrier off: %v", err)
		}
	}
}

// TestStorageGenericSMsEncryptStorageUnderTheBarrier is the other half: under
// the seal barrier, disk and fast over pebble or file storage write only
// ciphertext — neither the sentinel key nor the sentinel value written through
// the state machine appears in any file under the replica's directory — and a
// replica restarted with the same barrier reads it back.
func TestStorageGenericSMsEncryptStorageUnderTheBarrier(t *testing.T) {
	const (
		sentinelKey = "SEALED-SENTINEL-PLAINTEXT-KEY"
		sentinel    = "SEALED-SENTINEL-PLAINTEXT-VALUE"
	)
	sealed := atrest.New(sealtest.UnsealedBarrier(t))
	ctx := context.Background()

	for _, tc := range []struct{ smType, kind string }{
		{"disk", "pebble"}, {"fast", "pebble"}, {"disk", "file"}, {"fast", "file"},
	} {
		smType := tc.smType
		t.Run(smType+"/"+tc.kind, func(t *testing.T) {
			sc := StorageConfig{Root: t.TempDir(), Kind: tc.kind, Encryptor: sealed}
			open := func() interface{} {
				createFn, err := ResolveTypedWithStorage(smType, "", sc)
				if err != nil {
					t.Fatalf("ResolveTypedWithStorage: %v", err)
				}
				machine := createFn(1, 1)
				if d, ok := machine.(sm.DiskStateMachine); ok {
					if _, err := d.Open(ctx, "", nil); err != nil {
						t.Fatalf("Open: %v", err)
					}
				}
				return machine
			}
			type updater interface {
				Update(context.Context, []sm.Entry, []sm.Result) error
				Lookup(context.Context, interface{}) (interface{}, error)
				Close(context.Context) error
			}

			m := open().(updater)
			if err := m.Update(ctx, []sm.Entry{{Index: 1, Cmd: mustEncode(kv.EncodePut([]byte(sentinelKey), []byte(sentinel)))}}, make([]sm.Result, 1)); err != nil {
				t.Fatalf("Update: %v", err)
			}
			if err := m.Close(ctx); err != nil {
				t.Fatalf("Close: %v", err)
			}

			err := filepath.WalkDir(sc.Root, func(p string, d os.DirEntry, walkErr error) error {
				if walkErr != nil || d.IsDir() || strings.HasSuffix(p, "LOCK") {
					return walkErr
				}
				// #nosec G122 -- the tree walked is this test's own t.TempDir().
				body, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				for _, plain := range []string{sentinelKey, sentinel} {
					if bytes.Contains(body, []byte(plain)) {
						t.Errorf("plaintext %q leaked in %s", plain, p)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatalf("WalkDir: %v", err)
			}

			// The disk state machine resumes from its storage; the fast one is
			// rebuilt by Raft, so only disk reads its value back here.
			if smType == "disk" {
				m2 := open().(updater)
				t.Cleanup(func() { _ = m2.Close(ctx) })
				got, err := m2.Lookup(ctx, []byte(sentinelKey))
				if err != nil || string(got.([]byte)) != sentinel {
					t.Fatalf("restarted replica read %v, %v; want the sentinel", got, err)
				}
			}
		})
	}
}
