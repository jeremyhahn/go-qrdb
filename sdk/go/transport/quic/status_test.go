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

// What the QUIC SDK client does with each wire status (go-qrdb#437).
//
// No build constraint: these name only mapWireError, isWireNotFound, the
// call-site mappers and the wire constants, all unconstrained.

package quic

import (
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestMapWireError_EveryStatusIsLegible walks the whole vocabulary through the
// client mapper.
//
// Every status must produce an error that NAMES its condition: a status the
// client renders with no label leaves an operator reading the server's prose
// to find out what happened, which is the state the six-status wire left every
// condition in.
func TestMapWireError_EveryStatusIsLegible(t *testing.T) {
	for st := uint16(0); wire.StatusDeclared(st); st++ {
		if st == wire.StatusOK {
			// StatusOK never reaches this function — call() returns the
			// payload rather than an error.
			continue
		}
		t.Run(wire.StatusName(st), func(t *testing.T) {
			err := mapWireError("kv.Get", st, errorBody(t, st, "the server's reason"))
			if err == nil {
				t.Fatal("a non-OK status must produce an error")
			}
			if !strings.Contains(err.Error(), "the server's reason") {
				t.Errorf("the server's reason was dropped: %q", err.Error())
			}

			// The two typed reconstitutions are asserted individually below;
			// every other status carries its number so a caller can act on it.
			if _, typed := typedWireErrors[st]; typed {
				return
			}
			var reqErr *transport.RequestError
			if !errors.As(err, &reqErr) {
				t.Fatalf("expected *transport.RequestError, got %T", err)
			}
			if reqErr.StatusCode != int(st) {
				t.Errorf("StatusCode = %d, want %d", reqErr.StatusCode, st)
			}
			// StatusError is the deliberate exception: it is the fallback,
			// its message IS the whole story, and labelling it would change
			// the text of every generic QUIC failure a consumer already logs
			// for no gain in classification.
			if st != wire.StatusError && wireStatusPrefixes[st] == "" {
				t.Errorf("%s has no label, so the error reads as the server's "+
					"sentence with no classification attached", wire.StatusName(st))
			}
		})
	}
}

// TestMapWireError_UnknownStatusDegradesRatherThanMisclassifies is the
// version-skew guard. A status this build does not declare is a NEWER server's
// condition; reporting it as a generic failure is right, and reporting it as
// any condition this build knows would be a lie.
func TestMapWireError_UnknownStatusDegradesRatherThanMisclassifies(t *testing.T) {
	const future = uint16(4242)
	err := mapWireError("kv.Get", future, errorBody(t, future, "a condition from a newer server"))

	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
	if reqErr.StatusCode != int(future) {
		t.Errorf("StatusCode = %d, want %d — the number must survive so an "+
			"operator can look it up", reqErr.StatusCode, future)
	}
	var knf *transport.KeyNotFoundError
	var cue *transport.CapabilityUnsupportedError
	if errors.As(err, &knf) || errors.As(err, &cue) {
		t.Fatal("an unrecognised status was reconstituted as a condition this build knows")
	}
}

// TestMapWireError_UnavailableIsNoLongerWordedAsASealedBarrier.
//
// The label read "barrier sealed", which was true of the only condition that
// produced StatusUnavailable when it was written. A closed store, a drain, a
// non-leader and an exhausted cluster all produce it now, and telling an
// operator their barrier is sealed sends them to unseal something that was
// never locked.
func TestMapWireError_UnavailableIsNoLongerWordedAsASealedBarrier(t *testing.T) {
	err := mapWireError("kv.Put", wire.StatusUnavailable, errorBody(t, wire.StatusUnavailable, "storage: store is closed"))
	if strings.Contains(err.Error(), "barrier sealed") {
		t.Fatalf("a closed store was reported as a sealed barrier: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("got %q", err.Error())
	}
}

// TestMapWireError_UnimplementedStaysTyped: the reconstitution go-qrdb#141 put
// here is untouched by the vocabulary change.
func TestMapWireError_UnimplementedStaysTyped(t *testing.T) {
	err := mapWireError("kv.Scan", wire.StatusUnimplemented, errorBody(t, wire.StatusUnimplemented, "no opcode"))
	var cue *transport.CapabilityUnsupportedError
	if !errors.As(err, &cue) {
		t.Fatalf("expected *transport.CapabilityUnsupportedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// isWireNotFound and the call-site mappers
// ---------------------------------------------------------------------------

// TestIsWireNotFound fires on the shared mapper's not-found and on nothing else.
func TestIsWireNotFound(t *testing.T) {
	if !isWireNotFound(mapWireError("kv.Get", wire.StatusNotFound, errorBody(t, wire.StatusNotFound, "gone"))) {
		t.Error("a StatusNotFound frame must be recognised as absence")
	}
	for _, st := range []uint16{
		wire.StatusError, wire.StatusUnavailable, wire.StatusInvalidArgument,
		wire.StatusPermissionDenied, wire.StatusUnimplemented,
	} {
		if isWireNotFound(mapWireError("kv.Get", st, errorBody(t, st, "key not found"))) {
			t.Errorf("%s was recognised as absence — note the payload deliberately "+
				"contains the old substring, which must no longer matter",
				wire.StatusName(st))
		}
	}
	if isWireNotFound(nil) {
		t.Error("nil is not absence")
	}
}

// TestMapUserNotFound_TypesTheIdTheCallerAsked is the #289 contract on QUIC:
// the typed error names the user the CALLER asked for, reconstructed from the
// call site's own argument and never parsed out of the server's message.
func TestMapUserNotFound_TypesTheIdTheCallerAsked(t *testing.T) {
	absent := mapWireError("user.Get", wire.StatusNotFound, errorBody(t, wire.StatusNotFound, "user: not found: 999999"))

	err := mapUserNotFound("GetUser", 999999, absent)
	var unf *transport.UserNotFoundError
	if !errors.As(err, &unf) {
		t.Fatalf("expected *transport.UserNotFoundError, got %T: %v", err, err)
	}
	if unf.ID != 999999 {
		t.Errorf("ID = %d, want 999999", unf.ID)
	}
	if unf.Lookup != "" {
		t.Errorf("Lookup = %q, want empty for an id lookup", unf.Lookup)
	}
}

// TestMapUserNotFound_LeavesEverythingElseAlone: a permission denial or a
// server fault at a user endpoint is not a missing user, and typing it as one
// would be the same fail-open the substring match was.
func TestMapUserNotFound_LeavesEverythingElseAlone(t *testing.T) {
	for _, st := range []uint16{
		wire.StatusError, wire.StatusPermissionDenied, wire.StatusUnavailable,
		wire.StatusInvalidArgument,
	} {
		err := mapUserNotFound("GetUser", 7, mapWireError("user.Get", st, errorBody(t, st, "user: not found: 7")))
		var unf *transport.UserNotFoundError
		if errors.As(err, &unf) {
			t.Errorf("%s was classified as a missing user", wire.StatusName(st))
		}
		var reqErr *transport.RequestError
		if !errors.As(err, &reqErr) {
			t.Errorf("%s: expected *transport.RequestError, got %T", wire.StatusName(st), err)
		}
	}
}

// TestMapUserLookupNotFound_TypesTheKeyTheCallerAsked covers the non-id
// lookups: the descriptor has to come from the call site, because the server's
// message is not a key.
func TestMapUserLookupNotFound_TypesTheKeyTheCallerAsked(t *testing.T) {
	absent := mapWireError("user.GetByUsername", wire.StatusNotFound, errorBody(t, wire.StatusNotFound, "user: not found: system/nobody"))

	err := mapUserLookupNotFound("GetUserByUsername", "system/nobody", absent)
	var unf *transport.UserNotFoundError
	if !errors.As(err, &unf) {
		t.Fatalf("expected *transport.UserNotFoundError, got %T: %v", err, err)
	}
	if unf.Lookup != "system/nobody" {
		t.Errorf("Lookup = %q, want %q", unf.Lookup, "system/nobody")
	}
	if unf.ID != 0 {
		t.Errorf("ID = %d, want 0 for a non-id lookup", unf.ID)
	}
}

// TestMapUserLookupNotFound_LeavesEverythingElseAlone.
func TestMapUserLookupNotFound_LeavesEverythingElseAlone(t *testing.T) {
	err := mapUserLookupNotFound("GetUserByEmail", "system/a@example.com",
		mapWireError("user.GetByEmail", wire.StatusPermissionDenied, errorBody(t, wire.StatusPermissionDenied, "no grant")))
	var unf *transport.UserNotFoundError
	if errors.As(err, &unf) {
		t.Fatal("a permission denial was classified as a missing user")
	}
}

// TestMapLeaseAbsent_SplitsTheTwoLeaseConditions. Both arrive as
// wire.StatusNotFound because neither this wire nor gRPC nor REST has an
// expired-specific sibling of not-found, so the message is what separates
// them — at a call site that already knows the subject is a lease.
func TestMapLeaseAbsent_SplitsTheTwoLeaseConditions(t *testing.T) {
	gone := mapWireError("lease.Revoke", wire.StatusNotFound, errorBody(t, wire.StatusNotFound, "service: lease not found: 42"))
	if err := mapLeaseAbsent(42, gone); err != nil {
		var notFound *transport.LeaseNotFoundError
		if !errors.As(err, &notFound) {
			t.Fatalf("expected *transport.LeaseNotFoundError, got %T: %v", err, err)
		}
		if notFound.LeaseID != 42 {
			t.Errorf("LeaseID = %d, want 42", notFound.LeaseID)
		}
	}

	expired := mapWireError("lease.Revoke", wire.StatusNotFound, errorBody(t, wire.StatusNotFound, "service: lease expired: 42"))
	var exp *transport.LeaseExpiredError
	if err := mapLeaseAbsent(42, expired); !errors.As(err, &exp) {
		t.Fatalf("expected *transport.LeaseExpiredError, got %T: %v", err, err)
	}
}

// TestMapLeaseAbsent_IgnoresEveryOtherStatus is the guard the gate change
// needed: before go-qrdb#437 this function keyed on wire.StatusError, which
// meant it had to read the message to avoid claiming every server fault at a
// lease call site. It keys on the status now, so a fault is excluded before
// the message is looked at.
func TestMapLeaseAbsent_IgnoresEveryOtherStatus(t *testing.T) {
	for _, st := range []uint16{
		wire.StatusError, wire.StatusUnavailable, wire.StatusPermissionDenied,
		wire.StatusInvalidArgument, wire.StatusUnimplemented,
	} {
		// The payload deliberately reads as a lease absence. Only the status
		// decides.
		err := mapLeaseAbsent(42, mapWireError("lease.Revoke", st, errorBody(t, st, "service: lease not found: 42")))
		var notFound *transport.LeaseNotFoundError
		var expired *transport.LeaseExpiredError
		if errors.As(err, &notFound) || errors.As(err, &expired) {
			t.Errorf("%s was classified as a lease absence", wire.StatusName(st))
		}
	}
}
