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

package rowlock

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestReleaseWith_FoldsTheReleaseIntoTheCallersProposal(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	sess := NewSession()
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	o := sess.NewOwner()
	require.NoError(t, o.Acquire(context.Background(), tbl, []string{keyA, keyB}, ModeExclusive, 0))
	assert.Equal(t, "acme/shop", o.Scope())

	var proposed []service.Op
	require.NoError(t, o.ReleaseWith(context.Background(), func(release []service.Op) (bool, error) {
		proposed = release
		_, err := s.deps.Txn.Execute(context.Background(), nil, release, release)
		return true, err
	}))
	assert.Len(t, proposed, 2, "one delete per record the owner holds")
	assert.Empty(t, queueOwners(t, tbl, keyA))
	assert.Empty(t, queueOwners(t, tbl, keyB))
	assert.False(t, o.Active())
	assert.True(t, leaseAlive(t, s, o.lease.id), "the session keeps its lease")
}

func TestReleaseWith_NotFoldedReleasesOnItsOwn(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	sess := NewSession()
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	o := sess.NewOwner()
	require.NoError(t, o.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))

	require.NoError(t, o.ReleaseWith(context.Background(), func([]service.Op) (bool, error) { return false, nil }))
	assert.Empty(t, queueOwners(t, tbl, keyA))
	assert.False(t, o.Active())
}

func TestReleaseWith_FailedProposalKeepsTheLocks(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	sess := NewSession()
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	o := sess.NewOwner()
	require.NoError(t, o.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))

	boom := &injectedError{op: "commit"}
	require.ErrorIs(t, o.ReleaseWith(context.Background(), func([]service.Op) (bool, error) { return false, boom }), boom)
	assert.True(t, o.Holds(keyA, ModeExclusive), "an unknown outcome leaves the locks for a retry")
	assert.Equal(t, []string{o.ID() + "/x"}, queueOwners(t, tbl, keyA))
	require.NoError(t, o.Release(context.Background()))
}

func TestReleaseWith_NothingToFold(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	sess := NewSession()
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	idle := sess.NewOwner()
	assert.Empty(t, idle.Scope())
	called := false
	require.NoError(t, idle.ReleaseWith(context.Background(), func(release []service.Op) (bool, error) {
		called = true
		assert.Nil(t, release, "an owner without locks has nothing to fold")
		return false, nil
	}))
	assert.True(t, called)

	lost := sess.NewOwner()
	require.NoError(t, lost.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, 0))
	require.NoError(t, sess.Close(context.Background()))
	require.NoError(t, lost.ReleaseWith(context.Background(), func(release []service.Op) (bool, error) {
		assert.Nil(t, release, "an ended lease took the records; there is nothing to delete")
		return false, nil
	}))
	assert.Empty(t, queueOwners(t, tbl, keyA))
}
