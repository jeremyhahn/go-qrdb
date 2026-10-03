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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestRegister_DatabaseRefusesPastNBeforeInitialization is the regression for
// the enrolment dead end: a database barrier's M-of-N is fixed at its create,
// and before initialization more than N holders could be enrolled -- two
// browser tabs, the wizard and the database view, or the CLI -- after which
// initialization refused the count and no holder could be revoked. The
// N+1th enrolment is refused, and initialization goes ahead with the N.
func TestRegister_DatabaseRefusesPastNBeforeInitialization(t *testing.T) {
	ring := audit.NewRingBuffer(64)
	a := auditScopes(t, ring)["database"](t)
	ctx := context.Background()
	a.enrol(t, ctx, newHolder(t, "alice", ""), newHolder(t, "bob", ""), newHolder(t, "carol", ""))

	_, err := a.register(ctx, newHolder(t, "dave", "").request("admin-1"))
	var limit *HolderLimitReachedError
	require.ErrorAs(t, err, &limit, "a fourth holder of a 2-of-3 database barrier is refused before initialization")
	assert.Equal(t, 3, limit.Total)

	require.NoError(t, a.distribute(ctx), "initialization proceeds with the N enrolled")

	var refused []audit.AuditEvent
	for _, ev := range eventsOf(scopeEvents(ring, a.scope), ActionHolderRegister) {
		if ev.Attributes["holder"] == "dave" {
			refused = append(refused, ev)
		}
	}
	require.Len(t, refused, 1)
	assert.True(t, strings.HasPrefix(refused[0].Outcome, audit.OutcomeFailure+":"), refused[0].Outcome)

	// Once initialized, a new holder enrols and joins at the next rekey.
	_, err = a.register(ctx, newHolder(t, "dave", "").request("admin-1"))
	require.NoError(t, err)
}

// TestRegister_ScopesThatChooseNAtInitializationAreNotLimited pins that only
// a scope whose N is fixed before initialization limits enrolment: the system
// barrier's and a tenant's initialization take the M-of-N with the request,
// so a fourth holder enrols and the operator initializes with the count
// enrolled.
func TestRegister_ScopesThatChooseNAtInitializationAreNotLimited(t *testing.T) {
	for _, name := range []string{"system", "tenant"} {
		t.Run(name, func(t *testing.T) {
			a := auditScopes(t, nil)[name](t)
			ctx := context.Background()
			a.enrol(t, ctx, newHolder(t, "alice", ""), newHolder(t, "bob", ""), newHolder(t, "carol", ""))
			_, err := a.register(ctx, newHolder(t, "dave", "").request("admin-1"))
			require.NoError(t, err)
			a.barrier.SetShamirConfig(&seal.ShamirConfig{Threshold: 2, TotalShares: 4})
			require.NoError(t, a.distribute(ctx))
		})
	}
}

// TestRegister_RecoveryDomainHasNoFixedN pins that recovery holders are not
// limited: a recovery key is split to every recovery holder enrolled.
func TestRegister_RecoveryDomainHasNoFixedN(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	e.initShamir("", newHolder(t, "so-1", ""), newHolder(t, "so-2", ""))
	e.register(seal.DomainRecovery, newHolder(t, "r1", ""), newHolder(t, "r2", ""), newHolder(t, "r3", ""))
	res, err := e.barrier.GenerateRecoveryKeys(context.Background(), 2, "")
	require.NoError(t, err)
	assert.Equal(t, 3, res.TotalShares)
}

// TestCapacityProbe covers the probe: only a database barrier with a Shamir
// configuration fixes N before initialization.
func TestCapacityProbe(t *testing.T) {
	shamir := shamirBarrier(t, seal.NewMemoryBackend(), 2, 5)
	db := ScopeDatabase("acme", "orders")
	assert.Equal(t, 5, capacityProbe(db, shamir)())
	assert.Zero(t, capacityProbe(ScopeSystem, shamir)(), "the system barrier's initialization chooses N")
	assert.Zero(t, capacityProbe(ScopeTenant("acme"), shamir)(), "a tenant's initialization chooses N")
	assert.Zero(t, capacityProbe(ScopeRecovery, shamir)(), "a recovery key is split to every recovery holder")
	assert.Zero(t, capacityProbe(db, testBarrier(t))(), "a barrier with no Shamir configuration fixes none")
	assert.Zero(t, capacityProbe(db, seal.NewMemoryBackend())(), "a backend that is not a barrier fixes none")
}
