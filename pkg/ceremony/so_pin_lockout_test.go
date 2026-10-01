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
)

// TestSignCSRInit_ConcurrentGuessesAreCountedBeforeTheNext: SO PIN guesses
// are checked one at a time and a failure is counted before the next check,
// so firing many guesses at once buys an attacker no more checks than firing
// them in turn. Past the free failures every further guess is refused
// without being checked.
func TestSignCSRInit_ConcurrentGuessesAreCountedBeforeTheNext(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)
	_, err = svc.Initialize(context.Background(), newMockCA(t), nil)
	require.NoError(t, err)
	csrPEM, _ := generateCSRPEM(t, "user-1")

	const guesses = 50
	var checked atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < guesses; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.SignCSRInit(context.Background(), "user-1", "wrong-pin", csrPEM, "admin")
			if errors.Is(err, ErrSOPINMismatch) {
				checked.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := checked.Load(); n > soPINFreeFailures {
		t.Fatalf("%d of %d concurrent SO PIN guesses were checked; want at most %d", n, guesses, soPINFreeFailures)
	}
}

// TestSOPINAttempts_BackoffDoublesAndResets walks the lockout on a fake
// clock: free failures cost nothing; each failure after them locks the check
// for a backoff that doubles; a locked check is refused without comparing,
// even for the right PIN; and the right PIN, once admitted, resets the count.
func TestSOPINAttempts_BackoffDoublesAndResets(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	a := &soPINAttempts{now: func() time.Time { return now }}
	const right = "test-so-pin"

	for i := 0; i < soPINFreeFailures-1; i++ {
		require.ErrorIs(t, a.check("wrong", right), ErrSOPINMismatch)
	}
	// The failure that reaches the free count starts the first lockout.
	require.ErrorIs(t, a.check("wrong", right), ErrSOPINMismatch)

	var locked *SOPINLockedError
	require.ErrorAs(t, a.check(right, right), &locked, "the right PIN must be refused while locked")
	require.Equal(t, soPINBaseBackoff, locked.RetryAfter)
	require.True(t, errors.Is(locked, pin.ErrPINLocked))

	now = now.Add(soPINBaseBackoff)
	require.ErrorIs(t, a.check("wrong", right), ErrSOPINMismatch)
	require.ErrorAs(t, a.check(right, right), &locked)
	require.Equal(t, 2*soPINBaseBackoff, locked.RetryAfter, "the backoff must double")

	now = now.Add(2 * soPINBaseBackoff)
	require.NoError(t, a.check(right, right))
	// Reset: free failures are available again.
	for i := 0; i < soPINFreeFailures-1; i++ {
		require.ErrorIs(t, a.check("wrong", right), ErrSOPINMismatch)
	}
	require.NoError(t, a.check(right, right))
}

// TestSOPINBackoff_Caps: the backoff stops doubling at soPINMaxBackoff and
// does not overflow however long the failures go on.
func TestSOPINBackoff_Caps(t *testing.T) {
	require.Equal(t, soPINBaseBackoff, soPINBackoff(0))
	require.Equal(t, 4*soPINBaseBackoff, soPINBackoff(2))
	require.Equal(t, soPINMaxBackoff, soPINBackoff(20))
	require.Equal(t, soPINMaxBackoff, soPINBackoff(1_000_000))
}
