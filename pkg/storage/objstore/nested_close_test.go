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

package objstore

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// waitForPendingClose spins until Close is waiting on lifeMu exclusive: a
// pending writer makes TryRLock fail.
func waitForPendingClose(e *Engine) {
	for {
		if !e.lifeMu.TryRLock() {
			return
		}
		e.lifeMu.RUnlock()
		runtime.Gosched()
	}
}

// A call admitted by enter never enters again. Were it to, a Close arriving
// between the outer admission and the nested one would wait for the outer
// read lock while the nested RLock waited behind Close, and neither would
// return. Each operation below runs with a hook that, on any nested enter,
// starts Close and waits until Close is blocked on lifeMu before letting the
// nested RLock run, so a nested enter deadlocks the test.
func TestNestedCallsNeverReenterAgainstClose(t *testing.T) {
	ops := map[string]func(e *Engine) error{
		"CompareAndSwap": func(e *Engine) error {
			_, err := e.CompareAndSwap(context.Background(), []byte("k"), []byte("v"), []byte("w"))
			return err
		},
		"DeleteRange": func(e *Engine) error {
			return e.DeleteRange([]byte("a"), []byte("z"))
		},
		"List": func(e *Engine) error {
			_, err := e.List(nil)
			return err
		},
		"NewIterator": func(e *Engine) error {
			it := e.NewIterator(nil)
			defer func() { _ = it.Close() }()
			for it.Next() {
			}
			return it.Error()
		},
		"Snapshot": func(e *Engine) error {
			s, err := e.Snapshot()
			if err != nil {
				return err
			}
			_, err = s.Get([]byte("k"))
			_ = s.Close()
			return err
		},
		"BatchDeleteRange": func(e *Engine) error {
			return e.commitBufferedOps([]storage.BufferedOp{{Key: []byte("a"), RangeEnd: []byte("z"), Range: true}})
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			e, cs := newClosingEngine(t)
			var enters atomic.Int32
			closed := make(chan error, 1)
			e.beforeEnter = func() {
				if enters.Add(1) == 1 {
					return
				}
				// A nested enter: Close now, and let the nested RLock run
				// only once Close is waiting on lifeMu.
				go func() { closed <- e.Close() }()
				waitForPendingClose(e)
			}

			done := make(chan error, 1)
			go func() { done <- op(e) }()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(30 * time.Second):
				t.Fatalf("%s deadlocked against Close: a nested call re-entered lifeMu", name)
			}
			assert.Equal(t, int32(1), enters.Load(), "%s entered once, never nested", name)

			e.beforeEnter = nil
			if enters.Load() == 1 {
				require.NoError(t, e.Close())
			} else {
				require.NoError(t, <-closed)
			}
			assert.False(t, cs.usedAfterFree.Load(), "no call used the released provider")
		})
	}
}
