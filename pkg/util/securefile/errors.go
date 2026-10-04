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

package securefile

import "strconv"

// SymlinkRefusedError is returned when WriteFileAtomic's target path already
// exists and is a symlink. The writer refuses it rather than replacing
// whatever the symlink names.
type SymlinkRefusedError struct {
	Path string
}

// Error returns a human-readable description.
func (e *SymlinkRefusedError) Error() string {
	return "securefile: refusing to write through a symlink at " + strconv.Quote(e.Path)
}

// FileExistsError is returned when CreateExclusive's target path already
// exists -- a plain file, a directory, or a symlink of any kind.
type FileExistsError struct {
	Path string
}

// Error returns a human-readable description.
func (e *FileExistsError) Error() string {
	return "securefile: " + strconv.Quote(e.Path) + " already exists"
}
