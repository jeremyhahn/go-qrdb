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
	"errors"
	"os"

	"github.com/jeremyhahn/go-qrdb/pkg/validation"
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
	// The absolute / canonical / no-symlink trio lives in pkg/validation so
	// this package and the volume pool cannot drift apart on it (#292). The
	// reason string is carried through into this package's own typed error:
	// callers switch on InvalidDataRootError, not on validation.PathError.
	if err := validation.AbsoluteCanonicalPath(path); err != nil {
		var pathErr *validation.PathError
		if errors.As(err, &pathErr) {
			return &InvalidDataRootError{DataRoot: path, Reason: pathErr.Reason}
		}
		return &InvalidDataRootError{DataRoot: path, Reason: err.Error()}
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
func dataRootsOverlap(a, b string) bool { return validation.PathsOverlap(a, b) }

// isPathAncestor reports whether parent is a strict filesystem ancestor of
// child. See validation.IsPathAncestor for the component-aware reasoning that
// makes /data/foo2 NOT a child of /data/foo.
func isPathAncestor(parent, child string) bool { return validation.IsPathAncestor(parent, child) }
