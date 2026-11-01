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

package filesys

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestDirSyncSupported_FollowsPlatform pins that Default syncs directory
// handles everywhere but Windows, where FlushFileBuffers fails on one.
func TestDirSyncSupported_FollowsPlatform(t *testing.T) {
	if want := runtime.GOOS != "windows"; dirSyncSupported != want {
		t.Errorf("dirSyncSupported = %v on %s, want %v", dirSyncSupported, runtime.GOOS, want)
	}
}

func TestOpenDir_SyncSupportedReturnsOSFile(t *testing.T) {
	d, err := openDir(t.TempDir(), true)
	if err != nil {
		t.Fatalf("openDir: %v", err)
	}
	defer func() { _ = d.Close() }()
	if _, ok := d.(*os.File); !ok {
		t.Errorf("openDir(syncSupported) = %T, want *os.File", d)
	}
}

// TestOpenDir_SyncUnsupportedSyncIsNoOp proves Sync never reaches the
// handle where the platform cannot flush one: it succeeds even after the
// handle is closed, where the *os.File's own Sync fails.
func TestOpenDir_SyncUnsupportedSyncIsNoOp(t *testing.T) {
	d, err := openDir(t.TempDir(), false)
	if err != nil {
		t.Fatalf("openDir: %v", err)
	}
	u, ok := d.(unsyncableDir)
	if !ok {
		t.Fatalf("openDir(!syncSupported) = %T, want unsyncableDir", d)
	}
	fi, err := d.Stat()
	if err != nil || !fi.IsDir() {
		t.Errorf("Stat dir = %v, %v; want a directory", fi, err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close dir: %v", err)
	}
	if err := u.File.Sync(); err == nil {
		t.Fatal("Sync of the closed *os.File succeeded; the test proves nothing")
	}
	if err := d.Sync(); err != nil {
		t.Errorf("Sync dir = %v, want nil", err)
	}
}

func TestOpenDir_MissingFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	for _, syncSupported := range []bool{true, false} {
		d, err := openDir(missing, syncSupported)
		if !errors.Is(err, fs.ErrNotExist) || d != nil {
			t.Errorf("openDir(missing, %v) = %v, %v; want nil, ErrNotExist", syncSupported, d, err)
		}
	}
}
