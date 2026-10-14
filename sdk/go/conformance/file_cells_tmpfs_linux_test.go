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

package conformance

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/testutil/memdir"
)

// TestFileCells_RunOnTmpfs: every directory a file-engine cell writes to is
// on tmpfs, so the cell's deadlines measure the protocol and not the runner's
// disk (see cell.dataDir).
func TestFileCells_RunOnTmpfs(t *testing.T) {
	checked := 0
	for _, c := range append(matrix(), coordinationCells()...) {
		if c.backend != database.BackendFile {
			continue
		}
		cfg := c.config(t)
		dirs := []string{cfg.Raft.DataDir}
		for _, pool := range cfg.Storage.VolumePools {
			dirs = append(dirs, pool.Path)
		}
		for _, dir := range dirs {
			mem, err := memdir.IsMemory(dir)
			if err != nil {
				t.Fatalf("%s/%s/%s: IsMemory(%s): %v", c.backend, c.mode, c.transport, dir, err)
			}
			if !mem {
				t.Fatalf("%s/%s/%s writes to %s, which is not tmpfs", c.backend, c.mode, c.transport, dir)
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no file-engine cell in the matrix: this check covers nothing")
	}
}
