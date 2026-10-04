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

package sdk_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/util/securefile"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// Compile-time: the re-exported error types cross the SDK boundary as
// aliases of the canonical declarations, not as copies. A copy would compile
// here but fail these assignments.
var (
	_ *securefile.SymlinkRefusedError = (*sdk.SymlinkRefusedError)(nil)
	_ *securefile.FileExistsError     = (*sdk.FileExistsError)(nil)
)

// TestSDK_WriteFileAtomic_RoundTrip proves sdk.WriteFileAtomic reaches the
// same implementation an embedder would otherwise need pkg/util/securefile
// to call (go-qrdb#580).
func TestSDK_WriteFileAtomic_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	if err := sdk.WriteFileAtomic(path, []byte("payload"), 0o600); err != nil {
		t.Fatalf("sdk.WriteFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "payload" {
		t.Fatalf("content = %q err=%v, want \"payload\"", got, err)
	}
}

// TestSDK_WriteFileAtomic_RefusesASymlink proves the typed refusal crosses
// the SDK boundary as the same type a caller's errors.As expects.
func TestSDK_WriteFileAtomic_RefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	path := filepath.Join(dir, "link")
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	var symErr *sdk.SymlinkRefusedError
	if err := sdk.WriteFileAtomic(path, []byte("x"), 0o600); !errors.As(err, &symErr) {
		t.Fatalf("sdk.WriteFileAtomic over a symlink = %v, want *sdk.SymlinkRefusedError", err)
	}
}

// TestSDK_CreateExclusive_RoundTrip proves sdk.CreateExclusive writes a new
// file and refuses to overwrite an existing one.
func TestSDK_CreateExclusive_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	if err := sdk.CreateExclusive(path, []byte("payload"), 0o600); err != nil {
		t.Fatalf("sdk.CreateExclusive: %v", err)
	}

	var existsErr *sdk.FileExistsError
	if err := sdk.CreateExclusive(path, []byte("again"), 0o600); !errors.As(err, &existsErr) {
		t.Fatalf("sdk.CreateExclusive over an existing file = %v, want *sdk.FileExistsError", err)
	}
}
