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

import "github.com/jeremyhahn/go-qrdb/pkg/validation"

// dataRootsOverlap reports whether two resolved tenant-directory paths
// refer to the same directory or one is a filesystem ancestor of the
// other. Both are cleaned first. The comparison is separator-aware so
// /data/foo and /data/foo2 do NOT overlap: a naive strings.HasPrefix(b, a)
// would wrongly treat /data/foo2 as nested under /data/foo. Used by the
// deleted-tenant reconcile's residue guard.
func dataRootsOverlap(a, b string) bool { return validation.PathsOverlap(a, b) }

// isPathAncestor reports whether parent is a strict filesystem ancestor of
// child. See validation.IsPathAncestor for the component-aware reasoning that
// makes /data/foo2 NOT a child of /data/foo.
func isPathAncestor(parent, child string) bool { return validation.IsPathAncestor(parent, child) }
