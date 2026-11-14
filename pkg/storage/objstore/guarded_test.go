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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// pendingBatchBackend returns a standalone database backend over an engine
// whose last batch journaled k=batch and n=batch and failed before applying
// either, over a committed k=old: the journal is pending and n has no
// applied write.
func pendingBatchBackend(t *testing.T) (*database.StandaloneBackend, *Engine) {
	t.Helper()
	e, fs := newFaultEngine(t)
	require.NoError(t, e.Set([]byte("k"), []byte("old")))
	pf := &putFailStore{faultStore: fs, armed: true}
	e.store = pf
	require.ErrorIs(t, e.commitBufferedOps([]storage.BufferedOp{
		{Key: []byte("k"), Value: []byte("batch")},
		{Key: []byte("n"), Value: []byte("batch")},
	}), errProvider)
	pf.armed = false
	return database.NewStandaloneBackend(e), e
}

// TestGuardedVerbs_ResolvePendingJournalFirst pins that a database's guarded
// verbs on an objstore engine compare against the state the pending journal
// commits, never the pre-batch state its failed apply left behind.
func TestGuardedVerbs_ResolvePendingJournalFirst(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		verb    func(b *database.StandaloneBackend) (bool, error)
		applied bool
		key     string
		want    string
	}{
		"swap against the pre-batch value is refused": {
			verb: func(b *database.StandaloneBackend) (bool, error) {
				return b.CompareAndSwap(ctx, []byte("k"), []byte("old"), []byte("swapped"))
			},
			key: "k", want: "batch",
		},
		"swap against the committed value succeeds": {
			verb: func(b *database.StandaloneBackend) (bool, error) {
				return b.CompareAndSwap(ctx, []byte("k"), []byte("batch"), []byte("swapped"))
			},
			applied: true, key: "k", want: "swapped",
		},
		"put-if-absent over a pending set is refused": {
			verb: func(b *database.StandaloneBackend) (bool, error) {
				return b.PutIfAbsent(ctx, []byte("n"), []byte("put"))
			},
			key: "n", want: "batch",
		},
		"delete against the pre-batch value is refused": {
			verb: func(b *database.StandaloneBackend) (bool, error) {
				return b.CompareAndDelete(ctx, []byte("k"), []byte("old"))
			},
			key: "k", want: "batch",
		},
		"delete against the committed value succeeds": {
			verb: func(b *database.StandaloneBackend) (bool, error) {
				return b.CompareAndDelete(ctx, []byte("k"), []byte("batch"))
			},
			applied: true, key: "k",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			b, e := pendingBatchBackend(t)
			applied, err := tc.verb(b)
			require.NoError(t, err)
			assert.Equal(t, tc.applied, applied)

			got, err := e.Get([]byte(tc.key))
			require.NoError(t, err)
			if tc.want == "" {
				assert.Nil(t, got)
				return
			}
			assert.Equal(t, tc.want, string(got))
		})
	}
}
