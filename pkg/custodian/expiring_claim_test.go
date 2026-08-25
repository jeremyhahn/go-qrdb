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

package custodian_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
)

// usernameClaimKey builds the username index key a registration claims.
//
// Written out here rather than taken from the package: this is the layout the
// production code is being checked against, so deriving it from that code
// would make the yardstick move with the thing it measures.
func usernameClaimKey(tenantID, username string) string {
	return "__sys:custodian-idx/username/" + tenantID + "/" + username
}

// strandedClaim builds the value an interrupted registration leaves on the
// username index key, expiring at the given instant.
//
// Also written independently. The claim's wire layout -- "claim:<unix
// seconds>:<token>" -- IS the contract under test: a test that asked the
// package to build its own fixture would pass against any layout the package
// happened to produce, including one no other build could read.
func strandedClaim(expiry time.Time) []byte {
	return []byte("claim:" + strconv.FormatInt(expiry.Unix(), 10) + ":StaleTokenFromACrashedRegistration")
}

// strandRegistration puts a claim on the username key the way a process that
// died mid-registration would leave it: the claim written, nothing else.
func strandRegistration(t *testing.T, store *memKV, tenantID, username string, expiry time.Time) {
	t.Helper()
	if err := store.Put(context.Background(),
		usernameClaimKey(tenantID, username), strandedClaim(expiry)); err != nil {
		t.Fatalf("strand the claim: %v", err)
	}
}

// TestRegister_LiveClaimIsRefused is the property the expiry must not break.
//
// A claim that has NOT expired belongs to a registration that is running or
// has only just been interrupted. Taking it over would admit exactly the
// concurrent duplicate the claim exists to prevent: two enrolments for one
// identity, each holding a sealed share, only one of which the store records.
func TestRegister_LiveClaimIsRefused(t *testing.T) {
	svc, store := newTestService(t)
	req, _ := testRegisterRequest(t)

	strandRegistration(t, store, req.TenantID, req.Username, time.Now().Add(time.Hour))

	_, err := svc.RegisterCustodian(context.Background(), req)
	var incomplete *custodian.IncompleteEnrolmentError
	if !errors.As(err, &incomplete) {
		t.Fatalf("RegisterCustodian = %v, want *IncompleteEnrolmentError", err)
	}
	if !incomplete.Readable {
		t.Error("the claim carried a readable expiry but the error says otherwise")
	}
	if incomplete.ExpiresAt.IsZero() {
		t.Error("the error carries no expiry, so a caller cannot tell when to retry")
	}
	if !strings.Contains(err.Error(), "expires") && !strings.Contains(err.Error(), "retry after") {
		t.Errorf("error %q does not tell the caller when the claim releases", err)
	}
}

// TestRegister_ExpiredClaimIsTakenOver is the ruling: option (c), an expiring
// username claim (owner ruling 2026-08-23, go-qrdb#21).
//
// A process that died between taking the claim and committing or compensating
// used to strand the username permanently -- it could not enrol until an
// operator deleted the key by hand. Nothing was lost with it (the claim is
// taken before any share is sealed), but the identity was stuck.
func TestRegister_ExpiredClaimIsTakenOver(t *testing.T) {
	svc, store := newTestService(t)
	req, _ := testRegisterRequest(t)

	strandRegistration(t, store, req.TenantID, req.Username, time.Now().Add(-time.Hour))

	resp, err := svc.RegisterCustodian(context.Background(), req)
	if err != nil {
		t.Fatalf("RegisterCustodian over an expired claim: %v", err)
	}
	if resp.SealedEnvelope == nil {
		t.Error("registration returned no sealed envelope")
	}

	// The claim must be gone, replaced by the committed identifier -- not
	// merely overwritten by another claim.
	indexed, err := store.Get(context.Background(), usernameClaimKey(req.TenantID, req.Username))
	if err != nil {
		t.Fatalf("read the username index: %v", err)
	}
	if strings.HasPrefix(string(indexed), "claim:") {
		t.Fatalf("the username index still holds a claim (%q); the take-over did not commit", indexed)
	}
	if string(indexed) != resp.Custodian.ID {
		t.Errorf("username index holds %q, want the registered custodian %q", indexed, resp.Custodian.ID)
	}
}

// TestRegister_UnreadableClaimIsNotTakenOver pins the fail-closed direction.
//
// A claim this build cannot parse -- one written before expiries existed, or a
// corrupt value -- might belong to a registration that is still running. There
// is no expiry to compare against, so it is treated as never expiring and left
// for an operator. Treating an unreadable claim as expired would make every
// unrecognised value a licence to enrol a duplicate.
func TestRegister_UnreadableClaimIsNotTakenOver(t *testing.T) {
	for name, value := range map[string][]byte{
		"pre-expiry format":  []byte("claim:TokenWithNoExpiryField"),
		"non-numeric expiry": []byte("claim:not-a-timestamp:Token"),
		"empty expiry":       []byte("claim::Token"),
	} {
		t.Run(name, func(t *testing.T) {
			svc, store := newTestService(t)
			req, _ := testRegisterRequest(t)

			if err := store.Put(context.Background(),
				usernameClaimKey(req.TenantID, req.Username), value); err != nil {
				t.Fatalf("seed the claim: %v", err)
			}

			_, err := svc.RegisterCustodian(context.Background(), req)
			var incomplete *custodian.IncompleteEnrolmentError
			if !errors.As(err, &incomplete) {
				t.Fatalf("RegisterCustodian = %v, want *IncompleteEnrolmentError", err)
			}
			if incomplete.Readable {
				t.Error("an unparseable claim was reported as carrying a readable expiry")
			}
			if !strings.Contains(err.Error(), "remove") {
				t.Errorf("error %q does not tell the operator the remedy", err)
			}
		})
	}
}

// TestRegister_ConcurrentTakeOverAdmitsExactlyOne is why the take-over is a
// compare-and-swap rather than a delete followed by a create.
//
// Two registrations meeting one expired claim would otherwise both remove it
// and both create their own, each believing it owned the identity -- the
// window between the read and the write being exactly what a guarded write
// exists to close. With CAS the loser compares against bytes that are no
// longer there.
func TestRegister_ConcurrentTakeOverAdmitsExactlyOne(t *testing.T) {
	svc, store := newTestService(t)
	reqA, _ := testRegisterRequest(t)
	reqB, _ := testRegisterRequest(t)
	reqB.Username = reqA.Username
	reqB.TenantID = reqA.TenantID

	strandRegistration(t, store, reqA.TenantID, reqA.Username, time.Now().Add(-time.Hour))

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		wins    int
		lastErr error
	)
	start := make(chan struct{})
	for _, req := range []custodian.RegisterRequest{reqA, reqB} {
		wg.Add(1)
		go func(r custodian.RegisterRequest) {
			defer wg.Done()
			<-start
			_, err := svc.RegisterCustodian(context.Background(), r)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else {
				lastErr = err
			}
		}(req)
	}
	close(start)
	wg.Wait()

	if wins != 1 {
		t.Fatalf("%d of 2 concurrent registrations succeeded over one expired claim, want exactly 1 "+
			"(last error: %v)", wins, lastErr)
	}

	// And the survivor is a committed custodian, not a leftover claim.
	indexed, err := store.Get(context.Background(), usernameClaimKey(reqA.TenantID, reqA.Username))
	if err != nil {
		t.Fatalf("read the username index: %v", err)
	}
	if strings.HasPrefix(string(indexed), "claim:") {
		t.Fatalf("the username index holds a claim (%q) after both attempts settled", indexed)
	}
}

// TestRegister_FreshClaimCarriesAFutureExpiry checks the claim a registration
// writes is itself readable and in the future.
//
// Without this the take-over could pass every test above while writing claims
// nothing could ever expire -- the defect would simply move to the next
// crashed registration.
func TestRegister_FreshClaimCarriesAFutureExpiry(t *testing.T) {
	store := newHookKV()
	svc := newHookService(t, store)
	req, _ := testRegisterRequest(t)

	var claimValue []byte
	store.hookClaim(func(key string, value []byte) error {
		if strings.HasPrefix(key, "__sys:custodian-idx/username/") {
			claimValue = append([]byte(nil), value...)
		}
		return nil
	})

	if _, err := svc.RegisterCustodian(context.Background(), req); err != nil {
		t.Fatalf("RegisterCustodian: %v", err)
	}
	if claimValue == nil {
		t.Fatal("registration took no username claim")
	}

	rest, ok := strings.CutPrefix(string(claimValue), "claim:")
	if !ok {
		t.Fatalf("claim %q does not carry the claim prefix", claimValue)
	}
	secs, _, ok := strings.Cut(rest, ":")
	if !ok {
		t.Fatalf("claim %q carries no expiry field", claimValue)
	}
	unix, err := strconv.ParseInt(secs, 10, 64)
	if err != nil {
		t.Fatalf("claim %q has an unparseable expiry: %v", claimValue, err)
	}
	expiry := time.Unix(unix, 0)
	if !expiry.After(time.Now()) {
		t.Errorf("a fresh claim expires at %s, which is not in the future — it would be "+
			"taken over immediately", expiry.UTC().Format(time.RFC3339))
	}
	if expiry.After(time.Now().Add(time.Hour)) {
		t.Errorf("a fresh claim expires at %s, more than an hour out — a crash would strand "+
			"the username for that long", expiry.UTC().Format(time.RFC3339))
	}
}

// TestRegister_TakeOverNeverLeavesTheKeyAbsent is why the take-over is a
// compare-and-swap and not a delete followed by a create.
//
// Both leave the store looking identical once they have run, so the finished
// state cannot tell them apart. The difference is an INSTANT: delete-then-
// create leaves the username unclaimed in between, and any registration
// passing through that instant creates its own claim and believes it owns the
// identity -- the concurrent duplicate the claim exists to prevent,
// reintroduced by the repair.
//
// The observable is therefore the absence itself: a take-over must never
// delete the claim key. Asserted with a hook rather than by racing two
// registrations, because a race only sometimes lands inside the window and a
// test that catches a defect sometimes is a test that reports it passing.
// (Measured: a delete-then-create take-over survived the concurrent test
// below.)
func TestRegister_TakeOverNeverLeavesTheKeyAbsent(t *testing.T) {
	store := newHookKV()
	svc := newHookService(t, store)
	req, _ := testRegisterRequest(t)

	key := usernameClaimKey(req.TenantID, req.Username)
	strandRegistration(t, store.memKV, req.TenantID, req.Username, time.Now().Add(-time.Hour))

	var deletedTheClaim bool
	store.hookDelete(func(k string) error {
		if k == key {
			deletedTheClaim = true
		}
		return nil
	})

	if _, err := svc.RegisterCustodian(context.Background(), req); err != nil {
		t.Fatalf("RegisterCustodian over an expired claim: %v", err)
	}
	if deletedTheClaim {
		t.Error("the take-over DELETED the claim key; between that delete and the " +
			"create that follows it the username is unclaimed, and a registration " +
			"passing through takes it too")
	}
}
