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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
)

// signCSRGuessBudget is far more SO PIN guesses than a caller should get
// back-to-back from one node.
const signCSRGuessBudget = 50

// TestCeremonySignCSR_SOPINGuessingIsThrottled: ceremony-sign-csr is
// reachable without authentication on the sealed-window listener, and
// possession of the SO PIN is its only authorization, so it is an online PIN
// oracle on every transport. It used to answer every guess immediately,
// without limit. Guesses are now throttled by the node's SO PIN lockout: after a short burst
// the service refuses with a PIN lockout (pin.ErrPINLocked, answered 429 /
// RESOURCE_EXHAUSTED) instead of checking another guess, and the refusal
// covers the correct PIN too, so it cannot confirm a guess while throttled.
func TestCeremonySignCSR_SOPINGuessingIsThrottled(t *testing.T) {
	// go-qrdb#548: sign-csr is HOLDER-only (so, custodian) and
	// PreOperationalOnly, so this must land -- and stay -- in
	// StateEnrolling (an M-of-N ceremony) rather than the single-admin
	// setup that used to drive it straight to StateOperational.
	e := newHolderSvcEnv(t, 2, 2)
	svc := e.cer
	ctx := context.Background()
	h1, h2 := newSvcHolder(t, "officer-1"), newSvcHolder(t, "officer-2")
	_, err := svc.CeremonyInitialize(adminCtx(), e.ca, &CeremonyInitializeOptions{
		SOPin: svcSOPIN, UserPin: "user-pin-1234", Threshold: 2, Hostname: "db.example.com",
		Officers: []CeremonyOfficerConfig{
			{Username: h1.name, CSRPEM: string(h1.csr)},
			{Username: h2.name, CSRPEM: string(h2.csr)},
		},
	})
	require.NoError(t, err)
	csrPEM := string(newTestOfficerCSR(t, "user-1"))

	throttled := 0
	for i := 0; i < signCSRGuessBudget; i++ {
		_, err := svc.CeremonySignCSR(ctx, "user-1", "wrong-pin", csrPEM, "so")
		switch {
		case errors.Is(err, pin.ErrPINLocked):
			throttled++
		case !errors.Is(err, pin.ErrPINInvalid):
			t.Fatalf("guess %d: %v, want pin.ErrPINInvalid or pin.ErrPINLocked", i, err)
		}
	}
	if throttled == 0 {
		t.Fatalf("%d SO PIN guesses in a row were all checked; want the endpoint throttled", signCSRGuessBudget)
	}

	_, err = svc.CeremonySignCSR(ctx, "user-1", svcSOPIN, csrPEM, "so")
	if !errors.Is(err, pin.ErrPINLocked) {
		t.Fatalf("the correct SO PIN while throttled: %v, want pin.ErrPINLocked", err)
	}
}
