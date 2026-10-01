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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// registerAnonymouslyAfterInit returns an initialized-then-sealed barrier's
// fresh ceremony -- a restart while sealed -- with its CA set, and an
// anonymous context as the boot-time sealed-window listener marks one.
func registerAnonymouslyAfterInit(t *testing.T) (*CeremonyService, context.Context) {
	t.Helper()
	ctx := context.Background()
	barrier := testBarrier(t)
	require.NoError(t, barrier.Initialize(ctx, seal.Credentials{}))
	require.NoError(t, barrier.Seal())
	svc, err := NewCeremonyService(defaultConfig(), barrier, newTestCredentialService(), testLogger())
	require.NoError(t, err)
	svc.SetCA(newMockCA(t))
	return svc, sealedwindow.WithAnonymous(ctx)
}

// TestRegisterOfficer_RefusesAnonymousAfterEnrollmentClosed: the
// PreOperationalOnly rule is enforced by the ceremony itself, under its own
// lock, not only by the listener's gate, so no path that reaches
// RegisterOfficer anonymously -- a gate that missed it, or one that sampled
// enrollment just before it closed -- can enroll an officer once enrollment
// is over.
func TestRegisterOfficer_RefusesAnonymousAfterEnrollmentClosed(t *testing.T) {
	svc, anon := registerAnonymouslyAfterInit(t)
	csrPEM, _ := generateCSRPEM(t, "mallory")
	_, err := svc.RegisterOfficer(anon, RegisterOfficerRequest{Username: "mallory", CSRPEM: csrPEM})
	require.ErrorIs(t, err, ErrEnrollmentClosed)

	_, err = svc.RegisterRecoveryOfficer(anon, RegisterOfficerRequest{Username: "mallory", CSRPEM: csrPEM})
	require.ErrorIs(t, err, ErrEnrollmentClosed)
}

// TestRegisterOfficer_AuthenticatedCallerUnaffected: a caller whose context
// is not marked anonymous -- an authenticated listener's -- still registers
// officers after enrollment closed, as rekey and recovery need.
func TestRegisterOfficer_AuthenticatedCallerUnaffected(t *testing.T) {
	svc, _ := registerAnonymouslyAfterInit(t)
	csrPEM, _ := generateCSRPEM(t, "alice")
	_, err := svc.RegisterOfficer(context.Background(), RegisterOfficerRequest{Username: "alice", CSRPEM: csrPEM})
	require.NoError(t, err)
}
