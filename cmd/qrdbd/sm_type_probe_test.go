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

package main

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
)

// TestRequireGuardedBatch_PassesOnEveryRegisteredSMType is the daemon half of
// go-qrdb#136: the boot-time guarded-batch probe go-qrdb#555 added runs
// against every --sm the daemon can resolve — the REAL state machine behind
// the daemon's own guarded adapter, not a transaction double — and every one
// of them passes it, so no sm_type is refused at start.
func TestRequireGuardedBatch_PassesOnEveryRegisteredSMType(t *testing.T) {
	ctx := context.Background()
	for _, name := range statemachine.Names() {
		t.Run(name, func(t *testing.T) {
			desc, err := statemachine.Describe(name)
			if err != nil {
				t.Fatal(err)
			}
			sc := statemachine.StorageConfig{}
			if desc.StorageGeneric {
				sc = statemachine.StorageConfig{Root: t.TempDir(), Kind: "memory"}
			}
			createFn, err := resolveCreateFunc(name, "", sc)
			if err != nil {
				t.Fatalf("resolveCreateFunc: %v", err)
			}
			machine := createFn(1, 1)
			if d, ok := machine.(sm.DiskStateMachine); ok {
				if _, err := d.Open(ctx, t.TempDir(), nil); err != nil {
					t.Fatalf("Open: %v", err)
				}
			}
			adapterSM := machine.(backend.SMAdapter)
			t.Cleanup(func() { _ = adapterSM.Close(ctx) })

			host := backend.NewLocalBackend(adapterSM)
			store, err := newGuardedKVServiceAdapter(service.NewKVService(host, 0), service.NewTransactionService(host, 0), nil, nil)
			if err != nil {
				t.Fatalf("newGuardedKVServiceAdapter: %v", err)
			}
			if err := requireGuardedBatch(ctx, store.(kvstore.MultiGuardedBatchKVStore)); err != nil {
				t.Fatalf("--sm %s is refused at start: %v", name, err)
			}
		})
	}
}
