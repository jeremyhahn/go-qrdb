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

package validation

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAbsoluteCanonicalPath(t *testing.T) {
	root := t.TempDir()

	for _, tc := range []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"absolute canonical", filepath.Join(root, "vol"), false},
		{"empty", "", true},
		{"relative", "data/vol", true},
		// Concatenated, not Join'd: Join cleans, so it cannot build these.
		{"dot-dot component", root + "/a/../b", true},
		{"trailing separator", root + "/vol/", true},
		{"doubled separator", root + "//vol", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := AbsoluteCanonicalPath(tc.path)
			if tc.wantErr && err == nil {
				t.Fatalf("accepted %q", tc.path)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("rejected %q: %v", tc.path, err)
			}
			if err == nil {
				return
			}
			var pathErr *PathError
			if !errors.As(err, &pathErr) {
				t.Fatalf("got %T, want *PathError", err)
			}
			if pathErr.Reason == "" {
				t.Error("PathError carries no reason")
			}
		})
	}
}

// The symlink predicate is the one filepath.Clean cannot substitute for: a
// perfectly canonical path can still be a symlink to somewhere else, because
// Clean reasons lexically and the syscall follows the link physically.
func TestRejectSymlinkedPath(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := RejectSymlinkedPath(real); err != nil {
		t.Errorf("rejected a real directory: %v", err)
	}
	if err := RejectSymlinkedPath(link); err == nil {
		t.Error("accepted a symlink")
	}
	// The ancestor case: the leaf does not exist, but its parent is a link.
	if err := RejectSymlinkedPath(filepath.Join(link, "vol")); err == nil {
		t.Error("accepted a path whose existing ancestor is a symlink")
	}
	// A leaf that does not exist under a real ancestor is fine: the caller
	// creates the leaf rather than following it.
	if err := RejectSymlinkedPath(filepath.Join(real, "vol")); err != nil {
		t.Errorf("rejected a not-yet-created leaf under a real ancestor: %v", err)
	}
}

// The separator-aware property, which is the whole reason these are not
// strings.HasPrefix.
func TestPathsOverlapIsSeparatorAware(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"/data/foo", "/data/foo", true},
		{"/data/foo", "/data/foo/bar", true},
		{"/data/foo/bar", "/data/foo", true},
		{"/data/foo", "/data/foo2", false},
		{"/data/foo2", "/data/foo", false},
		{"/data/foo", "/data/bar", false},
	} {
		if got := PathsOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("PathsOverlap(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestIsPathAncestorIsStrict(t *testing.T) {
	if IsPathAncestor("/data/foo", "/data/foo") {
		t.Error("a path is not a STRICT ancestor of itself")
	}
	if !IsPathAncestor("/data/foo", "/data/foo/bar") {
		t.Error("/data/foo must be an ancestor of /data/foo/bar")
	}
	if IsPathAncestor("/data/foo", "/data/foo2") {
		t.Error("/data/foo2 is a sibling, not a child, of /data/foo")
	}
	if IsPathAncestor("/data/foo/bar", "/data/foo") {
		t.Error("ancestry must not be symmetric")
	}
}

func TestIsPathAncestorRejectsUnrelatablePaths(t *testing.T) {
	// filepath.Rel cannot relate an absolute path to a relative one.
	if IsPathAncestor("/data/foo", "data/foo/bar") {
		t.Error("an absolute path must not be an ancestor of a relative one")
	}
	// Unequal strings that name the same directory are not strict ancestors.
	if IsPathAncestor("/data/foo/", "/data/foo") {
		t.Error("a path with a trailing separator is not a STRICT ancestor of itself")
	}
}

func TestRejectSymlinkedPathReportsStatFailure(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// A component under a regular file fails Lstat with ENOTDIR, which is
	// not a not-exist error and must not be walked past.
	path := filepath.Join(file, "vol")
	err := RejectSymlinkedPath(path)
	var pathErr *PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("RejectSymlinkedPath(%q) = %v, want *PathError", path, err)
	}
	if pathErr.Path != path {
		t.Errorf("PathError.Path = %q, want %q", pathErr.Path, path)
	}
	if !strings.HasPrefix(pathErr.Reason, "stat failed: ") {
		t.Errorf("PathError.Reason = %q, want a stat failure", pathErr.Reason)
	}
	want := "validation: invalid path " + strconv.Quote(path) + ": " + pathErr.Reason
	if got := pathErr.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestRejectSymlinkedPathReportsResolveFailure(t *testing.T) {
	// A self-referencing link exists to Lstat but cannot be resolved.
	loop := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	err := RejectSymlinkedPath(loop)
	var pathErr *PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("RejectSymlinkedPath(%q) = %v, want *PathError", loop, err)
	}
	if !strings.HasPrefix(pathErr.Reason, "resolve symlinks failed: ") {
		t.Errorf("PathError.Reason = %q, want a resolve failure", pathErr.Reason)
	}
}
