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

package objstore

import (
	"runtime"
	"testing"
)

// TestCacheDirSyncSupported_FollowsPlatform pins that the cache syncs its
// directory handle everywhere but Windows, where FlushFileBuffers fails on
// one and every cache publication would fail with it.
func TestCacheDirSyncSupported_FollowsPlatform(t *testing.T) {
	if want := runtime.GOOS != "windows"; cacheDirSyncSupported != want {
		t.Errorf("cacheDirSyncSupported = %v on %s, want %v", cacheDirSyncSupported, runtime.GOOS, want)
	}
}
