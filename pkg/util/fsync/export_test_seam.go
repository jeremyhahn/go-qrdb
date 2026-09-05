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

package fsync

// SetForTest replaces the directory-fsync step of Dir and Rename and returns a
// function restoring the original.
//
// It exists because the durability of a rename is not observable from its
// post-state -- the names on disk are identical whether or not the containing
// directory was synced -- so a caller in another package can only assert the
// property by failing the operation at its boundary. Every caller fixed by
// go-qrdb#268 needs exactly that: an ordinary test cannot tell a durable
// commit point from one that merely looks committed until the power drops.
//
// It is not a general seam: it is unsafe for concurrent use and is restore-only
// via the returned function. It lives in a non-test file because the packages
// that must assert the property are not this one.
//
// A replacement must NOT call Dir or Rename: both route through the function
// being replaced, so a replacement that calls through recurses until the stack
// overflows. A replacement that wants the real sync as well has no way to ask
// for it, deliberately -- the unseamed path is exercised by this package's own
// tests, and a caller asserting durability wants the sync FAILED, not counted.
func SetForTest(fn func(string) error) func() {
	prev := syncDirFn
	syncDirFn = fn
	return func() { syncDirFn = prev }
}
