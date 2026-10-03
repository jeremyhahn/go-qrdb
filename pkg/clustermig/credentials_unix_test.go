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

//go:build unix

package clustermig

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestLocalCredentials_RefusesAFIFOWithoutBlocking proves a FIFO in place of
// the credential file is refused at once: the file is opened non-blocking and
// checked on the descriptor, so a writer-less FIFO cannot hang the
// reconciler.
func TestLocalCredentials_RefusesAFIFOWithoutBlocking(t *testing.T) {
	dir := credDir(t)
	if err := syscall.Mkfifo(filepath.Join(dir, "system"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := credentialOf(t, NewLocalCredentials(dir), systemMigration(1))
		done <- err
	}()
	select {
	case err := <-done:
		var fe *MigrationCredentialFileError
		if !errors.As(err, &fe) {
			t.Fatalf("err = %v, want *MigrationCredentialFileError", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO credential file blocked")
	}
}

// TestLocalCredentials_DirectorySwappedAfterVettingIsNotRead closes the
// check-then-open window: the credentials directory is vetted on the
// descriptor it is opened as, and the file is opened relative to that
// descriptor, so a directory moved away and replaced at the same path after
// the vetting is never the one read -- the vetted directory's file is.
func TestLocalCredentials_DirectorySwappedAfterVettingIsNotRead(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "creds")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCredential(t, dir, "system", "vetted", 0o600)
	l := NewLocalCredentials(dir)
	vetted, err := l.openDir()
	if err != nil {
		t.Fatalf("openDir: %v", err)
	}
	defer vetted.Close()
	if err := os.Rename(dir, filepath.Join(root, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCredential(t, dir, "system", "attacker", 0o600)
	got, err := l.readAt(vetted, "system")
	if err != nil {
		t.Fatalf("readAt: %v", err)
	}
	if string(got) != "vetted" {
		t.Fatalf("read %q after the directory was swapped; want the vetted directory's file", got)
	}
}

// TestLocalCredentials_FileSymlinkInsideTheDirectoryIsRefused: the file is
// opened relative to the directory without following a symlink, so a link in
// the vetted directory pointing anywhere else is refused, not followed.
func TestLocalCredentials_FileSymlinkInsideTheDirectoryIsRefused(t *testing.T) {
	dir := credDir(t)
	target := writeCredential(t, t.TempDir(), "elsewhere", "outside", 0o600)
	if err := os.Symlink(target, filepath.Join(dir, "system")); err != nil {
		t.Fatal(err)
	}
	_, err := credentialOf(t, NewLocalCredentials(dir), systemMigration(1))
	var fe *MigrationCredentialFileError
	if !errors.As(err, &fe) || fe.Path != filepath.Join(dir, "system") {
		t.Fatalf("err = %v, want *MigrationCredentialFileError naming the symlinked file", err)
	}
}
