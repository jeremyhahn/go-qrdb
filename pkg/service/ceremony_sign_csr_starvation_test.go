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
)

// TestCeremonySignCSR_CorrectPINIsNotStarved: only failed SO PIN checks
// count against the endpoint. A legitimate officer signing many CSRs in a
// row -- or signing while anonymous callers send requests that never reach a
// PIN check -- is never refused for volume alone.
func TestCeremonySignCSR_CorrectPINIsNotStarved(t *testing.T) {
	// go-qrdb#548: sign-csr is HOLDER-only (so, custodian) and
	// PreOperationalOnly, so this must land -- and stay -- in
	// StateEnrolling (an M-of-N ceremony) rather than the single-admin
	// setup that used to drive it straight to StateOperational.
	e := newHolderSvcEnv(t, 2, 2)
	svc := e.cer
	ctx := context.Background()
	h1, h2 := newSvcHolder(t, "officer-1"), newSvcHolder(t, "officer-2")
	_, err := svc.CeremonyInitialize(adminCtx(), e.ca, &CeremonyInitializeOptions{
		SOPin: svcSOPIN, UserPin: "user-pin-1234", Threshold: 2, Hostname: "db.example.com", AdminGenerate: true,
		Officers: []CeremonyOfficerConfig{
			{Username: h1.name, CSRPEM: string(h1.csr)},
			{Username: h2.name, CSRPEM: string(h2.csr)},
		},
	})
	require.NoError(t, err)
	for i := 0; i < 30; i++ {
		// Malformed requests that fail before the PIN is looked at.
		_, err := svc.CeremonySignCSR(ctx, "", "anything", "csr", "so")
		require.Error(t, err)
	}
	for i := 0; i < 30; i++ {
		csrPEM := string(newTestOfficerCSR(t, "officer"))
		if _, err := svc.CeremonySignCSR(ctx, "officer", svcSOPIN, csrPEM, "so"); err != nil {
			t.Fatalf("correct-PIN signing %d: %v", i, err)
		}
	}
}
