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

package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// rekeyStarts returns ring's holder rekey-start events.
func rekeyStarts(ring *audit.RingBuffer) []audit.AuditEvent {
	var out []audit.AuditEvent
	for _, ev := range ring.Snapshot() {
		if ev.Action == ceremony.ActionHolderRekeyStart {
			out = append(out, ev)
		}
	}
	return out
}

// TestBarrierService_RekeyStartsAreAudited pins that the system barrier's
// rekey and revocation verbs open their rounds through the holder authority,
// so both are on the trail: the rekey that opened, and the revocation
// refused because a round is already open.
func TestBarrierService_RekeyStartsAreAudited(t *testing.T) {
	ctx := adminCtx()
	e := newHolderSvcEnv(t, 2, 3)
	ring := audit.NewRingBuffer(64)
	e.core.SetAuditLogger(ring)
	e.register(seal.DomainUnseal, newSvcHolder(t, "alice"), newSvcHolder(t, "bob"), newSvcHolder(t, "carol"))
	// See TestBarrierService_HolderCustodyEndToEnd (holder_service_test.go):
	// a plain, non-ceremony BarrierService over the same barrier, since
	// e.svc's ceremony now refuses BarrierInitializeShamir (go-qrdb#560
	// review item 8). This test exercises the rekey/revocation audit
	// trail, not that bootstrap gate.
	_, err := NewBarrierService(e.barrier).BarrierInitializeShamir(ctx, 2, 3, "")
	require.NoError(t, err)
	require.NoError(t, e.pins.SetSOPIN("", svcSOPIN))

	_, err = e.svc.BarrierRekeyStart(ctx, seal.DomainUnseal, 2, seal.FactorPolicyAnyEnrolled, svcSOPIN)
	require.NoError(t, err)
	_, err = e.svc.BarrierOfficerRevoke(ctx, seal.DomainUnseal, "carol", 0, svcSOPIN)
	require.ErrorIs(t, err, seal.ErrQuorumRoundInProgress)

	starts := rekeyStarts(ring)
	require.Len(t, starts, 3, "the rekey permitted then opened, and the refused revocation")
	for _, ev := range starts {
		assert.Equal(t, tenantid.System, ev.TenantID)
		assert.Equal(t, "system", ev.Attributes["scope"])
		assert.Equal(t, "admin", ev.Actor)
	}
	assert.Equal(t, audit.OutcomePermitted, starts[0].Outcome)
	assert.Equal(t, audit.OutcomeSuccess, starts[1].Outcome)
	assert.Equal(t, string(seal.FactorPolicyAnyEnrolled), starts[1].Attributes["policy"])
	assert.True(t, strings.HasPrefix(starts[2].Outcome, audit.OutcomeFailure+":"), starts[2].Outcome)
	assert.Equal(t, "carol", starts[2].Attributes["holder"])
}

// TestDatabaseHolders_RekeyStartsAreAudited pins that the database service's
// rekey and revocation verbs open their rounds through the database's holder
// authority, keyed by the database's tenant.
func TestDatabaseHolders_RekeyStartsAreAudited(t *testing.T) {
	ring := audit.NewRingBuffer(64)
	svc, _ := newAuditedHolderDatabaseService(t, ring)
	ctx := dbAdminCtx("acme")
	for _, h := range []*dbSvcHolder{newDBSvcHolder(t, "alice"), newDBSvcHolder(t, "bob")} {
		res, err := svc.DatabaseBarrierOfficerRegister(ctx, "vault", &BarrierOfficerRegisterRequest{Username: h.name, CSRPEM: h.csr})
		require.NoError(t, err)
		_, err = mtls.ParseCertificatePEM(res.CertPEM)
		require.NoError(t, err)
	}
	_, err := svc.DatabaseBarrierShamirInitialize(ctx, "vault", "")
	require.NoError(t, err)

	_, err = svc.DatabaseBarrierRekeyStart(ctx, "vault", 2, "")
	require.NoError(t, err)
	var breaks *seal.RevocationBreaksQuorumError
	_, err = svc.DatabaseBarrierOfficerRevoke(ctx, "vault", "bob", 2)
	require.True(t, errors.As(err, &breaks), err)

	starts := rekeyStarts(ring)
	require.Len(t, starts, 3, "the rekey permitted then opened, and the refused revocation")
	for _, ev := range starts {
		assert.Equal(t, "acme", ev.TenantID, "a database's holder events belong to its tenant")
		assert.Equal(t, ceremony.ScopeDatabase("acme", "vault").String(), ev.Attributes["scope"])
		assert.Equal(t, "db-admin", ev.Actor)
	}
	assert.Equal(t, audit.OutcomePermitted, starts[0].Outcome)
	assert.Equal(t, audit.OutcomeSuccess, starts[1].Outcome)
	assert.True(t, strings.HasPrefix(starts[2].Outcome, audit.OutcomeFailure+":"), starts[2].Outcome)
	assert.Equal(t, "bob", starts[2].Attributes["holder"])
}

// TestDatabaseHolders_UnenrolBeforeInitialization pins the database service's
// unenrolment: confined to the caller's tenant like every holder verb, it
// removes a holder before the database's custody is initialized and refuses
// once it is.
func TestDatabaseHolders_UnenrolBeforeInitialization(t *testing.T) {
	ring := audit.NewRingBuffer(64)
	svc, _ := newAuditedHolderDatabaseService(t, ring)
	ctx := dbAdminCtx("acme")
	for _, h := range []*dbSvcHolder{newDBSvcHolder(t, "alice"), newDBSvcHolder(t, "bob")} {
		_, err := svc.DatabaseBarrierOfficerRegister(ctx, "vault", &BarrierOfficerRegisterRequest{Username: h.name, CSRPEM: h.csr})
		require.NoError(t, err)
	}

	var notFound *database.NotFoundError
	require.True(t, errors.As(svc.DatabaseBarrierOfficerUnenrol(dbAdminCtx("globex"), "vault", "bob"), &notFound),
		"another tenant's database is indistinguishable from a missing one")
	require.NoError(t, svc.DatabaseBarrierOfficerUnenrol(ctx, "vault", "bob"))
	list, err := svc.DatabaseBarrierOfficerList(ctx, "vault")
	require.NoError(t, err)
	require.Len(t, list, 1)

	carol := newDBSvcHolder(t, "carol")
	_, err = svc.DatabaseBarrierOfficerRegister(ctx, "vault", &BarrierOfficerRegisterRequest{Username: carol.name, CSRPEM: carol.csr})
	require.NoError(t, err)
	_, err = svc.DatabaseBarrierShamirInitialize(ctx, "vault", "")
	require.NoError(t, err)
	require.ErrorIs(t, svc.DatabaseBarrierOfficerUnenrol(ctx, "vault", "carol"), ceremony.ErrUnenrolInitialized)

	var unenrols []audit.AuditEvent
	for _, ev := range ring.Snapshot() {
		if ev.Action == ceremony.ActionHolderUnenrol {
			unenrols = append(unenrols, ev)
		}
	}
	require.Len(t, unenrols, 3, "the removal permitted then done, and the refusal; the other tenant was refused before the holder authority")
	assert.Equal(t, audit.OutcomePermitted, unenrols[0].Outcome)
	assert.Equal(t, audit.OutcomeSuccess, unenrols[1].Outcome)
	assert.True(t, strings.HasPrefix(unenrols[2].Outcome, audit.OutcomeFailure+":"), unenrols[2].Outcome)
	for _, ev := range unenrols {
		assert.Equal(t, "acme", ev.TenantID)
	}
}
