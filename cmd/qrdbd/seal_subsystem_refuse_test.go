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

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

func TestRefuseCommittedJournalWithoutStagingRoot_Allows(t *testing.T) {
	ctx := context.Background()
	const rootKeyPath = "system/root-key"
	const stagingPath = "system/root-key.staging"
	committed := func() *seal.MigrationJournal {
		return &seal.MigrationJournal{Phase: seal.PhaseCommitting, StagingRootKeyPath: stagingPath}
	}
	cases := map[string]struct {
		j     *seal.MigrationJournal
		setup func(t *testing.T, backend seal.StorageBackend)
	}{
		"nil journal":         {j: nil},
		"uncommitted journal": {j: &seal.MigrationJournal{StagingRootKeyPath: stagingPath}},
		"in-place commit":     {j: &seal.MigrationJournal{Phase: seal.PhaseCommitting}},
		"canonical root keyed": {j: committed(), setup: func(t *testing.T, backend seal.StorageBackend) {
			t.Helper()
			blob := []byte(`{"strategy":"` + string(seal.StrategyPassphrase) + `"}`)
			if err := backend.Put(ctx, rootKeyPath, blob); err != nil {
				t.Fatalf("Put root key: %v", err)
			}
		}},
		"staging root present": {j: committed(), setup: func(t *testing.T, backend seal.StorageBackend) {
			t.Helper()
			if err := backend.Put(ctx, stagingPath, []byte("sealed staging root")); err != nil {
				t.Fatalf("Put staging root: %v", err)
			}
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			backend := seal.NewMemoryBackend()
			if tc.setup != nil {
				tc.setup(t, backend)
			}
			if err := refuseCommittedJournalWithoutStagingRoot(ctx, backend, rootKeyPath, tc.j); err != nil {
				t.Fatalf("refuseCommittedJournalWithoutStagingRoot = %v, want nil", err)
			}
		})
	}
}
