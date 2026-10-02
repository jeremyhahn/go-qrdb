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

package ceremony

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
)

// An audit query is confined to the caller's tenant, so an event that names no
// tenant is a PLATFORM event that only the system tenant reads. Every event of
// a custodian's claim path names the custodian's tenant, so that tenant's
// auditors see their own custodians' claims and no one else's.

func requireEveryEventTenant(t *testing.T, events []eventView, tenantID string) {
	t.Helper()
	require.NotEmpty(t, events, "the flow emitted no audit event")
	for _, ev := range events {
		require.Equal(t, tenantID, ev.TenantID, "event %s/%s %q names tenant %q", ev.Resource, ev.Action, ev.Outcome, ev.TenantID)
	}
}

// eventView is the part of an audit event the tenant assertion reads.
type eventView struct{ TenantID, Resource, Action, Outcome string }

func eventsOf(l *fakeAuditLogger) []eventView {
	out := make([]eventView, len(l.events))
	for i, ev := range l.events {
		out[i] = eventView{ev.TenantID, ev.Resource, ev.Action, ev.Outcome}
	}
	return out
}

func TestCustodianClaimPath_EveryEventNamesTheCustodiansTenant(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1", TenantID: "acme", Method: custodian.FactorMethodPIV}
	auditLog := &fakeAuditLogger{}
	ext := &CeremonyExtension{CustodianSvc: custSvc, FactorVerifier: &fakeFactorVerifier{}, AuditLogger: auditLog, Threshold: 3}
	svc := newCeremonyServiceWithExt(t, ext)
	ctx := context.Background()

	_, err := svc.ClaimShareChallenge(ctx, "c1")
	require.NoError(t, err)
	_, err = svc.VerifyClaimAndDeliver(ctx, "c1", "ch", &authfactors.Response{})
	require.NoError(t, err)
	custSvc.custodians["c1"].ShareClaimed = true
	_, err = svc.VerifyClaimAndDeliver(ctx, "c1", "ch", &authfactors.Response{})
	require.ErrorIs(t, err, ErrShareAlreadyDelivered)
	_, err = svc.SubmitUnsealShare(ctx, SubmitUnsealShareRequest{CustodianID: "c1", ShareBytes: []byte("share-one"), FactorResponse: &authfactors.Response{}})
	require.NoError(t, err)

	requireEveryEventTenant(t, eventsOf(auditLog), "acme")
}

func TestCustodianClaimPath_FailuresNameTheCustodiansTenant(t *testing.T) {
	custSvc := newFakeCustodianSvc()
	custSvc.custodians["c1"] = &custodian.Custodian{ID: "c1", TenantID: "acme", Method: custodian.FactorMethodPIV}
	auditLog := &fakeAuditLogger{}
	ext := &CeremonyExtension{CustodianSvc: custSvc, FactorVerifier: &fakeFactorVerifier{err: errors.New("bad factor")}, AuditLogger: auditLog, Threshold: 2}
	svc := newCeremonyServiceWithExt(t, ext)
	ctx := context.Background()

	_, err := svc.ClaimShareChallenge(ctx, "c1")
	require.NoError(t, err)
	_, err = svc.VerifyClaimAndDeliver(ctx, "c1", "ch", &authfactors.Response{})
	require.ErrorIs(t, err, ErrChallengeVerificationFailed)
	_, err = svc.SubmitUnsealShare(ctx, SubmitUnsealShareRequest{CustodianID: "c1", FactorResponse: &authfactors.Response{}})
	require.ErrorIs(t, err, ErrChallengeVerificationFailed)

	requireEveryEventTenant(t, eventsOf(auditLog), "acme")
}
