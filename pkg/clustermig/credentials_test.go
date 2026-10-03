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

package clustermig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

func systemMigration(gen uint64) *ClusterMigration {
	return &ClusterMigration{Target: seal.StrategyPassphrase, Owners: []string{"n1"}, Generation: gen}
}

func tenantMigration(tenantID string, gen uint64) *ClusterMigration {
	return &ClusterMigration{TenantID: tenantID, Target: seal.StrategyPassphrase, Owners: []string{"n1"}, Generation: gen}
}

func writeCredential(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

// credDir returns a credentials directory as an operator makes one: owned by
// this user and writable by no one else (t.TempDir follows the umask).
func credDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- a directory needs its search bit; this is the operator's mode
		t.Fatal(err)
	}
	return dir
}

func credentialOf(t *testing.T, l *LocalCredentials, m *ClusterMigration) ([]byte, error) {
	t.Helper()
	return l.Credential(context.Background(), m)
}

// TestLocalCredentials_ReadsThisNodesFile proves an owner reads its own
// credential from the file named for the barrier: "system" for the node
// barrier, "tenant-<id>" for a tenant barrier. One trailing newline is not
// part of the credential.
func TestLocalCredentials_ReadsThisNodesFile(t *testing.T) {
	dir := credDir(t)
	writeCredential(t, dir, "system", "node-pass\n", 0o600)
	writeCredential(t, dir, "tenant-acme", "acme-pass\r\n", 0o400)
	l := NewLocalCredentials(dir)

	cases := []struct {
		m    *ClusterMigration
		want string
	}{
		{systemMigration(1), "node-pass"},
		{tenantMigration("acme", 1), "acme-pass"},
	}
	for _, tc := range cases {
		got, err := credentialOf(t, l, tc.m)
		if err != nil || string(got) != tc.want {
			t.Fatalf("Credential(%q) = %q, %v; want %q", tc.m.TenantID, got, err, tc.want)
		}
	}
}

// TestLocalCredentials_AbsentFileIsAwaited proves a missing file, or no
// directory configured, means the credential has not arrived yet.
func TestLocalCredentials_AbsentFileIsAwaited(t *testing.T) {
	for name, l := range map[string]*LocalCredentials{
		"absent file": NewLocalCredentials(credDir(t)),
		"no dir":      NewLocalCredentials(""),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := credentialOf(t, l, systemMigration(1)); !errors.Is(err, ErrCredentialAwaited) {
				t.Fatalf("err = %v, want ErrCredentialAwaited", err)
			}
		})
	}
}

// TestLocalCredentials_RefusesAnUnsafeFile proves a credential file readable
// by anyone but the daemon's user -- or not a plain file, or empty, or owned
// by another user -- is refused with *MigrationCredentialFileError naming it.
func TestLocalCredentials_RefusesAnUnsafeFile(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string) *LocalCredentials{
		"group readable": func(t *testing.T, dir string) *LocalCredentials {
			writeCredential(t, dir, "system", "pass", 0o640)
			return NewLocalCredentials(dir)
		},
		"world readable": func(t *testing.T, dir string) *LocalCredentials {
			writeCredential(t, dir, "system", "pass", 0o604)
			return NewLocalCredentials(dir)
		},
		"empty": func(t *testing.T, dir string) *LocalCredentials {
			writeCredential(t, dir, "system", "", 0o600)
			return NewLocalCredentials(dir)
		},
		"only a newline": func(t *testing.T, dir string) *LocalCredentials {
			writeCredential(t, dir, "system", "\n", 0o600)
			return NewLocalCredentials(dir)
		},
		"a directory": func(t *testing.T, dir string) *LocalCredentials {
			if err := os.Mkdir(filepath.Join(dir, "system"), 0o700); err != nil {
				t.Fatal(err)
			}
			return NewLocalCredentials(dir)
		},
		"a symlink": func(t *testing.T, dir string) *LocalCredentials {
			target := writeCredential(t, dir, "elsewhere", "pass", 0o600)
			if err := os.Symlink(target, filepath.Join(dir, "system")); err != nil {
				t.Fatal(err)
			}
			return NewLocalCredentials(dir)
		},
		"too large": func(t *testing.T, dir string) *LocalCredentials {
			writeCredential(t, dir, "system", strings.Repeat("x", MaxCredentialFileSize+1), 0o600)
			return NewLocalCredentials(dir)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			dir := credDir(t)
			l := setup(t, dir)
			_, err := credentialOf(t, l, systemMigration(1))
			var fe *MigrationCredentialFileError
			if !errors.As(err, &fe) {
				t.Fatalf("err = %v, want *MigrationCredentialFileError", err)
			}
			if fe.Path != filepath.Join(dir, "system") || fe.Reason == "" {
				t.Fatalf("error = %+v, want the path and a reason", fe)
			}
		})
	}
}

// TestLocalCredentials_RefusesAnUnsafeDirectory proves the credentials
// directory is held to the file's standard: owned by the daemon's user,
// writable by no one else -- a directory others can write lets them replace
// the file -- and a real directory, not a symlink to one.
func TestLocalCredentials_RefusesAnUnsafeDirectory(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string) *LocalCredentials{
		"group writable": func(t *testing.T, dir string) *LocalCredentials {
			writeCredential(t, dir, "system", "pass", 0o600)
			chmodDir(t, dir, 0o770)
			return NewLocalCredentials(dir)
		},
		"world writable": func(t *testing.T, dir string) *LocalCredentials {
			writeCredential(t, dir, "system", "pass", 0o600)
			chmodDir(t, dir, 0o703)
			return NewLocalCredentials(dir)
		},
		"another user's": func(t *testing.T, dir string) *LocalCredentials {
			writeCredential(t, dir, "system", "pass", 0o600)
			l := NewLocalCredentials(dir)
			l.uid++ // the daemon's user owns neither the directory nor the file
			return l
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			dir := credDir(t)
			l := setup(t, dir)
			_, err := credentialOf(t, l, systemMigration(1))
			var fe *MigrationCredentialFileError
			if !errors.As(err, &fe) || fe.Path != dir || fe.Reason == "" {
				t.Fatalf("err = %v, want *MigrationCredentialFileError naming the directory", err)
			}
		})
	}
	t.Run("a symlink", func(t *testing.T) {
		real := credDir(t)
		writeCredential(t, real, "system", "pass", 0o600)
		link := filepath.Join(t.TempDir(), "creds")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		_, err := credentialOf(t, NewLocalCredentials(link), systemMigration(1))
		var fe *MigrationCredentialFileError
		if !errors.As(err, &fe) || fe.Path != link {
			t.Fatalf("err = %v, want *MigrationCredentialFileError naming the symlinked directory", err)
		}
	})
}

// chmodDir sets dir's mode and restores it for t.TempDir's cleanup.
func chmodDir(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(dir, mode); err != nil { // #nosec G302 -- the test widens its own temp directory to prove the refusal
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // #nosec G302 -- restores the temp directory for t.TempDir's cleanup
}

// TestLocalCredentials_WipesTheTrimmedNewline proves the bytes trimmed off
// the credential are wiped, not left behind in the buffer the caller holds.
func TestLocalCredentials_WipesTheTrimmedNewline(t *testing.T) {
	dir := credDir(t)
	writeCredential(t, dir, "system", "pass\r\n", 0o600)
	got, err := credentialOf(t, NewLocalCredentials(dir), systemMigration(1))
	if err != nil || string(got) != "pass" {
		t.Fatalf("Credential = %q, %v", got, err)
	}
	if tail := got[len(got):cap(got)]; strings.Trim(string(tail), "\x00") != "" {
		t.Fatalf("trimmed bytes %q left in the buffer", tail)
	}
}

// TestLocalCredentials_RefusesATenantIDThatIsNotAFileName proves a tenant ID
// can never steer the read outside the credentials directory.
func TestLocalCredentials_RefusesATenantIDThatIsNotAFileName(t *testing.T) {
	dir := credDir(t)
	l := NewLocalCredentials(dir)
	for _, id := range []string{"../system", "a/b", "..", "x\x00y"} {
		_, err := credentialOf(t, l, tenantMigration(id, 1))
		var fe *MigrationCredentialFileError
		if !errors.As(err, &fe) {
			t.Fatalf("tenant %q: err = %v, want *MigrationCredentialFileError", id, err)
		}
	}
}

// TestLocalCredentials_HeldRequestCredential proves the node that accepted
// the request uses the request's credential, held in memory for that
// migration's generation only, ahead of any file; and that Release destroys
// it.
func TestLocalCredentials_HeldRequestCredential(t *testing.T) {
	dir := credDir(t)
	writeCredential(t, dir, "system", "file-pass", 0o600)
	l := NewLocalCredentials(dir)
	m := systemMigration(2)
	if err := l.Hold(m, []byte("request-pass")); err != nil {
		t.Fatal(err)
	}
	got, err := credentialOf(t, l, m)
	if err != nil || string(got) != "request-pass" {
		t.Fatalf("Credential = %q, %v; want the held request credential", got, err)
	}
	// It is a copy: wiping it leaves the held credential intact for a retry.
	clear(got)
	if again, err := credentialOf(t, l, m); err != nil || string(again) != "request-pass" {
		t.Fatalf("second Credential = %q, %v", again, err)
	}
	// Another generation of the same barrier does not see it.
	if other, err := credentialOf(t, l, systemMigration(3)); err != nil || string(other) != "file-pass" {
		t.Fatalf("other generation = %q, %v; want the file", other, err)
	}
	l.Release(m)
	if after, err := credentialOf(t, l, m); err != nil || string(after) != "file-pass" {
		t.Fatalf("after Release = %q, %v; want the file", after, err)
	}
}

// TestLocalCredentials_HoldReplacesAnEarlierGeneration proves a re-issued
// request replaces the credential an earlier generation held, and that
// releasing the earlier generation leaves the newer one alone.
func TestLocalCredentials_HoldReplacesAnEarlierGeneration(t *testing.T) {
	l := NewLocalCredentials("")
	old, cur := tenantMigration("acme", 1), tenantMigration("acme", 2)
	if err := l.Hold(old, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := l.Hold(cur, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if _, err := credentialOf(t, l, old); !errors.Is(err, ErrCredentialAwaited) {
		t.Fatalf("the replaced generation: err = %v, want ErrCredentialAwaited", err)
	}
	l.Release(old)
	if got, err := credentialOf(t, l, cur); err != nil || string(got) != "new" {
		t.Fatalf("Credential = %q, %v; want new", got, err)
	}
}

// TestLocalCredentials_HoldRefusesAnEmptyCredential proves there is nothing to
// hold without a credential.
func TestLocalCredentials_HoldRefusesAnEmptyCredential(t *testing.T) {
	if err := NewLocalCredentials("").Hold(systemMigration(1), nil); !errors.Is(err, ErrEmptyCredential) {
		t.Fatalf("err = %v, want ErrEmptyCredential", err)
	}
}

// TestMigrationCredentialFileError_Message names the file and the reason.
func TestMigrationCredentialFileError_Message(t *testing.T) {
	err := &MigrationCredentialFileError{Path: "/c/system", Reason: "empty"}
	if msg := err.Error(); !strings.Contains(msg, "/c/system") || !strings.Contains(msg, "empty") {
		t.Fatalf("message %q", msg)
	}
}

// TestLocalCredentials_ReleaseLeavesOthersAlone proves Release touches only
// the generation it names: nothing held, or another generation held, is a
// no-op.
func TestLocalCredentials_ReleaseLeavesOthersAlone(t *testing.T) {
	l := NewLocalCredentials("")
	l.Release(systemMigration(1)) // nothing held
	if err := l.Hold(systemMigration(2), []byte("pass")); err != nil {
		t.Fatal(err)
	}
	l.Release(systemMigration(1))
	if got, err := credentialOf(t, l, systemMigration(2)); err != nil || string(got) != "pass" {
		t.Fatalf("Credential = %q, %v; releasing another generation dropped it", got, err)
	}
}

// TestLocalCredentials_UnreadableDirectoryIsRefused proves a credential path
// that cannot be examined is refused with the reason, not reported as absent.
func TestLocalCredentials_UnreadableDirectoryIsRefused(t *testing.T) {
	dir := credDir(t)
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCredential(t, locked, "system", "pass", 0o600)
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) // #nosec G302 -- restores the temp directory's search bit so t.TempDir can remove it
	_, err := credentialOf(t, NewLocalCredentials(locked), systemMigration(1))
	var fe *MigrationCredentialFileError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want *MigrationCredentialFileError", err)
	}
}

// TestLocalCredentials_DirectoryShapes covers the directory's other shapes: a
// directory not created yet is awaited like a missing file, and a file where
// the directory should be is refused.
func TestLocalCredentials_DirectoryShapes(t *testing.T) {
	missing := filepath.Join(credDir(t), "not-yet")
	if _, err := credentialOf(t, NewLocalCredentials(missing), systemMigration(1)); !errors.Is(err, ErrCredentialAwaited) {
		t.Fatalf("missing directory: err = %v, want ErrCredentialAwaited", err)
	}
	notDir := writeCredential(t, credDir(t), "creds", "x", 0o600)
	var fe *MigrationCredentialFileError
	if _, err := credentialOf(t, NewLocalCredentials(notDir), systemMigration(1)); !errors.As(err, &fe) || fe.Path != notDir {
		t.Fatalf("a file as the directory: err = %v, want *MigrationCredentialFileError naming it", err)
	}
}
