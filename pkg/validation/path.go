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
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PathError reports that a filesystem path supplied by a caller failed one of
// the containment predicates below. Reason states which one, so a transport
// can surface it without re-deriving the check.
type PathError struct {
	Path   string
	Reason string
}

// Error returns a human-readable description.
func (e *PathError) Error() string {
	return "validation: invalid path " + strconv.Quote(e.Path) + ": " + e.Reason
}

// AbsoluteCanonicalPath reports whether path is safe to hand to a filesystem
// call that will CREATE at it — os.MkdirAll and friends — as opposed to one
// that reads an operator-named file.
//
// The predicates, and why each is here rather than assumed:
//
//   - non-empty, so a blank entry is rejected rather than silently resolving
//     to the process working directory;
//   - absolute, because a relative path is meaningless as a durable storage
//     location independent of the daemon's working directory;
//   - canonical (filepath.Clean(path) == path), so redundant separators or
//     ".." components cannot smuggle the target somewhere else;
//   - no symlink component, checked against the deepest EXISTING ancestor.
//     This one cannot be folded into the canonical check: filepath.Clean
//     reasons about a path lexically, while the syscall follows it
//     physically, so a perfectly canonical /mnt/disk-a/x can still be a
//     symlink to anywhere.
//
// A path whose leaf does not exist yet is accepted: the caller creates the
// leaf rather than following it, and its existing ancestor is what is checked.
func AbsoluteCanonicalPath(path string) error {
	if path == "" {
		return &PathError{Path: path, Reason: "must not be empty"}
	}
	if !filepath.IsAbs(path) {
		return &PathError{Path: path, Reason: "must be an absolute path"}
	}
	if filepath.Clean(path) != path {
		return &PathError{Path: path, Reason: "must be a canonical path (no redundant separators or . / .. components)"}
	}
	return RejectSymlinkedPath(path)
}

// RejectSymlinkedPath fails if path, or its deepest already-existing ancestor,
// traverses a symlink. Split out from AbsoluteCanonicalPath because a caller
// that has already established absoluteness by construction still needs this
// one, and because it is the predicate most easily left out by hand.
func RejectSymlinkedPath(path string) error {
	probe := path
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return &PathError{Path: path, Reason: "stat failed: " + err.Error()}
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			// Reached the filesystem root without finding an existing
			// component; there is no symlink to resolve.
			return nil
		}
		probe = parent
	}
	resolved, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return &PathError{Path: path, Reason: "resolve symlinks failed: " + err.Error()}
	}
	if resolved != probe {
		return &PathError{Path: path, Reason: "path traverses a symlink (" + probe + " resolves to " + resolved + ")"}
	}
	return nil
}

// PathsOverlap reports whether two paths refer to the same directory or one is
// a filesystem ancestor of the other. Both are cleaned first.
//
// The comparison is separator-aware, which is the whole point: a naive
// strings.HasPrefix(b, a) treats /data/foo2 as nested under /data/foo, and it
// is not.
func PathsOverlap(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if a == b {
		return true
	}
	return IsPathAncestor(a, b) || IsPathAncestor(b, a)
}

// IsPathAncestor reports whether parent is a STRICT filesystem ancestor of
// child. Both are assumed cleaned and absolute.
//
// filepath.Rel is what makes this component-aware: child sits under parent
// only when the relative path is neither "." (equal), ".." (parent is below
// child), nor "../"-prefixed (child escapes upward). That is why /data/foo2 is
// not a child of /data/foo — Rel yields "../foo2".
func IsPathAncestor(parent, child string) bool {
	if parent == child {
		return false
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}
