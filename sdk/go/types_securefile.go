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

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/util/securefile"
)

// ---------------------------------------------------------------------------
// Atomic, mode-enforcing secure file writer (go-qrdb#580)
//
// go-xkms and quicbaas write files a user explicitly asked for -- an
// --output key, a share or cert export, an ACME cert deploy path, a
// pre-barrier sealed key block -- and consume go-qrdb exclusively through
// sdk/go (the ownership constitution), so pkg/util/securefile's writer is
// re-exported here rather than requiring a pkg/* import.
// ---------------------------------------------------------------------------

// WriteFileAtomic writes data to path by creating a uniquely named temporary
// file in path's directory, fsyncing it, chmoding it to perm, renaming it
// into place, and fsyncing the containing directory so the rename survives
// a crash. The temporary file is removed on every error path.
//
// A pre-existing symlink at path is refused with a typed *SymlinkRefusedError
// rather than replaced.
var WriteFileAtomic = securefile.WriteFileAtomic

// CreateExclusive writes data to path, refusing to create it if anything --
// a plain file, a directory, or a symlink of any kind, dangling or not --
// already occupies that name (a typed *FileExistsError). It is for outputs
// that must never silently overwrite an existing file.
var CreateExclusive = securefile.CreateExclusive

// SymlinkRefusedError is returned by WriteFileAtomic when its target path
// already exists and is a symlink.
type SymlinkRefusedError = securefile.SymlinkRefusedError

// FileExistsError is returned by CreateExclusive when its target path
// already exists.
type FileExistsError = securefile.FileExistsError
