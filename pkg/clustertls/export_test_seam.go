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

package clustertls

// SetSyncDirForTest replaces the directory-fsync step of WriteFileAtomic and
// returns a function restoring the original.
//
// It exists because the durability of a write is not observable from its
// post-state -- the bytes on disk are identical whether or not the containing
// directory was synced -- so a caller in another package can only assert the
// property by failing the operation at its boundary. pkg/clusterenroll needs
// exactly that to prove persistRole does not report success before the role's
// directory entry is durable (go-qrdb#241).
//
// It is not a general seam: it is unsafe for concurrent use and is restore-only
// via the returned function.
func SetSyncDirForTest(fn func(string) error) func() {
	prev := syncDirFn
	syncDirFn = fn
	return func() { syncDirFn = prev }
}
