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

package objstore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

func TestOpenStore_MemoryWritesNoMarker(t *testing.T) {
	store, err := objstore.OpenStore("memory", map[string]string{})
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	names, err := store.ListWithContext(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("OpenStore wrote objects %v; want an empty store (no format marker)", names)
	}
	if err := objstore.EnsureLSMFormat(context.Background(), store); err != nil {
		t.Fatalf("EnsureLSMFormat on the opened store: %v", err)
	}
}

func TestOpenStore_ArchiveTierRefused(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-key-file")
	for _, provider := range []string{"glacier", "azurearchive"} {
		t.Run(provider, func(t *testing.T) {
			for name, opts := range map[string][]objstore.Option{
				"no credentials": nil,
				// A credential file that does not exist: the refusal comes
				// before any credential file is read.
				"missing credential file": {objstore.WithCredentialFiles(objstore.CredentialFiles{KeyFile: missing})},
			} {
				_, err := objstore.OpenStore(provider, map[string]string{}, opts...)
				var archive *objstore.ArchiveTierUnsupportedError
				if !errors.As(err, &archive) {
					t.Fatalf("%s: err = %v; want *ArchiveTierUnsupportedError", name, err)
				}
				if archive.Provider != provider {
					t.Fatalf("%s: Provider = %q; want %q", name, archive.Provider, provider)
				}
			}
		})
	}
}

func TestOpenStore_InlineCredentialRefused(t *testing.T) {
	_, err := objstore.OpenStore("memory", map[string]string{"secret_key": "not-a-real-secret"})
	var settingErr *objstore.CredentialSettingError
	if !errors.As(err, &settingErr) {
		t.Fatalf("err = %v; want *CredentialSettingError", err)
	}
}

func TestOpenStore_EmptyProvider(t *testing.T) {
	if _, err := objstore.OpenStore("", map[string]string{}); !errors.Is(err, storage.ErrInvalidPath) {
		t.Fatalf("err = %v; want storage.ErrInvalidPath", err)
	}
}

func TestOpenStore_UnknownProvider(t *testing.T) {
	_, err := objstore.OpenStore("no-such-provider", map[string]string{})
	var unsupported *objstore.UnsupportedProviderError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v; want *UnsupportedProviderError", err)
	}
}

func TestOpenStore_LSMStoreRefusedByFlatEngine(t *testing.T) {
	settings := map[string]string{"path": t.TempDir()}
	store, err := objstore.OpenStore("local", settings)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := objstore.EnsureLSMFormat(context.Background(), store); err != nil {
		t.Fatalf("EnsureLSMFormat: %v", err)
	}
	_, err = objstore.New("local", settings)
	var mismatch *objstore.FormatVersionMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("flat New on an LSM store: err = %v; want *FormatVersionMismatchError", err)
	}
	if mismatch.Found != "3" {
		t.Fatalf("Found = %q; want \"3\"", mismatch.Found)
	}
}

// TestOpenStore_SymlinkRootFormatAndList: a local bucket whose root is a
// symlink is opened on its physical root, so a fresh one takes the LSM format
// marker, a marked one passes the format check on reopen, and its Remote lists
// the objects written through it.
func TestOpenStore_SymlinkRootFormatAndList(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	ctx := context.Background()
	open := func() *objstore.Remote {
		t.Helper()
		store, err := objstore.OpenStore("local", map[string]string{"path": link})
		if err != nil {
			t.Fatalf("OpenStore: %v", err)
		}
		if err := objstore.EnsureLSMFormat(ctx, store); err != nil {
			t.Fatalf("EnsureLSMFormat on the symlinked root: %v", err)
		}
		rs, err := objstore.NewRemote(store, "lsm")
		if err != nil {
			t.Fatalf("NewRemote: %v", err)
		}
		return rs
	}
	w, err := open().CreateObject("000001.sst")
	if err != nil {
		t.Fatalf("CreateObject: %v", err)
	}
	if _, err := w.Write([]byte("sst")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	names, err := open().List("", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 1 || names[0] != "000001.sst" {
		t.Fatalf("List on the symlinked root = %v; want [000001.sst]", names)
	}
	if _, err := os.Stat(filepath.Join(target, "lsm", "000001.sst")); err != nil {
		t.Fatalf("the object must sit under the physical root: %v", err)
	}
}
