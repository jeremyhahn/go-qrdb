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
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// sealedJournal wraps a record body in the journal magic and a valid CRC, so a
// test reaches the record parser rather than the header and checksum checks.
func sealedJournal(body []byte) []byte {
	image := append([]byte(journalMagic), body...)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc32.Checksum(image, castagnoli))
	return append(image, sum[:]...)
}

func be32(n int) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(n))
	return b[:]
}

func TestDecodeJournal_MalformedRecords(t *testing.T) {
	key := append(be32(1), 'k')
	cases := map[string]struct {
		image  []byte
		reason string
	}{
		"short header":     {[]byte("x"), "missing or unknown journal header"},
		"bad checksum":     {append(append([]byte(journalMagic), journalOpDelete), 0, 0, 0, 0, 0), "checksum mismatch"},
		"truncated length": {sealedJournal([]byte{journalOpSet}), "truncated key length"},
		"truncated key":    {sealedJournal(append([]byte{journalOpSet}, append(be32(9), 'k')...)), "truncated key"},
		"no value length":  {sealedJournal(append([]byte{journalOpSet}, key...)), "truncated value length"},
		"truncated value":  {sealedJournal(append(append([]byte{journalOpSet}, key...), append(be32(9), 'v')...)), "truncated value"},
		"unknown tag":      {sealedJournal(append([]byte{7}, key...)), "unknown record tag"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeJournal(tc.image)
			var corrupt *JournalCorruptError
			require.ErrorAs(t, err, &corrupt)
			assert.Equal(t, tc.reason, corrupt.Reason)
			assert.Contains(t, err.Error(), tc.reason)
		})
	}
}

func TestJournalWriteError_ErrorAndUnwrap(t *testing.T) {
	err := &JournalWriteError{Err: errProvider}
	assert.Contains(t, err.Error(), errProvider.Error())
	assert.ErrorIs(t, err, errProvider)
}

func TestCommitBufferedOps_JournalFaults(t *testing.T) {
	ops := []storage.BufferedOp{{Key: []byte("k"), Value: []byte("v")}}

	t.Run("journal write fails", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		pf := &putFailStore{faultStore: fs, armed: true}
		e.journal = pf
		var werr *JournalWriteError
		require.ErrorAs(t, e.commitBufferedOps(ops), &werr)
		assert.ErrorIs(t, werr, errProvider)
	})
	t.Run("journal clear fails", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		fs.deleteFails = true
		var werr *JournalWriteError
		require.ErrorAs(t, e.commitBufferedOps(ops), &werr)
	})
	t.Run("apply fails and the journal stays", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		pf := &putFailStore{faultStore: fs}
		e.journal = fs
		e.store = pf
		pf.armed = true
		require.ErrorIs(t, e.commitBufferedOps(ops), errProvider)
		_, err := fs.Storage.Get(journalName)
		assert.NoError(t, err, "the journal must survive a failed apply for recovery")
	})
	t.Run("empty key refused", func(t *testing.T) {
		e, _ := newFaultEngine(t)
		err := e.commitBufferedOps([]storage.BufferedOp{{Key: nil}})
		assert.True(t, errors.Is(err, storage.ErrEmptyKey))
	})
}

// failCommitApply commits a batch setting key "k" to "batch" whose apply
// fails after the journal is published, as a provider outage would, then
// lets the provider recover. The journal is left pending with "k" still at
// "old".
func failCommitApply(t *testing.T) (*Engine, *faultStore) {
	t.Helper()
	e, fs := newFaultEngine(t)
	require.NoError(t, e.Set([]byte("k"), []byte("old")))
	pf := &putFailStore{faultStore: fs, armed: true}
	e.store = pf
	require.ErrorIs(t, e.commitBufferedOps([]storage.BufferedOp{{Key: []byte("k"), Value: []byte("batch")}}), errProvider)
	pf.armed = false
	got, err := e.Get([]byte("k"))
	require.NoError(t, err)
	require.Equal(t, "old", string(got), "the failed apply must leave k unwritten")
	return e, fs
}

// TestPendingJournal_ResolvedBeforeTheNextWrite pins that a commit left
// journaled by a failed apply is completed before any later write is
// accepted: a swap must not be decided against the half-applied state, and no
// write may land under a journal that a later replay would apply over it.
func TestPendingJournal_ResolvedBeforeTheNextWrite(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		write func(e *Engine) error
		want  string
	}{
		"swap against the pre-batch value is refused": {
			write: func(e *Engine) error {
				swapped, err := e.CompareAndSwap(ctx, []byte("k"), []byte("old"), []byte("swapped"))
				if err == nil && swapped {
					return errors.New("swap accepted against the half-applied state")
				}
				return err
			},
			want: "batch",
		},
		"swap against the committed value succeeds": {
			write: func(e *Engine) error {
				swapped, err := e.CompareAndSwap(ctx, []byte("k"), []byte("batch"), []byte("swapped"))
				if err == nil && !swapped {
					return errors.New("swap refused against the committed value")
				}
				return err
			},
			want: "swapped",
		},
		"set": {
			write: func(e *Engine) error { return e.Set([]byte("k"), []byte("set")) },
			want:  "set",
		},
		"delete": {
			write: func(e *Engine) error { return e.Delete([]byte("k")) },
		},
		"next batch": {
			write: func(e *Engine) error {
				return e.commitBufferedOps([]storage.BufferedOp{{Key: []byte("other"), Value: []byte("v")}})
			},
			want: "batch",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e, fs := failCommitApply(t)
			require.NoError(t, tc.write(e))

			_, err := fs.Storage.Get(journalName)
			require.True(t, notFound(err), "the pending journal must be resolved and cleared, got %v", err)
			// Replaying at reopen must not change what the write left.
			require.NoError(t, e.recoverJournal())
			got, err := e.Get([]byte("k"))
			require.NoError(t, err)
			if tc.want == "" {
				assert.Nil(t, got)
				return
			}
			assert.Equal(t, tc.want, string(got))
		})
	}
}

// journalGateStore pauses the batch commit that publishes the journal, right
// after the publish, until proceed is closed.
type journalGateStore struct {
	*faultStore
	once      sync.Once
	published chan struct{}
	proceed   chan struct{}
}

func (g *journalGateStore) Put(name string, data io.Reader) error {
	if err := g.faultStore.Put(name, data); err != nil {
		return err
	}
	if name == journalName {
		g.once.Do(func() {
			close(g.published)
			<-g.proceed
		})
	}
	return nil
}

// TestWrite_ExcludedWhileABatchCommits pins the window between a Set or
// Delete's check for a pending commit and its write: a batch commit failing
// inside it left a journal the writer never resolved, and the next write's
// replay applied the batch over the acknowledged write. A commit therefore
// excludes every writer from its journal publish to its failure, and the
// writer let in afterwards resolves the commit before it writes.
func TestWrite_ExcludedWhileABatchCommits(t *testing.T) {
	cases := map[string]struct {
		write func(e *Engine) error
		want  string
	}{
		"set": {
			write: func(e *Engine) error { return e.Set([]byte("k"), []byte("set")) },
			want:  "set",
		},
		"delete": {
			write: func(e *Engine) error { return e.Delete([]byte("k")) },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e, fs := newFaultEngine(t)
			require.NoError(t, e.Set([]byte("k"), []byte("old")))
			gate := &journalGateStore{faultStore: fs, published: make(chan struct{}), proceed: make(chan struct{})}
			e.journal = gate
			pf := &putFailStore{faultStore: fs, armed: true}
			e.store = pf

			committed := make(chan error, 1)
			go func() {
				committed <- e.commitBufferedOps([]storage.BufferedOp{{Key: []byte("k"), Value: []byte("batch")}})
			}()
			<-gate.published
			// The commit has published its journal and not yet failed. A
			// writer let in now checks before the failure and writes after it.
			entered := e.writeMu.TryRLock()
			if entered {
				e.writeMu.RUnlock()
			}
			close(gate.proceed)
			require.ErrorIs(t, <-committed, errProvider)
			require.False(t, entered, "a writer entered while a batch commit had its journal in flight")

			pf.armed = false
			require.NoError(t, tc.write(e))
			// The write resolved the commit first, so a replay has nothing
			// left to apply over it.
			_, err := fs.Storage.Get(journalName)
			require.True(t, notFound(err), "the pending journal must be resolved before the write, got %v", err)
			require.NoError(t, e.Set([]byte("other"), []byte("v")))
			got, err := e.Get([]byte("k"))
			require.NoError(t, err)
			if tc.want == "" {
				assert.Nil(t, got)
				return
			}
			assert.Equal(t, tc.want, string(got))
		})
	}
}

// TestPendingJournal_UnresolvableRefusesTheWrite pins that a pending journal
// the provider still cannot apply fails the next write rather than letting it
// through.
func TestPendingJournal_UnresolvableRefusesTheWrite(t *testing.T) {
	ctx := context.Background()
	e, fs := failCommitApply(t)
	e.store = &putFailStore{faultStore: fs, armed: true}

	swapped, err := e.CompareAndSwap(ctx, []byte("k"), []byte("old"), []byte("swapped"))
	require.ErrorIs(t, err, errProvider)
	assert.False(t, swapped)
	require.ErrorIs(t, e.Delete([]byte("k")), errProvider)

	_, err = fs.Storage.Get(journalName)
	require.NoError(t, err, "an unresolved journal must stay for the next attempt")
}

func TestRecoverJournal_Faults(t *testing.T) {
	t.Run("journal read fails", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		fs.getFails[journalName] = true
		assert.ErrorIs(t, e.recoverJournal(), errProvider)
	})
	t.Run("journal body unreadable", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		fs.readFails[journalName] = true
		assert.ErrorIs(t, e.recoverJournal(), errProvider)
	})
	t.Run("journal replays and clears", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		require.NoError(t, fs.Storage.Put(journalName, bytes.NewReader(encodeJournal([]storage.BufferedOp{{Key: []byte("k"), Value: []byte("v")}}))))
		require.NoError(t, e.recoverJournal())
		got, err := e.Get([]byte("k"))
		require.NoError(t, err)
		assert.Equal(t, "v", string(got))
	})
	t.Run("replay apply fails", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		require.NoError(t, fs.Storage.Put(journalName, bytes.NewReader(encodeJournal([]storage.BufferedOp{{Key: []byte("k"), Value: []byte("v")}}))))
		pf := &putFailStore{faultStore: fs, armed: true}
		e.store = pf
		assert.ErrorIs(t, e.recoverJournal(), errProvider)
	})
}

func TestRefuseInTreeJournal(t *testing.T) {
	t.Run("get fault", func(t *testing.T) {
		_, fs := newFaultEngine(t)
		fs.getFails[journalName] = true
		assert.ErrorIs(t, refuseInTreeJournal("local", fs), errProvider)
	})
	t.Run("journal present", func(t *testing.T) {
		_, fs := newFaultEngine(t)
		require.NoError(t, fs.Storage.Put(journalName, bytes.NewReader([]byte("x"))))
		var inTree *InTreeJournalError
		require.ErrorAs(t, refuseInTreeJournal("local", fs), &inTree)
		assert.Contains(t, inTree.Error(), "local")
	})
	t.Run("journal absent", func(t *testing.T) {
		_, fs := newFaultEngine(t)
		assert.NoError(t, refuseInTreeJournal("local", fs))
	})
}

func TestJournalRoot_SiblingOfCleanedRoot(t *testing.T) {
	a, err := journalRoot("/data/db/.")
	require.NoError(t, err)
	b, err := journalRoot("/data/db/")
	require.NoError(t, err)
	assert.Equal(t, "/data/db.journal", a)
	assert.Equal(t, a, b)
}
