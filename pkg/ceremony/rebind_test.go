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

package ceremony

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestRebind_CarriesHoldersAndEnvelopesToANewBarrier proves an in-process
// data-plane restart can keep the ceremony state that makes a Shamir barrier
// openable. A new barrier instance over the same storage is sealed; rebinding
// the running ceremony service to it keeps the enrolled holders and the
// envelopes not yet claimed, so the holders claim from the new barrier and a
// quorum of them unseals it.
func TestRebind_CarriesHoldersAndEnvelopesToANewBarrier(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 3)
	holders := []*testHolder{
		newHolder(t, "alice", seal.RoleSecurityOfficer),
		newHolder(t, "bob", seal.RoleCustodian),
		newHolder(t, "carol", seal.RoleCustodian),
	}
	e.register(seal.DomainUnseal, holders...)
	_, err := e.barrier.InitializeShamir(ctx, "")
	require.NoError(t, err)

	restarted := shamirBarrier(t, e.backend, 2, 3)
	require.True(t, restarted.IsSealed())
	e.svc.Rebind(restarted)
	e.barrier = restarted

	for _, h := range holders {
		e.claim(seal.DomainUnseal, h)
	}
	var out *seal.QuorumOutcome
	for _, h := range holders[:2] {
		out, err = e.submit(e.svc, restarted, seal.DomainUnseal, seal.OpUnseal, h, "")
		require.NoError(t, err, h.username)
	}
	require.True(t, out.Complete)
	require.False(t, restarted.IsSealed())

	// Once unsealed, the holder records the service carried are persisted
	// through the new barrier and read back.
	list, err := e.svc.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, err)
	require.Len(t, list, 3)
}

// TestRebind_SameBarrierIsANoOp proves rebinding to the barrier the service
// already serves changes nothing.
func TestRebind_SameBarrierIsANoOp(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	holders := []*testHolder{newHolder(t, "alice", seal.RoleSecurityOfficer), newHolder(t, "bob", seal.RoleCustodian)}
	e.register(seal.DomainUnseal, holders...)
	e.svc.Rebind(e.barrier)
	_, err := e.barrier.InitializeShamir(context.Background(), "")
	require.NoError(t, err)
	e.claim(seal.DomainUnseal, holders[0])
	require.Same(t, e.barrier, e.svc.Barrier())
}
