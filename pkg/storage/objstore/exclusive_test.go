package objstore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// TestExclusive_SettlesThePendingJournal runs Exclusive after a batch whose
// apply failed once its journal was published: fn must read the batch's
// value, not the pre-batch one the journal is about to replace.
func TestExclusive_SettlesThePendingJournal(t *testing.T) {
	e, fs := failCommitApply(t)

	var x storage.Exclusive = e
	var seen string
	require.NoError(t, x.Exclusive(func(rw storage.ReadWriter) error {
		got, err := rw.Get([]byte("k"))
		if err != nil {
			return err
		}
		seen = string(got)
		if err := rw.Set([]byte("k2"), []byte("v2")); err != nil {
			return err
		}
		return rw.Delete([]byte("k"))
	}))
	assert.Equal(t, "batch", seen)

	_, err := fs.Storage.Get(journalName)
	require.True(t, notFound(err), "the pending journal must be resolved and cleared, got %v", err)
	got, err := e.Get([]byte("k"))
	require.NoError(t, err)
	assert.Nil(t, got)
	got, err = e.Get([]byte("k2"))
	require.NoError(t, err)
	assert.Equal(t, "v2", string(got))
}

// TestExclusive_RefusedAfterCloseAndReclaim: Exclusive admits no call on a
// closed engine, nor a write once Reclaim has started.
func TestExclusive_RefusedAfterCloseAndReclaim(t *testing.T) {
	noop := func(storage.ReadWriter) error { return nil }

	reclaimed, _, _ := newReclaimEngine(t)
	require.NoError(t, reclaimed.Reclaim())
	var closedErr *storage.ClosedError
	require.ErrorAs(t, reclaimed.Exclusive(noop), &closedErr)

	closed, _ := newFaultEngine(t)
	require.NoError(t, closed.Close())
	require.Error(t, closed.Exclusive(noop))
}
