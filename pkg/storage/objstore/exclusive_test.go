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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// requireUnlocked fails t unless both of Exclusive's locks are free.
func requireUnlocked(t *testing.T, e *Engine) {
	t.Helper()
	require.True(t, e.journalMu.TryLock(), "journalMu still held")
	e.journalMu.Unlock()
	require.True(t, e.writeMu.TryLock(), "writeMu still held")
	e.writeMu.Unlock()
}

func TestExclusive(t *testing.T) {
	t.Run("closed engine is refused", func(t *testing.T) {
		e, _ := newFaultEngine(t)
		require.NoError(t, e.Close())
		called := false
		err := e.Exclusive(func(storage.ReadWriter) error { called = true; return nil })
		var closed *storage.ClosedError
		require.ErrorAs(t, err, &closed)
		assert.False(t, called)
	})
	t.Run("view reads and writes without the lock", func(t *testing.T) {
		e, _ := newFaultEngine(t)
		require.NoError(t, e.Set([]byte("gone"), []byte("v")))
		require.NoError(t, e.Exclusive(func(rw storage.ReadWriter) error {
			if err := rw.Set([]byte("k"), []byte("v")); err != nil {
				return err
			}
			got, err := rw.Get([]byte("k"))
			if err != nil {
				return err
			}
			assert.Equal(t, "v", string(got))
			assert.ErrorIs(t, rw.Set(nil, []byte("v")), storage.ErrEmptyKey)
			assert.ErrorIs(t, rw.Delete(nil), storage.ErrEmptyKey)
			return rw.Delete([]byte("gone"))
		}))
		got, err := e.Get([]byte("gone"))
		require.NoError(t, err)
		assert.Nil(t, got)
		requireUnlocked(t, e)
	})
	t.Run("fn error is returned", func(t *testing.T) {
		e, _ := newFaultEngine(t)
		boom := errors.New("boom")
		assert.ErrorIs(t, e.Exclusive(func(storage.ReadWriter) error { return boom }), boom)
		requireUnlocked(t, e)
	})
	t.Run("locks are released after a panic", func(t *testing.T) {
		e, _ := newFaultEngine(t)
		assert.Panics(t, func() {
			_ = e.Exclusive(func(storage.ReadWriter) error { panic("boom") })
		})
		requireUnlocked(t, e)
	})
	t.Run("unresolvable journal refuses fn", func(t *testing.T) {
		e, fs := failCommitApply(t)
		e.store = &putFailStore{faultStore: fs, armed: true}
		called := false
		err := e.Exclusive(func(storage.ReadWriter) error { called = true; return nil })
		require.ErrorIs(t, err, errProvider)
		assert.False(t, called)
		requireUnlocked(t, e)
	})
}
