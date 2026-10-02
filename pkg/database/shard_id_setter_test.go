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

package database

import (
	"errors"
	"strconv"
	"sync"
	"testing"
)

// SetShardIDClaimer with nil clears the claimer: a raft create then refuses as
// a Manager that never had one does.
func TestSetShardIDClaimer_NilClears(t *testing.T) {
	m := NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = m.Close() })
	m.SetShardIDClaimer(testShardClaimer(t))
	m.SetShardIDClaimer(nil)
	_, err := m.Create(DatabaseConfig{Name: "unissued", Engine: EngineKV, Backend: BackendMemory, Raft: validRaftConfig(0)})
	var required *ShardIssuerRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("raft create after clearing the claimer = %v, want *ShardIssuerRequiredError", err)
	}
}

// The claimer is set while creates run (a composition wiring a node that is
// already serving): each create sees no claimer or the one set, never a torn
// value. Run under -race.
func TestSetShardIDClaimer_ConcurrentWithCreates(t *testing.T) {
	m := NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = m.Close() })
	claimer := testShardClaimer(t)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Create(DatabaseConfig{
				Name: "racing-" + strconv.Itoa(i), Engine: EngineKV, Backend: BackendMemory, Raft: validRaftConfig(0),
			})
			var required *ShardIssuerRequiredError
			if err != nil && !errors.As(err, &required) {
				t.Errorf("create racing the claimer: %v", err)
			}
		}()
	}
	m.SetShardIDClaimer(claimer)
	wg.Wait()
}
