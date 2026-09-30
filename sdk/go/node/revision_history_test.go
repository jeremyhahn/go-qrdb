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

package node

import (
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// The clustered node's default state machine is the MVCC store, which keeps
// revision history; an alternate type is judged by its descriptor.
func TestClusteredKeepsRevisionHistory(t *testing.T) {
	for smType, want := range map[string]bool{"": true, "pebble": true, "mvcc": true, "memory": false, "no-such-type": false} {
		cfg := &qrdbconfig.Config{}
		cfg.Raft.SMType = smType
		if got := clusteredKeepsRevisionHistory(cfg); got != want {
			t.Errorf("clusteredKeepsRevisionHistory(%q) = %v, want %v", smType, got, want)
		}
	}
}
