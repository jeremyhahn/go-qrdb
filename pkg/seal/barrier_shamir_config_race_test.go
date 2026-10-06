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

// barrier_shamir_config_race_test.go proves SetShamirConfig/SetShamirStrategy
// and every reader of config.Shamir (Status, the quorum TTL lookup,
// InitializeShamir) go through shamirMu rather than racing the field
// directly -- the exact shape ReconfigureTenantShamir (a tenant's barrier
// shell reconfigured to Shamir after `tenant create`) exercises against a
// concurrent Status() poll. Run under `go test -race`.
package seal

import (
	"sync"
	"testing"
)

// TestStorageBarrier_ShamirConfig_ConcurrentReadWrite races SetShamirConfig
// writes against Status/quorumTTL reads on the same barrier. `go test
// -race` must report nothing: before shamirMu existed, this reliably
// reported a DATA RACE (not just a logical one) on config.Shamir.
func TestStorageBarrier_ShamirConfig_ConcurrentReadWrite(t *testing.T) {
	backend := NewMemoryBackend()
	strategy := testSoftwareStrategy(t)
	b, err := NewStorageBarrier(testLogger(), backend, BarrierConfig{RootKeyPath: "race/root-key"}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			b.SetShamirConfig(&ShamirConfig{Threshold: 2, TotalShares: 3, QuorumTTL: 0})
			b.updateShamirConfig(2, 3)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = b.Status()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = b.quorumTTL()
		}
	}()

	for i := 0; i < 2000; i++ {
		_ = b.shamirConfig()
	}
	close(stop)
	wg.Wait()
}
