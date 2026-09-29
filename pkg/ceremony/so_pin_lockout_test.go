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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestSignCSRInit_ConcurrentGuessesAreCountedBeforeTheNext: SO PIN guesses
// are checked one at a time and a failure is counted before the next check,
// so firing many guesses at once buys an attacker no more checks than firing
// them in turn. Past the free failures every further guess is refused
// without being checked.
func TestSignCSRInit_ConcurrentGuessesAreCountedBeforeTheNext(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	_, err := e.svc.Initialize(context.Background(), e.ca, initOpts(0))
	require.NoError(t, err)
	h := newHolder(t, "user-1", seal.RoleSecurityOfficer)

	const guesses = 50
	var checked atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < guesses; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.SignCSRInit(context.Background(), "user-1", "wrong-pin", h.csrPEM, "admin")
			if errors.Is(err, pin.ErrPINInvalid) {
				checked.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := checked.Load(); n > soPINFreeFailures {
		t.Fatalf("%d of %d concurrent SO PIN guesses were checked; want at most %d", n, guesses, soPINFreeFailures)
	}
}

// TestSignCSRInit_LockedOutPINIsNotChecked: once locked out, even the right
// SO PIN is refused without being checked, as *SOPINLockedError.
func TestSignCSRInit_LockedOutPINIsNotChecked(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	_, err := e.svc.Initialize(context.Background(), e.ca, initOpts(0))
	require.NoError(t, err)
	h := newHolder(t, "user-1", seal.RoleSecurityOfficer)
	for i := 0; i < soPINFreeFailures; i++ {
		_, err := e.svc.SignCSRInit(context.Background(), "user-1", "wrong-pin", h.csrPEM, "admin")
		require.ErrorIs(t, err, pin.ErrPINInvalid)
	}
	_, err = e.svc.SignCSRInit(context.Background(), "user-1", "so-pin-1234", h.csrPEM, "admin")
	var locked *SOPINLockedError
	require.ErrorAs(t, err, &locked)
}

// wrongPIN and rightPIN are the verifications the PIN backend answers.
func wrongPIN() error { return pin.ErrPINInvalid }
func rightPIN() error { return nil }

// TestSOPINAttempts_BackoffDoublesAndResets walks the lockout on a fake
// clock: free failures cost nothing; each failure after them locks the check
// for a backoff that doubles; a locked check is refused without verifying,
// even for the right PIN; and the right PIN, once admitted, resets the count.
func TestSOPINAttempts_BackoffDoublesAndResets(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	a := &soPINAttempts{now: func() time.Time { return now }}

	for i := 0; i < soPINFreeFailures-1; i++ {
		require.ErrorIs(t, a.check(wrongPIN), pin.ErrPINInvalid)
	}
	// The failure that reaches the free count starts the first lockout.
	require.ErrorIs(t, a.check(wrongPIN), pin.ErrPINInvalid)

	var locked *SOPINLockedError
	verified := false
	require.ErrorAs(t, a.check(func() error { verified = true; return nil }), &locked, "the right PIN must be refused while locked")
	require.False(t, verified, "a locked check must not run the verification")
	require.Equal(t, soPINBaseBackoff, locked.RetryAfter)
	require.True(t, errors.Is(locked, pin.ErrPINLocked))

	now = now.Add(soPINBaseBackoff)
	require.ErrorIs(t, a.check(wrongPIN), pin.ErrPINInvalid)
	require.ErrorAs(t, a.check(rightPIN), &locked)
	require.Equal(t, 2*soPINBaseBackoff, locked.RetryAfter, "the backoff must double")

	now = now.Add(2 * soPINBaseBackoff)
	require.NoError(t, a.check(rightPIN))
	// Reset: free failures are available again.
	for i := 0; i < soPINFreeFailures-1; i++ {
		require.ErrorIs(t, a.check(wrongPIN), pin.ErrPINInvalid)
	}
	require.NoError(t, a.check(rightPIN))
}

// TestSOPINAttempts_OnlyAWrongPINCounts: a backend that cannot answer is not
// a guess, so it neither counts toward the lockout nor is masked.
func TestSOPINAttempts_OnlyAWrongPINCounts(t *testing.T) {
	a := &soPINAttempts{}
	for i := 0; i < 3*soPINFreeFailures; i++ {
		require.ErrorIs(t, a.check(func() error { return pin.ErrBackendNotConfigured }), pin.ErrBackendNotConfigured)
	}
	require.NoError(t, a.check(rightPIN))
}

// TestSOPINBackoff_Caps: the backoff stops doubling at soPINMaxBackoff and
// does not overflow however long the failures go on.
func TestSOPINBackoff_Caps(t *testing.T) {
	require.Equal(t, soPINBaseBackoff, soPINBackoff(0))
	require.Equal(t, 4*soPINBaseBackoff, soPINBackoff(2))
	require.Equal(t, soPINMaxBackoff, soPINBackoff(20))
	require.Equal(t, soPINMaxBackoff, soPINBackoff(1_000_000))
}
