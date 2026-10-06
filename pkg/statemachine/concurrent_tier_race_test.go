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

package statemachine

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// concurrentTierSM is the surface go-quicraft drives on its concurrent and
// on-disk tiers, which since v0.16 run Lookup and GetHash concurrently with
// Update.
type concurrentTierSM interface {
	Update(ctx context.Context, entries []sm.Entry, results []sm.Result) error
	Lookup(ctx context.Context, query interface{}) (interface{}, error)
	GetHash() (uint64, error)
}

// TestConcurrentTiers_ReadAndHashDuringUpdate drives every concurrent- and
// on-disk-tier state machine qrdb registers the way go-quicraft v0.16 does:
// batches applied on one goroutine while others look keys up and hash the
// state. Under -race any unsynchronised access fails it; every write must
// also be applied, and the final hash must be stable.
func TestConcurrentTiers_ReadAndHashDuringUpdate(t *testing.T) {
	targets := map[string]func() concurrentTierSM{
		"ConcurrentAdapter": func() concurrentTierSM {
			return &ConcurrentAdapter{KVStateMachine: NewKVStateMachine(NewConcurrentBackend())}
		},
		"HighPerfAdapter": func() concurrentTierSM {
			backend := NewHighPerfBackend(&HighPerfConfig{ShardCount: 16})
			return &HighPerfAdapter{KVStateMachine: NewKVStateMachine(backend), backend: backend}
		},
		"DiskAdapter": func() concurrentTierSM { return newDiskAdapter(newMemStorage()) },
		"ConcurrentStateMachine": func() concurrentTierSM {
			csm, _ := newTestConcurrentSM()
			return concurrentSMAdapter{csm}
		},
	}
	for name, build := range targets {
		t.Run(name, func(t *testing.T) {
			target := build()
			ctx := context.Background()
			const batches, perBatch = 50, 8

			var readers sync.WaitGroup
			stop := make(chan struct{})
			for r := 0; r < 4; r++ {
				readers.Add(1)
				go func() {
					defer readers.Done()
					for {
						select {
						case <-stop:
							return
						default:
						}
						if _, err := target.Lookup(ctx, []byte("key/0/0")); err != nil {
							t.Errorf("Lookup: %v", err)
							return
						}
						if _, err := target.GetHash(); err != nil {
							t.Errorf("GetHash: %v", err)
							return
						}
					}
				}()
			}

			index := uint64(0)
			for b := 0; b < batches; b++ {
				entries := make([]sm.Entry, perBatch)
				for i := range entries {
					index++
					entries[i] = putEntry(index, fmt.Sprintf("key/%d/%d", b, i), "v")
				}
				if err := target.Update(ctx, entries, make([]sm.Result, len(entries))); err != nil {
					close(stop)
					readers.Wait()
					t.Fatalf("Update batch %d: %v", b, err)
				}
			}
			close(stop)
			readers.Wait()

			h1, err := target.GetHash()
			if err != nil {
				t.Fatalf("GetHash: %v", err)
			}
			h2, _ := target.GetHash()
			if h1 != h2 {
				t.Fatalf("GetHash of a settled state moved: %d then %d", h1, h2)
			}
			got, err := target.Lookup(ctx, []byte(fmt.Sprintf("key/%d/%d", batches-1, perBatch-1)))
			if err != nil || got == nil {
				t.Fatalf("Lookup of the last write = %v, %v", got, err)
			}
		})
	}
}

// concurrentSMAdapter presents a ConcurrentStateMachine as concurrentTierSM.
type concurrentSMAdapter struct{ *ConcurrentStateMachine }

func (c concurrentSMAdapter) Lookup(ctx context.Context, query interface{}) (interface{}, error) {
	return c.ConcurrentStateMachine.Lookup(ctx, query)
}
