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

package user

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Lifecycle value semantics
// ---------------------------------------------------------------------------

func TestLifecycle_ValidRecognizesEveryDefinedState(t *testing.T) {
	for _, l := range knownLifecycles() {
		if !l.Valid() {
			t.Errorf("Valid(%q): got false, want true", l)
		}
		if l.String() != string(l) {
			t.Errorf("String(%q): got %q", l, l.String())
		}
	}
}

func TestLifecycle_ValidRejectsUnknownAndEmpty(t *testing.T) {
	for _, l := range []Lifecycle{"", "quarantined", "ACTIVE", "Active", " active"} {
		if l.Valid() {
			t.Errorf("Valid(%q): got true, want false", l)
		}
	}
}

// CanAuthenticate is the check a login ceremony makes, so it must fail closed on
// a state this build does not recognize — a record written by a newer node
// mid-upgrade must not be waved through because the switch had no case for it.
func TestLifecycle_OnlyActiveCanAuthenticate(t *testing.T) {
	for _, l := range knownLifecycles() {
		want := l == LifecycleActive
		if got := l.CanAuthenticate(); got != want {
			t.Errorf("CanAuthenticate(%q): got %v, want %v", l, got, want)
		}
	}
	for _, unknown := range []Lifecycle{"", "quarantined", "ACTIVE"} {
		if unknown.CanAuthenticate() {
			t.Errorf("CanAuthenticate(%q): got true; unrecognized states must fail closed", unknown)
		}
	}
}

func TestLifecycle_TerminalIsExactlyDeleting(t *testing.T) {
	for _, l := range knownLifecycles() {
		want := l == LifecycleDeleting
		if got := l.Terminal(); got != want {
			t.Errorf("Terminal(%q): got %v, want %v", l, got, want)
		}
	}
	if Lifecycle("quarantined").Terminal() {
		t.Error("Terminal(unknown): got true; an unrecognized state is not a known terminal one")
	}
}

func TestLifecycle_CreatableExcludesSuspendedLockedAndDeleting(t *testing.T) {
	creatable := map[Lifecycle]bool{
		LifecyclePending:  true,
		LifecycleActive:   true,
		LifecycleDisabled: true,
	}
	for _, l := range knownLifecycles() {
		if got := l.Creatable(); got != creatable[l] {
			t.Errorf("Creatable(%q): got %v, want %v", l, got, creatable[l])
		}
	}
	if Lifecycle("quarantined").Creatable() {
		t.Error("Creatable(unknown): got true, want false")
	}
}

// The transition table is the account-state policy, so it is asserted
// exhaustively rather than by spot check: every ordered pair of defined states
// is named here with the answer it must give. A pair that changes meaning has to
// be changed here too, which is the point.
func TestLifecycle_CanTransitionToCoversEveryPair(t *testing.T) {
	allowed := map[Lifecycle]map[Lifecycle]bool{
		LifecyclePending: {
			LifecyclePending: true, LifecycleActive: true, LifecycleDisabled: true, LifecycleDeleting: true,
			LifecycleSuspended: false, LifecycleLocked: false,
		},
		LifecycleActive: {
			LifecycleActive: true, LifecycleSuspended: true, LifecycleLocked: true,
			LifecycleDisabled: true, LifecycleDeleting: true, LifecyclePending: false,
		},
		LifecycleSuspended: {
			LifecycleSuspended: true, LifecycleActive: true, LifecycleLocked: true,
			LifecycleDisabled: true, LifecycleDeleting: true, LifecyclePending: false,
		},
		LifecycleLocked: {
			LifecycleLocked: true, LifecycleActive: true, LifecycleSuspended: true,
			LifecycleDisabled: true, LifecycleDeleting: true, LifecyclePending: false,
		},
		LifecycleDisabled: {
			LifecycleDisabled: true, LifecycleActive: true, LifecycleDeleting: true,
			LifecyclePending: false, LifecycleSuspended: false, LifecycleLocked: false,
		},
		LifecycleDeleting: {
			// Terminal: only the identity move, which keeps a partially
			// completed deletion resumable.
			LifecycleDeleting: true,
			LifecyclePending:  false, LifecycleActive: false, LifecycleSuspended: false,
			LifecycleLocked: false, LifecycleDisabled: false,
		},
	}
	for _, from := range knownLifecycles() {
		for _, to := range knownLifecycles() {
			want, named := allowed[from][to]
			if !named {
				t.Fatalf("test table is incomplete: %q -> %q is not named", from, to)
			}
			if got := from.CanTransitionTo(to); got != want {
				t.Errorf("CanTransitionTo(%q -> %q): got %v, want %v", from, to, got, want)
			}
		}
	}
}

// The guarantee the deleting marker exists for: nothing talks a deletion in
// progress back into an account that can authenticate.
func TestLifecycle_NothingEscapesDeleting(t *testing.T) {
	for _, to := range knownLifecycles() {
		if to == LifecycleDeleting {
			continue
		}
		if LifecycleDeleting.CanTransitionTo(to) {
			t.Errorf("deleting -> %q was admitted; deleting must be terminal", to)
		}
	}
}

func TestLifecycle_CanTransitionToRejectsUnknownOnEitherSide(t *testing.T) {
	if LifecycleActive.CanTransitionTo("quarantined") {
		t.Error("active -> unknown was admitted")
	}
	if Lifecycle("quarantined").CanTransitionTo(LifecycleActive) {
		t.Error("unknown -> active was admitted")
	}
	if Lifecycle("").CanTransitionTo(Lifecycle("")) {
		t.Error("empty -> empty was admitted; the empty state is not a state")
	}
}

// ---------------------------------------------------------------------------
// initialLifecycle
// ---------------------------------------------------------------------------

func TestInitialLifecycle_ResolvesEveryRequestShape(t *testing.T) {
	cases := []struct {
		name string
		req  CreateUserRequest
		want Lifecycle
	}{
		{"defaults to active", CreateUserRequest{}, LifecycleActive},
		{"pending shorthand", CreateUserRequest{Pending: true}, LifecyclePending},
		{"explicit active", CreateUserRequest{Lifecycle: LifecycleActive}, LifecycleActive},
		{"explicit disabled", CreateUserRequest{Lifecycle: LifecycleDisabled}, LifecycleDisabled},
		{"explicit pending", CreateUserRequest{Lifecycle: LifecyclePending}, LifecyclePending},
		{"both agreeing", CreateUserRequest{Pending: true, Lifecycle: LifecyclePending}, LifecyclePending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := initialLifecycle(&tc.req)
			if err != nil {
				t.Fatalf("initialLifecycle: unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInitialLifecycle_RejectsNonCreatableState(t *testing.T) {
	for _, l := range []Lifecycle{LifecycleSuspended, LifecycleLocked, LifecycleDeleting, "quarantined"} {
		t.Run(string(l), func(t *testing.T) {
			_, err := initialLifecycle(&CreateUserRequest{Lifecycle: l})
			var invalid *InvalidLifecycleError
			if !errors.As(err, &invalid) {
				t.Fatalf("expected *InvalidLifecycleError, got %T: %v", err, err)
			}
			if invalid.Value != l {
				t.Errorf("Value: got %q, want %q", invalid.Value, l)
			}
			if invalid.Op != "create" {
				t.Errorf("Op: got %q, want %q", invalid.Op, "create")
			}
			// The error must name what the caller may send instead; a bare
			// "invalid" leaves them guessing.
			if len(invalid.Allowed) != 3 {
				t.Fatalf("Allowed: got %v, want the three creatable states", invalid.Allowed)
			}
			if !strings.Contains(invalid.Error(), "pending, active, disabled") {
				t.Errorf("Error(): %q does not list the allowed states", invalid.Error())
			}
		})
	}
}

// Pending and an explicit contradicting Lifecycle describe two different
// accounts. Picking one silently is how an invited user ends up able to log in
// without redeeming the invitation, so the request is refused instead.
func TestInitialLifecycle_RejectsPendingConflictingWithLifecycle(t *testing.T) {
	for _, l := range []Lifecycle{LifecycleActive, LifecycleDisabled} {
		_, err := initialLifecycle(&CreateUserRequest{Pending: true, Lifecycle: l})
		var v *ValidationError
		if !errors.As(err, &v) {
			t.Fatalf("Pending+%q: expected *ValidationError, got %T: %v", l, err, err)
		}
		if v.Field != "lifecycle" {
			t.Errorf("Field: got %q, want %q", v.Field, "lifecycle")
		}
	}
}

// ---------------------------------------------------------------------------
// applyLifecycle
// ---------------------------------------------------------------------------

func TestApplyLifecycle_MovesState(t *testing.T) {
	u := &User{ID: 7, Lifecycle: LifecyclePending}
	if err := applyLifecycle(u, LifecycleActive); err != nil {
		t.Fatalf("applyLifecycle: %v", err)
	}
	if u.Lifecycle != LifecycleActive || !u.CanAuthenticate() {
		t.Fatalf("got lifecycle=%q canAuthenticate=%v, want active/true", u.Lifecycle, u.CanAuthenticate())
	}
	if err := applyLifecycle(u, LifecycleSuspended); err != nil {
		t.Fatalf("applyLifecycle(suspend): %v", err)
	}
	if u.CanAuthenticate() {
		t.Error("a suspended account may still authenticate")
	}
}

func TestApplyLifecycle_IsIdempotentIncludingFromTerminal(t *testing.T) {
	u := &User{ID: 8, Lifecycle: LifecycleDeleting}
	// A delete that failed part way through its cascade is retried by writing
	// the marker again; refusing that would make the retry special-case an
	// error to make progress.
	if err := applyLifecycle(u, LifecycleDeleting); err != nil {
		t.Fatalf("re-asserting deleting must be a no-op, got: %v", err)
	}
	if u.Lifecycle != LifecycleDeleting || u.CanAuthenticate() {
		t.Fatalf("got lifecycle=%q canAuthenticate=%v", u.Lifecycle, u.CanAuthenticate())
	}
}

func TestApplyLifecycle_RefusesUnconnectedTransitionAndLeavesRecordUntouched(t *testing.T) {
	u := &User{ID: 9, Lifecycle: LifecycleDeleting}
	err := applyLifecycle(u, LifecycleActive)
	var te *LifecycleTransitionError
	if !errors.As(err, &te) {
		t.Fatalf("expected *LifecycleTransitionError, got %T: %v", err, err)
	}
	if te.ID != 9 || te.From != LifecycleDeleting || te.To != LifecycleActive {
		t.Errorf("error carries %d %q->%q", te.ID, te.From, te.To)
	}
	if !strings.Contains(te.Error(), "terminal") {
		t.Errorf("Error(): %q should say deleting is terminal", te.Error())
	}
	if u.Lifecycle != LifecycleDeleting {
		t.Error("a refused transition mutated the record")
	}
}

func TestApplyLifecycle_RefusesUnknownTargetState(t *testing.T) {
	u := &User{ID: 10, Lifecycle: LifecycleActive}
	err := applyLifecycle(u, "quarantined")
	var invalid *InvalidLifecycleError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *InvalidLifecycleError, got %T: %v", err, err)
	}
	if invalid.Op != "update" {
		t.Errorf("Op: got %q, want %q", invalid.Op, "update")
	}
	if u.Lifecycle != LifecycleActive {
		t.Error("a refused transition mutated the record")
	}
}

// ---------------------------------------------------------------------------
// User.CanAuthenticate
// ---------------------------------------------------------------------------

func TestUserCanAuthenticate(t *testing.T) {
	if (&User{Lifecycle: LifecycleActive}).CanAuthenticate() != true {
		t.Error("active user cannot authenticate")
	}
	for _, l := range []Lifecycle{LifecyclePending, LifecycleSuspended, LifecycleLocked, LifecycleDisabled, LifecycleDeleting, "quarantined"} {
		if (&User{Lifecycle: l}).CanAuthenticate() {
			t.Errorf("user in %q may authenticate", l)
		}
	}
	// A nil record is not an authenticated one. Callers resolve a user and then
	// ask; a nil dereference here would be a panic on the login path.
	var nilUser *User
	if nilUser.CanAuthenticate() {
		t.Error("nil user reported able to authenticate")
	}
}

// An ABSENT state fails closed exactly like an unrecognized one. Lifecycle is
// the only account-state field; there is no older encoding to fall back to, so
// a record that carries no state is a record that cannot authenticate.
func TestUserCanAuthenticate_EmptyLifecycleFailsClosed(t *testing.T) {
	if (&User{ID: 1, Username: "alice"}).CanAuthenticate() {
		t.Error("a record with no lifecycle state may authenticate")
	}
	// The same record decoded from JSON — including one carrying the removed
	// "enabled" key — still has no state and must still be refused.
	var decoded User
	if err := json.Unmarshal([]byte(`{"id":1,"username":"alice","enabled":true}`), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Lifecycle != "" {
		t.Fatalf("Lifecycle: got %q, want empty", decoded.Lifecycle)
	}
	if decoded.CanAuthenticate() {
		t.Error("a decoded record with no lifecycle state may authenticate")
	}
}
