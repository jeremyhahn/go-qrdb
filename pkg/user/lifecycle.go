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

// Lifecycle is the canonical account state of a user. It is the SINGLE source
// of truth for whether an account may authenticate; User.Enabled is a derived
// projection of it, maintained by the store.
//
// It replaces the three-field encoding consumers were previously forced to
// carry above qrdb (a state enum plus separate `suspended` and `locked`
// booleans). That encoding admits contradictions — suspended and locked at
// once, or locked while the state says active — with no rule saying which wins,
// and every consumer that reads it has to invent one. A single value cannot
// contradict itself.
type Lifecycle string

const (
	// LifecyclePending is an account that exists but has never been activated:
	// an invited user whose first credential is not yet bound. It cannot
	// authenticate.
	LifecyclePending Lifecycle = "pending"

	// LifecycleActive is the only state in which an account may authenticate.
	LifecycleActive Lifecycle = "active"

	// LifecycleSuspended is an account an administrator has put on hold. It is
	// distinct from disabled so an operator can tell a temporary hold from a
	// decommissioned account, and distinct from locked so a hold is not confused
	// with an automated lockout.
	LifecycleSuspended Lifecycle = "suspended"

	// LifecycleLocked is an account locked by an automated control — a
	// failed-attempt lockout, an anomaly rule. Kept separate from suspended
	// because the two are cleared by different authorities: a lockout is
	// released by the control that set it, a suspension by a human.
	LifecycleLocked Lifecycle = "locked"

	// LifecycleDisabled is an administratively deactivated account. It is the
	// state DisableUser puts an account in, and the state a legacy record with
	// Enabled=false decodes to.
	LifecycleDisabled Lifecycle = "disabled"

	// LifecycleDeleting marks an account whose deletion has begun. It is
	// TERMINAL: no transition leads out of it, so a record that enters it can
	// never be talked back into authenticating.
	//
	// It exists for the registration-during-deletion window: a credential
	// ceremony spans a human touching a security key, so a registration begun
	// just before an operator's delete can finish well after the credential
	// sweep and leave a fresh orphan behind. A deleting marker written before
	// the first deletion mutation gives the ceremony something to refuse
	// against — CanAuthenticate reports false the moment it is set, and because
	// the state is terminal the refusal cannot be raced away by a concurrent
	// enable.
	LifecycleDeleting Lifecycle = "deleting"
)

// lifecycleTransitions is the authoritative transition table: for each state,
// the set of states an account may move to from it. Map-based rather than a
// switch so a transition check is a constant-time lookup and the whole policy
// is readable in one place.
//
// Three rules shape it:
//
//   - Only pending -> active is an ACTIVATION. Every other route to active is a
//     REINSTATEMENT of an account that was already activated.
//   - disabled is reachable from every non-terminal state: an administrator can
//     always deactivate.
//   - deleting is reachable from every non-terminal state and leads nowhere.
//
// An identity transition (a state to itself) is not listed; CanTransitionTo
// admits it separately so that setting a state an account already holds is
// idempotent rather than an error.
var lifecycleTransitions = map[Lifecycle]map[Lifecycle]struct{}{
	LifecyclePending: {
		LifecycleActive:   {},
		LifecycleDisabled: {},
		LifecycleDeleting: {},
	},
	LifecycleActive: {
		LifecycleSuspended: {},
		LifecycleLocked:    {},
		LifecycleDisabled:  {},
		LifecycleDeleting:  {},
	},
	LifecycleSuspended: {
		LifecycleActive:   {},
		LifecycleLocked:   {},
		LifecycleDisabled: {},
		LifecycleDeleting: {},
	},
	LifecycleLocked: {
		LifecycleActive:    {},
		LifecycleSuspended: {},
		LifecycleDisabled:  {},
		LifecycleDeleting:  {},
	},
	LifecycleDisabled: {
		LifecycleActive:   {},
		LifecycleDeleting: {},
	},
	LifecycleDeleting: {},
}

// lifecycleCreatable is the set of states a user may be CREATED in. An account
// is not born suspended or locked — both describe something that happened to an
// account that already existed — and it is certainly not born deleting.
var lifecycleCreatable = map[Lifecycle]struct{}{
	LifecyclePending:  {},
	LifecycleActive:   {},
	LifecycleDisabled: {},
}

// Valid reports whether l is a defined lifecycle state. The empty string is not
// valid: a caller that means "leave it alone" passes the zero value to a request
// field, and the request — not this type — decides that means no change.
func (l Lifecycle) Valid() bool {
	_, ok := lifecycleTransitions[l]
	return ok
}

// Creatable reports whether an account may be created directly in state l.
func (l Lifecycle) Creatable() bool {
	_, ok := lifecycleCreatable[l]
	return ok
}

// CanAuthenticate reports whether an account in state l may complete an
// authentication. Only LifecycleActive may; every other state, including an
// unrecognized one, may not.
//
// It fails closed on purpose. A record carrying a state this build does not
// know — written by a newer node mid-upgrade — must not be treated as able to
// log in because the check did not recognize it.
func (l Lifecycle) CanAuthenticate() bool {
	return l == LifecycleActive
}

// Terminal reports whether l leads to no OTHER state — once an account is in
// it, the only state it can be asked for is the one it already holds.
func (l Lifecycle) Terminal() bool {
	return l.Valid() && len(lifecycleTransitions[l]) == 0
}

// CanTransitionTo reports whether an account may move from l to next.
//
// A transition to the state already held is always admitted, including out of a
// terminal state. Re-declaring the state is not a state change, and refusing it
// would make the deletion marker un-retryable: a delete that writes deleting and
// then fails part way through its cascade is retried by writing deleting again,
// and a caller told "deleting is terminal, you may not" on its second attempt
// would have to special-case the error to make progress. Idempotence on the
// exact value asked for costs nothing — no state is lost by re-asserting it —
// while the guarantee that matters, that nothing reaches a DIFFERENT state from
// deleting, is untouched.
func (l Lifecycle) CanTransitionTo(next Lifecycle) bool {
	if !l.Valid() || !next.Valid() {
		return false
	}
	if l == next {
		return true
	}
	_, ok := lifecycleTransitions[l][next]
	return ok
}

// String returns the wire form of the lifecycle state.
func (l Lifecycle) String() string { return string(l) }

// lifecycleFromEnabled derives a lifecycle state from the boolean encoding that
// preceded it, for records written before this field existed.
//
// The mapping is deliberately lossy in one direction: an account created
// Pending was stored as Enabled=false, indistinguishable on disk from one that
// was disabled, so it decodes as disabled. That is the honest reading — the old
// encoding genuinely could not tell them apart — and it preserves the only
// property that mattered about such a record: it could not authenticate.
func lifecycleFromEnabled(enabled bool) Lifecycle {
	if enabled {
		return LifecycleActive
	}
	return LifecycleDisabled
}

// initialLifecycle resolves the state a CreateUserRequest asks for, reconciling
// the explicit Lifecycle field with the older Pending shorthand.
//
// Disagreement is an ERROR, not a precedence rule. A caller that sets Pending
// and also asks for active has described two different accounts, and picking
// one for them would create whichever the reader of this function happened to
// prefer — the kind of silent choice that shows up later as an invited user who
// could log in without redeeming the invitation.
func initialLifecycle(req *CreateUserRequest) (Lifecycle, error) {
	if req.Lifecycle == "" {
		if req.Pending {
			return LifecyclePending, nil
		}
		return LifecycleActive, nil
	}
	if !req.Lifecycle.Creatable() {
		return "", &InvalidLifecycleError{
			Value:   req.Lifecycle,
			Op:      "create",
			Allowed: creatableLifecycles(),
		}
	}
	if req.Pending && req.Lifecycle != LifecyclePending {
		return "", &ValidationError{
			Field: "lifecycle",
			Reason: "conflicts with pending: pending requests " +
				string(LifecyclePending) + " but lifecycle requests " + string(req.Lifecycle),
		}
	}
	return req.Lifecycle, nil
}

// creatableLifecycles returns the directly-creatable states in a stable order
// for error messages. Ordered by lifecycle progression rather than by map
// iteration so the message reads the same on every call.
func creatableLifecycles() []Lifecycle {
	return []Lifecycle{LifecyclePending, LifecycleActive, LifecycleDisabled}
}

// applyLifecycle moves a record to state `to`, or reports why it may not.
//
// A move to the state already held is applied as a no-op so that repeating a
// request is safe; every other move is checked against the transition table.
// The record is mutated only when the move is permitted, so a refused
// transition leaves the caller's copy exactly as it read it.
func applyLifecycle(u *User, to Lifecycle) error {
	if !to.Valid() {
		return &InvalidLifecycleError{Value: to, Op: "update", Allowed: knownLifecycles()}
	}
	if !u.Lifecycle.CanTransitionTo(to) {
		return &LifecycleTransitionError{ID: u.ID, From: u.Lifecycle, To: to}
	}
	u.Lifecycle = to
	u.Enabled = to.CanAuthenticate()
	return nil
}

// knownLifecycles returns every defined state in progression order, for error
// messages.
func knownLifecycles() []Lifecycle {
	return []Lifecycle{
		LifecyclePending,
		LifecycleActive,
		LifecycleSuspended,
		LifecycleLocked,
		LifecycleDisabled,
		LifecycleDeleting,
	}
}
