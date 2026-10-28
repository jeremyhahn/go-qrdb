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

package encfs

import (
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// newBaseFS returns an in-memory filesys.LockFS, Pebble's MemFS seen through
// the pkg/storage/pebble adapter, so tests stay off the host file system.
func newBaseFS() filesys.LockFS {
	return pebblestore.FromVFS(vfs.NewMem()).(filesys.LockFS)
}
