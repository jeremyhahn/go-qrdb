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

import (
	"log/slog"
	"os"
	"sync"
)

var noSyncIgnoredOnce sync.Once

// noSyncWAL reports whether the Raft WAL skips fdatasync. A production build
// always syncs it: QRDB_NOSYNC_WRITES is honoured only with the qrdb_testonly
// build tag, and setting it here logs one warning that it is ignored.
func noSyncWAL() bool {
	if _, set := os.LookupEnv("QRDB_NOSYNC_WRITES"); set {
		noSyncIgnoredOnce.Do(func() {
			slog.Default().Warn("node: QRDB_NOSYNC_WRITES is ignored outside test builds; the Raft WAL is synced")
		})
	}
	return false
}
