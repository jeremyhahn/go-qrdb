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

package tenant

import (
	"os"
	"path/filepath"
	"strings"
)

// validateExplicitDataRoot validates an operator-supplied dedicated-disk
// DataRoot for CreateTenant's WithDataRoot option. Unlike a pool-allocated
// DataRoot (whose uniqueness and containment are guaranteed by the volume
// pool's slot assignment -- see seal.containedTenantPath), a dedicated disk is
// by definition outside any configured pool, so containment cannot be checked
// against a pool root. Instead this enforces the same path-safety posture --
// the path must be exactly what it claims to be -- plus a collision guard:
//
//   - non-empty: an explicitly requested-but-blank path is rejected rather
//     than silently falling back to pool allocation, so a caller never gets a
//     different placement than the one it asked for without an error.
//   - absolute: a relative path is meaningless as a durable storage location
//     independent of the daemon's current working directory.
//   - canonical: filepath.Clean(path) must equal path, so redundant
//     separators or ".." traversal components cannot smuggle the tenant's
//     storage to an unintended location.
//   - no symlink component: the path (and its deepest existing ancestor) must
//     resolve to itself, so a symlinked directory cannot redirect the tenant's
//     storage to an unintended location after the canonical check passes.
//   - empty on disk: the target directory must not exist yet, or must exist
//     and be empty, so a fresh tenant never silently adopts another tenant's
//     (or a deleted tenant's) leftover data at a reused or mistyped path.
func validateExplicitDataRoot(path string) error {
	if path == "" {
		return ErrEmptyDataRoot
	}
	if !filepath.IsAbs(path) {
		return &InvalidDataRootError{DataRoot: path, Reason: "must be an absolute path"}
	}
	if cleaned := filepath.Clean(path); cleaned != path {
		return &InvalidDataRootError{DataRoot: path, Reason: "must be a canonical path (no redundant separators or . / .. components)"}
	}
	if err := rejectSymlinkedDataRoot(path); err != nil {
		return err
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return &InvalidDataRootError{DataRoot: path, Reason: "stat failed: " + err.Error()}
	}
	if len(entries) > 0 {
		return &DataRootNotEmptyError{DataRoot: path}
	}
	return nil
}

// dataRootsOverlap reports whether two dedicated-disk paths refer to the same
// directory or one is a filesystem ancestor of the other. Both are cleaned
// first. The comparison is separator-aware so /data/foo and /data/foo2 do NOT
// overlap: a naive strings.HasPrefix(b, a) would wrongly treat /data/foo2 as
// nested under /data/foo.
func dataRootsOverlap(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if a == b {
		return true
	}
	return isPathAncestor(a, b) || isPathAncestor(b, a)
}

// isPathAncestor reports whether parent is a strict filesystem ancestor of
// child (both assumed cleaned/absolute). It uses filepath.Rel so the check is
// component-aware: a child sits under parent only when the relative path is
// neither "." (equal), ".." (parent is below child), nor a "../"-prefixed path
// (child escapes upward). This is what makes /data/foo2 NOT a child of
// /data/foo — filepath.Rel yields "../foo2", which is rejected.
func isPathAncestor(parent, child string) bool {
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

// rejectSymlinkedDataRoot fails if the path, or its deepest already-existing
// ancestor, traverses a symlink. The canonical (filepath.Clean) check cannot
// see symlinks — a canonical path like /mnt/disk-a/tenant can still be a
// symlink to somewhere else — so this resolves the deepest existing prefix of
// the path and requires it to equal itself. A path whose leaf does not yet
// exist is fine: its existing ancestor is what gets checked, and the leaf will
// be created (not followed) by the caller.
func rejectSymlinkedDataRoot(path string) error {
	probe := path
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return &InvalidDataRootError{DataRoot: path, Reason: "stat failed: " + err.Error()}
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
		return &InvalidDataRootError{DataRoot: path, Reason: "resolve symlinks failed: " + err.Error()}
	}
	if resolved != probe {
		return &InvalidDataRootError{DataRoot: path, Reason: "path traverses a symlink (" + probe + " resolves to " + resolved + ")"}
	}
	return nil
}
