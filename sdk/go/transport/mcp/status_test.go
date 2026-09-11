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

package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func payload(code wire.Code, message string) *wire.ErrorPayload {
	return &wire.ErrorPayload{Code: code, Status: code.String(), Message: message}
}

// TestMapToolError_TypedReconstitutions asserts the two codes this client
// turns into a typed SDK error rather than a generic request failure.
func TestMapToolError_TypedReconstitutions(t *testing.T) {
	t.Run("not found becomes KeyNotFoundError", func(t *testing.T) {
		err := mapToolError("qrdb_get", payload(wire.CodeNotFound, "key not found: k"))
		var knf *transport.KeyNotFoundError
		if !errors.As(err, &knf) {
			t.Fatalf("got %T: %v, want *transport.KeyNotFoundError", err, err)
		}
	})

	t.Run("unimplemented becomes CapabilityUnsupportedError", func(t *testing.T) {
		err := mapToolError("qrdb_tx_execute", payload(wire.CodeUnimplemented, "mvcc not configured"))
		var cue *transport.CapabilityUnsupportedError
		if !errors.As(err, &cue) {
			t.Fatalf("got %T: %v, want *transport.CapabilityUnsupportedError", err, err)
		}
		if cue.Message != "mvcc not configured" {
			t.Errorf("Message = %q", cue.Message)
		}
	})
}

// TestMapToolError_UntypedCodesKeepTheirNumber asserts every other code
// degrades to a generic request error that still SAYS which condition it was.
func TestMapToolError_UntypedCodesKeepTheirNumber(t *testing.T) {
	for _, tc := range []struct {
		code       wire.Code
		wantPrefix string
	}{
		{wire.CodeUnauthenticated, "unauthenticated: "},
		{wire.CodePermissionDenied, "permission denied: "},
		{wire.CodeUnavailable, "unavailable: "},
		{wire.CodeDeadlineExceeded, "deadline exceeded: "},
		{wire.CodeResourceExhausted, "resource exhausted: "},
		{wire.CodeFailedPrecondition, "failed precondition: "},
		{wire.CodeCanceled, "canceled: "},
		{wire.CodeInvalidArgument, "invalid argument: "},
		{wire.CodeAlreadyExists, "already exists: "},
		{wire.CodeAborted, "aborted: "},
		{wire.CodeOutOfRange, "out of range: "},
		{wire.CodeDataLoss, "data loss: "},
		{wire.CodeInternal, ""},
		{wire.CodeUnknown, ""},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			err := mapToolError("qrdb_put", payload(tc.code, "the server said so"))

			var req *transport.RequestError
			if !errors.As(err, &req) {
				t.Fatalf("got %T, want *transport.RequestError", err)
			}
			if req.StatusCode != int(tc.code) {
				t.Errorf("StatusCode = %d, want %d; a caller reading the number must "+
					"get the one the server sent", req.StatusCode, int(tc.code))
			}
			if !strings.Contains(err.Error(), tc.wantPrefix+"the server said so") {
				t.Errorf("message %q does not carry %q", err.Error(), tc.wantPrefix)
			}
		})
	}
}

// TestMapToolError_UnrecognizedCodeDegrades is the version-skew answer: a code
// this build does not know becomes the generic failure it would have been
// before that code existed, never a misclassification.
func TestMapToolError_UnrecognizedCodeDegrades(t *testing.T) {
	err := mapToolError("qrdb_put", payload(wire.Code(-32098), "from a later server"))

	var req *transport.RequestError
	if !errors.As(err, &req) {
		t.Fatalf("got %T, want *transport.RequestError", err)
	}
	if req.StatusCode != -32098 {
		t.Errorf("StatusCode = %d, want -32098", req.StatusCode)
	}

	var knf *transport.KeyNotFoundError
	var cue *transport.CapabilityUnsupportedError
	if errors.As(err, &knf) || errors.As(err, &cue) {
		t.Error("an unrecognised code was reconstituted as a typed condition")
	}
}

func TestMapUserNotFound(t *testing.T) {
	t.Run("a not-found names the caller's id", func(t *testing.T) {
		err := mapUserNotFound(42, &transport.KeyNotFoundError{Key: "user not found"})
		var unf *transport.UserNotFoundError
		if !errors.As(err, &unf) {
			t.Fatalf("got %T, want *transport.UserNotFoundError", err)
		}
		if unf.ID != 42 {
			t.Errorf("ID = %d, want 42 — the id the CALLER supplied, not one read "+
				"out of the server's message", unf.ID)
		}
	})

	t.Run("anything else is returned unchanged", func(t *testing.T) {
		original := &transport.RequestError{Method: "qrdb_user_get", StatusCode: -32006}
		if got := mapUserNotFound(42, original); got != original {
			t.Errorf("got %v, want the error unchanged; re-wrapping would nest one "+
				"request error inside another", got)
		}
	})

	t.Run("nil stays nil", func(t *testing.T) {
		if err := mapUserNotFound(42, nil); err != nil {
			t.Errorf("got %v, want nil", err)
		}
	})
}

func TestMapUserLookupNotFound(t *testing.T) {
	err := mapUserLookupNotFound("cert_spki/abc", &transport.KeyNotFoundError{Key: "nope"})
	var unf *transport.UserNotFoundError
	if !errors.As(err, &unf) {
		t.Fatalf("got %T, want *transport.UserNotFoundError", err)
	}
	if unf.Lookup != "cert_spki/abc" {
		t.Errorf("Lookup = %q, want the descriptor the caller supplied", unf.Lookup)
	}
	if unf.ID != 0 {
		t.Errorf("ID = %d, want 0 for a non-numeric lookup", unf.ID)
	}
}

// TestMapLeaseAbsent is the test that matters for the lease family, because
// the expired/absent split is still decided by the message — BEHIND the code
// gate, exactly as the gRPC transport's mapLeaseAbsent does it.
func TestMapLeaseAbsent(t *testing.T) {
	t.Run("an expired lease", func(t *testing.T) {
		err := mapLeaseAbsent(7, &transport.KeyNotFoundError{Key: "lease expired: 7"})
		var lee *transport.LeaseExpiredError
		if !errors.As(err, &lee) {
			t.Fatalf("got %T, want *transport.LeaseExpiredError", err)
		}
		if lee.LeaseID != 7 {
			t.Errorf("LeaseID = %d, want 7", lee.LeaseID)
		}
	})

	t.Run("a lease that never existed", func(t *testing.T) {
		err := mapLeaseAbsent(7, &transport.KeyNotFoundError{Key: "lease not found: 7"})
		var lnf *transport.LeaseNotFoundError
		if !errors.As(err, &lnf) {
			t.Fatalf("got %T, want *transport.LeaseNotFoundError", err)
		}
		if lnf.LeaseID != 7 {
			t.Errorf("LeaseID = %d, want 7", lnf.LeaseID)
		}
	})

	// THE regression this gate exists for. Before go-qrdb#438 the message was
	// the only signal, so a SERVER FAULT whose prose happened to carry the
	// words was reported as a gone lease. It is now excluded by the code
	// before the message is looked at at all.
	t.Run("a server fault carrying the words is not a lease error", func(t *testing.T) {
		fault := mapToolError("qrdb_lease_ttl",
			payload(wire.CodeInternal, "raft apply failed: lease expired while committing"))

		err := mapLeaseAbsent(7, fault)
		var lee *transport.LeaseExpiredError
		var lnf *transport.LeaseNotFoundError
		if errors.As(err, &lee) || errors.As(err, &lnf) {
			t.Fatalf("a fault was reclassified as a lease condition (%T). The code "+
				"gate in mapLeaseAbsent is what stops that; do not remove it", err)
		}
		if err != fault {
			t.Errorf("got %v, want the fault unchanged", err)
		}
	})

	t.Run("nil stays nil", func(t *testing.T) {
		if err := mapLeaseAbsent(7, nil); err != nil {
			t.Errorf("got %v, want nil", err)
		}
	})
}

// TestLeaseExpiredMessage covers the predicate directly, including the nil
// case a caller can reach through mapLeaseAbsent only when the gate is bypassed.
func TestLeaseExpiredMessage(t *testing.T) {
	if leaseExpiredMessage(nil) {
		t.Error("nil reported as expired")
	}
	if !leaseExpiredMessage(errors.New("Lease Expired: 3")) {
		t.Error("the predicate is case-sensitive; it must not be")
	}
	if leaseExpiredMessage(errors.New("lease not found: 3")) {
		t.Error("an absent lease reported as expired")
	}
}

// ---------------------------------------------------------------------------
// End to end, against the real server
//
// Everything above drives the mappers directly. These drive the REAL
// pkg/api/mcp server, its real classification middleware and the real mcp-go
// transport, because the mappers agreeing with themselves proves nothing about
// what actually crosses the wire.
// ---------------------------------------------------------------------------

// TestE2E_ClosedStoreIsUnavailable is go-qrdb#371's MCP cell.
//
// docs/storage-engines/api.md recorded this row as "a generic tool failure —
// the classification is not carried yet". It carries it now:
// storage.ErrStorageClosed is classified Unavailable by the same clause the
// gRPC transport runs, and reaches the client as wire.CodeUnavailable — the
// same retryable family gRPC reports as codes.Unavailable and REST as 503.
func TestE2E_ClosedStoreIsUnavailable(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.putErr = &service.ProposeError{Err: storage.ErrStorageClosed}
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
	defer cleanup()

	err := tr.Put(context.Background(), "k", []byte("v"))
	if err == nil {
		t.Fatal("a write against a closed store succeeded")
	}

	var req *transport.RequestError
	if !errors.As(err, &req) {
		t.Fatalf("got %T: %v, want *transport.RequestError", err, err)
	}
	if req.StatusCode != int(wire.CodeUnavailable) {
		t.Errorf("StatusCode = %d (%s), want %d (UNAVAILABLE). A caller cannot tell "+
			"a node going away underneath it from a server fault without this",
			req.StatusCode, wire.Code(req.StatusCode), int(wire.CodeUnavailable))
	}
}

// TestE2E_MissingKeyIsTypedByCode proves the not-found path end to end: the
// service returns a typed error, the server classifies it, the client
// reconstitutes KeyNotFoundError around the key THE CALLER asked for.
func TestE2E_MissingKeyIsTypedByCode(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.getErr = &service.KeyNotFoundError{Key: "whatever-the-server-called-it"}
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
	defer cleanup()

	_, err := tr.Get(context.Background(), "the-key-i-asked-for")
	if err == nil {
		t.Fatal("reading a missing key succeeded")
	}

	var knf *transport.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("got %T: %v, want *transport.KeyNotFoundError", err, err)
	}
	if knf.Key != "the-key-i-asked-for" {
		t.Errorf("Key = %q, want the key the CALLER asked for. Taking it from the "+
			"server's message is the classification this change removed", knf.Key)
	}
}

// TestE2E_UntypedServiceErrorIsNotAbsence is the negative that makes the two
// above worth running.
//
// The service returns an UNTYPED error whose prose contains "not found". The
// server has nothing to classify it as, so it says INTERNAL, and the client
// must report a failure — not a determination that the key does not exist.
// The deleted isNotFoundError answered the opposite.
func TestE2E_UntypedServiceErrorIsNotAbsence(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.getErr = errors.New("upstream shard not found in routing table")
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
	defer cleanup()

	_, err := tr.Get(context.Background(), "k")
	if err == nil {
		t.Fatal("expected an error")
	}

	var knf *transport.KeyNotFoundError
	if errors.As(err, &knf) {
		t.Fatalf("a routing failure was classified as a missing key (%v). That is "+
			"fail-open absence — the substring match is back", err)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("this cell only tests what it is meant to while the message "+
			"contains the needle; got %q", err)
	}
}
