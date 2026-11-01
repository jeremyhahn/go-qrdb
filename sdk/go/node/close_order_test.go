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

package node

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// closeProbeStorage is a data-plane store that records, when it is closed,
// whether the barrier keying its encrypting file system had already sealed.
type closeProbeStorage struct {
	storage.Storage
	fs            interface{ BarrierSealed() bool }
	closeErr      error
	closes        int
	sealedAtClose bool
}

func (s *closeProbeStorage) Close() error {
	s.closes++
	s.sealedAtClose = s.fs.BarrierSealed()
	if err := s.Storage.Close(); err != nil {
		return err
	}
	return s.closeErr
}

var _ storage.Storage = (*closeProbeStorage)(nil)

// newCloseProbeNode boots an unsealed, encrypted-at-rest standalone node
// whose data-plane store is a closeProbeStorage.
func newCloseProbeNode(t *testing.T, closeErr error) (*Node, *closeProbeStorage) {
	t.Helper()
	cfg := sealedStandaloneConfig(t, t.TempDir())
	cfg.Seal.Strategy = "none"
	var probe *closeProbeStorage
	factory := func(_ string, fs filesys.FS) (storage.Storage, error) {
		sealedFS, ok := fs.(interface{ BarrierSealed() bool })
		if !ok {
			return nil, errors.New("the data plane was not given the barrier's encrypting file system")
		}
		probe = &closeProbeStorage{Storage: memory.NewStorage(), fs: sealedFS, closeErr: closeErr}
		return probe, nil
	}
	n, err := NewStandaloneNode(context.Background(), cfg, nil, WithStorageFactory(factory))
	require.NoError(t, err)
	require.False(t, n.IsSealed())
	require.NotNil(t, probe)
	require.False(t, probe.fs.BarrierSealed())
	return n, probe
}

func TestNode_CloseClosesTheDataPlaneBeforeSealingItsBarrier(t *testing.T) {
	n, probe := newCloseProbeNode(t, nil)
	barrier := n.barrier.Load()
	require.NotNil(t, barrier)

	require.NoError(t, n.Close())
	require.Equal(t, 1, probe.closes, "Close must close the data plane exactly once")
	require.False(t, probe.sealedAtClose, "the data plane closed after its barrier sealed")
	require.True(t, barrier.IsSealed(), "Close must still seal the barrier")
	require.True(t, probe.fs.BarrierSealed())
	require.Nil(t, n.stg)
	require.Nil(t, n.pebbleDB)
}

func TestNode_CloseReportsADataPlaneCloseErrorAndStillSeals(t *testing.T) {
	boom := errors.New("data plane close failed")
	n, probe := newCloseProbeNode(t, boom)
	barrier := n.barrier.Load()

	require.ErrorIs(t, n.Close(), boom)
	require.Equal(t, 1, probe.closes)
	require.False(t, probe.sealedAtClose)
	require.True(t, barrier.IsSealed(), "a failed store close must not leave the barrier unsealed")
	require.Nil(t, n.stg)
	require.NoError(t, n.Close(), "a second Close is a no-op")
	require.Equal(t, 1, probe.closes, "a second Close must not close the data plane again")
}
