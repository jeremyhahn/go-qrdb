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

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
)

// TestBarrierOfficerClaimStatus_RedactedUnlessAuthenticated: claim status
// names officers only to an authenticated caller. Redaction is the default,
// not something a transport has to remember to ask for: a context with no
// identity -- whatever listener or composer produced it -- gets the share
// indexes and claim state only.
func TestBarrierOfficerClaimStatus_RedactedUnlessAuthenticated(t *testing.T) {
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

	unauthenticated, err := svc.BarrierOfficerClaimStatus(ctx)
	require.NoError(t, err)
	require.Len(t, unauthenticated, 1)
	require.Empty(t, unauthenticated[0].Username, "a caller with no identity must not see officer usernames")
	require.Empty(t, unauthenticated[0].CertThumbprint)
	require.Empty(t, unauthenticated[0].AuthFactor)
	require.Equal(t, 1, unauthenticated[0].ShareIndex)

	authenticated, err := svc.BarrierOfficerClaimStatus(auth.WithIdentity(ctx, &auth.Identity{Subject: "admin"}))
	require.NoError(t, err)
	require.Len(t, authenticated, 1)
	require.Equal(t, "alice-officer", authenticated[0].Username)
	require.NotEmpty(t, authenticated[0].CertThumbprint)
}
