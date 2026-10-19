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

package audit

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// requireOutside fails the test unless err is a *FileError naming path and
// wrapping ErrOutsideTrailDir.
func requireOutside(t *testing.T, err error, path string) {
	t.Helper()
	if !errors.Is(err, ErrOutsideTrailDir) {
		t.Fatalf("err = %v, want ErrOutsideTrailDir", err)
	}
	var fe *FileError
	if !errors.As(err, &fe) || fe.Path != path {
		t.Fatalf("err = %v, want *FileError naming %q", err, path)
	}
}

func TestOpenTrailDir_MissingDirectory(t *testing.T) {
	base := filepath.Join(t.TempDir(), "missing")
	d, err := openTrailDir(base)
	if d != nil {
		t.Fatalf("openTrailDir returned %v for a missing directory", d)
	}
	var fe *FileError
	if !errors.As(err, &fe) || fe.Path != base {
		t.Fatalf("err = %v, want *FileError naming %q", err, base)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

func TestOpenTrailDir_FileIsNotADirectory(t *testing.T) {
	base := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(base, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openTrailDir(base); err == nil {
		t.Fatal("openTrailDir succeeded on a regular file")
	}
}

func TestTrailDir_Name(t *testing.T) {
	dir := t.TempDir()
	d := testTrailDir(t, filepath.Join(dir, "audit.log"))

	tests := []struct {
		name    string
		path    string
		want    string
		outside bool
	}{
		{name: "file below", path: filepath.Join(dir, "audit.log"), want: "audit.log"},
		{name: "nested", path: filepath.Join(dir, "a", "b.log"), want: filepath.Join("a", "b.log")},
		{name: "the directory itself", path: dir, want: "."},
		{name: "parent", path: filepath.Dir(dir), outside: true},
		{name: "sibling", path: filepath.Join(filepath.Dir(dir), "other.log"), outside: true},
		{name: "relative against absolute base", path: "audit.log", outside: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := d.name(tt.path)
			if tt.outside {
				requireOutside(t, err, tt.path)
				if got != "" {
					t.Fatalf("name = %q, want empty on refusal", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("name(%q): %v", tt.path, err)
			}
			if got != tt.want {
				t.Fatalf("name(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestTrailDir_RefusesPathsOutsideTheDirectory(t *testing.T) {
	dir := t.TempDir()
	d := testTrailDir(t, filepath.Join(dir, "audit.log"))
	inside := filepath.Join(dir, "audit.log")
	if err := os.WriteFile(inside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(dir), "escape.log")

	t.Run("openFile", func(t *testing.T) {
		f, err := d.openFile(outside, os.O_CREATE|os.O_WRONLY, 0o600)
		if f != nil {
			_ = f.Close()
			t.Fatal("openFile returned a file outside the directory")
		}
		requireOutside(t, err, outside)
	})
	t.Run("stat", func(t *testing.T) {
		fi, err := d.stat(outside)
		if fi != nil {
			t.Fatalf("stat returned %v outside the directory", fi)
		}
		requireOutside(t, err, outside)
	})
	t.Run("remove", func(t *testing.T) {
		requireOutside(t, d.remove(outside), outside)
	})
	t.Run("rename from outside", func(t *testing.T) {
		requireOutside(t, d.rename(outside, inside), outside)
	})
	t.Run("rename to outside", func(t *testing.T) {
		requireOutside(t, d.rename(inside, outside), outside)
		if _, err := os.Stat(inside); err != nil {
			t.Fatalf("refused rename moved the source: %v", err)
		}
	})
	if _, err := os.Lstat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused operation touched %q: %v", outside, err)
	}
}

func TestTrailDir_InsideOperations(t *testing.T) {
	dir := t.TempDir()
	d := testTrailDir(t, filepath.Join(dir, "audit.log"))
	path := filepath.Join(dir, "audit.log")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}

	fi, err := d.stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() != 3 {
		t.Fatalf("stat size = %d, want 3", fi.Size())
	}
	if err := d.remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := d.stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat after remove: err = %v, want os.ErrNotExist", err)
	}
	if err := d.remove(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second remove: err = %v, want os.ErrNotExist", err)
	}
}

func TestTrailDir_RenameAcrossDirectoriesSyncsBoth(t *testing.T) {
	dir := t.TempDir()
	d := testTrailDir(t, filepath.Join(dir, "audit.log"))
	if err := os.Mkdir(filepath.Join(dir, "archive"), 0o700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "audit.log")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var synced []string
	d.syncFunc = func(f *os.File) error {
		synced = append(synced, filepath.Base(f.Name()))
		return nil
	}

	if err := d.rename(src, filepath.Join(dir, "archive", "audit.1.log")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if len(synced) != 2 || synced[0] != "archive" || synced[1] != "." {
		t.Fatalf("synced = %q, want [archive .]", synced)
	}
}

func TestTrailDir_RenameMissingSourceSyncsNothing(t *testing.T) {
	dir := t.TempDir()
	d := testTrailDir(t, filepath.Join(dir, "audit.log"))
	d.syncFunc = func(*os.File) error {
		t.Fatal("a failed rename synced a directory")
		return nil
	}
	err := d.rename(filepath.Join(dir, "missing.log"), filepath.Join(dir, "audit.1.log"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}
