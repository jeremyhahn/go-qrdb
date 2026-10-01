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

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
)

// TestBarrierOfficerClaimStatus_RedactedForAnonymousCallers: a call the
// boot-time sealed-window listener admitted (sealedwindow.Anonymous) gets the
// share indexes and claim state without the officers' identities; any other
// caller gets the full record.
func TestBarrierOfficerClaimStatus_RedactedForAnonymousCallers(t *testing.T) {
	ctx := context.Background()
	barrier, _ := newTestSoftwareBarrier(t)
	core, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: t.TempDir()}, barrier, newTestCredentialService(), testLogger())
	require.NoError(t, err)
	core.SetCA(newTestCeremonyCA(t))
	svc := NewBarrierServiceWithCeremony(barrier, core)

	_, err = svc.BarrierOfficerRegister(ctx, &BarrierOfficerRegisterRequest{
		Username: "alice-officer",
		CSRPEM:   newTestOfficerCSR(t, "alice-officer"),
	})
	require.NoError(t, err)

	full, err := svc.BarrierOfficerClaimStatus(auth.WithIdentity(ctx, &auth.Identity{Subject: "admin"}))
	require.NoError(t, err)
	require.Len(t, full, 1)
	require.Equal(t, "alice-officer", full[0].Username)
	require.NotEmpty(t, full[0].CertThumbprint)

	// The anonymous mark redacts even with an identity in the context.
	redacted, err := svc.BarrierOfficerClaimStatus(sealedwindow.WithAnonymous(auth.WithIdentity(ctx, &auth.Identity{Subject: "admin"})))
	require.NoError(t, err)
	require.Len(t, redacted, 1)
	require.Empty(t, redacted[0].Username)
	require.Empty(t, redacted[0].CertThumbprint)
	require.Empty(t, redacted[0].AuthFactor)
	require.Equal(t, full[0].ShareIndex, redacted[0].ShareIndex)
	require.Equal(t, full[0].EnrolledAt, redacted[0].EnrolledAt)
	require.Equal(t, full[0].ClaimedAt, redacted[0].ClaimedAt)
}
