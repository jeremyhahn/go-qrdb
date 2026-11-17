// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !qrdb_testonly

package node

import "testing"

// TestNoSyncWAL_IgnoredInProductionBuild: outside test builds the Raft WAL
// is always synced, whatever QRDB_NOSYNC_WRITES says (#1618).
func TestNoSyncWAL_IgnoredInProductionBuild(t *testing.T) {
	t.Setenv("QRDB_NOSYNC_WRITES", "1")
	if noSyncWAL() {
		t.Fatal("noSyncWAL() = true in a production build with QRDB_NOSYNC_WRITES=1, want false")
	}
}
