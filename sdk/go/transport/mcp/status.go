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
	"errors"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// typedToolErrors are the codes this client reconstitutes into a TYPED SDK
// error rather than into a generic request error.
//
// A map of constructors rather than a switch: adding a typed reconstitution is
// one row, and every code NOT listed falls through to the generic error by
// construction — which is exactly the behaviour a client built before a code
// existed has to show when a newer server sends it.
//
// Both entries mirror the QUIC transport's typedWireErrors, which mirrors the
// gRPC transport's mapGRPCError, including its guess: a bare not-found says
// the subject is absent but not WHICH subject, so the shared mapper
// reconstitutes the key flavour and the call-site mappers override it where
// the subject is known.
var typedToolErrors = map[wire.Code]func(msg string) error{
	// The node is healthy and does not implement this verb — an opcode its
	// state machine has no handler for, a subsystem it was constructed
	// without, or a tool this server build does not register. Reconstituting
	// the typed error here is what lets a consumer degrade gracefully over MCP
	// exactly as it can embedded, over gRPC, over unix and over QUIC, instead
	// of matching message text (go-qrdb#141).
	//
	// It does NOT collect the refusals decided on a REQUEST's merits — an
	// invalid lease TTL, a unique-index violation. Since go-qrdb#557 those
	// have types of their own that the server classifies as InvalidArgument
	// and AlreadyExists, so on every transport CodeUnimplemented means the
	// capability refusal and nothing else.
	wire.CodeUnimplemented: func(msg string) error {
		return &transport.CapabilityUnsupportedError{Message: msg}
	},

	// This case REPLACES strings.Contains(msg, "not found"), which ran on
	// every tool failure because MCP had no code at all and the message was
	// the only signal left. That is the fail-open classification go-qrdb#289
	// exists to delete: a transport failure whose text happened to contain the
	// phrase read as absence. The server now says which condition it is and
	// the client no longer reads the prose (go-qrdb#438).
	wire.CodeNotFound: func(msg string) error {
		return &transport.KeyNotFoundError{Key: msg}
	},
}

// toolCodePrefixes label the generic request error with the condition the
// server named, so a client log says which of the seventeen it was instead of
// repeating the server's sentence with no classification attached.
//
// CodeInternal and CodeUnknown are absent deliberately: an unclassified fault
// is what a bare request error already means, and prefixing it would add a
// word without adding a fact.
var toolCodePrefixes = map[wire.Code]string{
	wire.CodeUnauthenticated:    "unauthenticated: ",
	wire.CodePermissionDenied:   "permission denied: ",
	wire.CodeUnavailable:        "unavailable: ",
	wire.CodeDeadlineExceeded:   "deadline exceeded: ",
	wire.CodeResourceExhausted:  "resource exhausted: ",
	wire.CodeFailedPrecondition: "failed precondition: ",
	wire.CodeCanceled:           "canceled: ",
	wire.CodeInvalidArgument:    "invalid argument: ",
	wire.CodeAlreadyExists:      "already exists: ",
	wire.CodeAborted:            "aborted: ",
	wire.CodeOutOfRange:         "out of range: ",
	wire.CodeDataLoss:           "data loss: ",
}

// mapToolError translates a coded tool failure into the appropriate SDK
// transport error type.
//
// A code this build does not recognise — a newer server's — keeps its number
// on the RequestError and its message unprefixed, so it degrades to the
// generic failure this client would have reported anyway and never to a
// misclassification.
func mapToolError(tool string, payload *wire.ErrorPayload) error {
	// Unavailable alone also means a draining node; the SEALED reason is what
	// makes it the sealed node's refusal.
	if payload.Code == wire.CodeUnavailable && payload.Reason == sealedwindow.ReasonSealed {
		return &transport.BarrierSealedError{Message: payload.Message}
	}
	typed := transport.RefusalError(payload.Reason, payload.Metadata)
	if build, ok := typedToolErrors[payload.Code]; ok && typed == nil {
		return build(payload.Message)
	}
	if typed == nil {
		typed = &toolResultError{
			Tool:    tool,
			Message: toolCodePrefixes[payload.Code] + payload.Message,
		}
	}

	return &transport.RequestError{
		Method:     tool,
		StatusCode: int(payload.Code),
		Err:        typed,
		RetryAfter: time.Duration(payload.RetryAfterSeconds) * time.Second,
		Reason:     payload.Reason,
		Metadata:   payload.Metadata,
	}
}

// isToolNotFound reports whether err is the not-found the shared mapper
// reconstitutes from wire.CodeNotFound.
//
// The call-site mappers ask this and then rebuild the error around the subject
// THEY named, so the typed error carries which user or which lease was missing
// rather than a descriptor parsed back out of the server's prose. It is the
// exact counterpart of the QUIC transport's isWireNotFound, and it REPLACES
// isNotFoundError, whose whole implementation was
// strings.Contains(strings.ToLower(err.Error()), "not found").
func isToolNotFound(err error) bool {
	var knf *transport.KeyNotFoundError
	return errors.As(err, &knf)
}

// isToolOutOfRange reports whether err is the request error the shared mapper
// builds from wire.CodeOutOfRange, the server's class for a revision below
// the compaction floor and nothing else, as 416 is on REST. It is the exact
// counterpart of the QUIC transport's isWireOutOfRange.
func isToolOutOfRange(err error) bool {
	var re *transport.RequestError
	return errors.As(err, &re) && re.StatusCode == int(wire.CodeOutOfRange)
}

// mapUserNotFound rebuilds a bare not-found as the typed user-not-found,
// naming the id the CALLER asked for.
//
// The server does not say which subject was missing and is not asked to: every
// transport answers one not-found code for a missing user and a missing role
// alike, and the id here is the one this call site supplied. That is what
// makes the mapping honest — see sdk/go/conformance's
// TestUserNotFound_AmbiguousMethods for the methods where no call site knows
// the subject and none of them may therefore be typed.
func mapUserNotFound(id uint64, err error) error {
	if isToolNotFound(err) {
		return &transport.UserNotFoundError{ID: id}
	}
	return err
}

// mapUserLookupNotFound is mapUserNotFound for a lookup by a non-numeric key.
//
// Neither mapper takes the SDK method name its QUIC twin does, because neither
// needs it: a failure that is not a not-found is returned UNCHANGED, already
// wrapped by callTool in a transport.RequestError naming the tool. Taking the
// name to re-wrap would nest one request error inside another and say the same
// thing twice.
func mapUserLookupNotFound(lookup string, err error) error {
	if isToolNotFound(err) {
		return &transport.UserNotFoundError{Lookup: lookup}
	}
	return err
}

// mapLeaseAbsent rebuilds a bare not-found as the typed lease error, naming
// the lease the CALLER asked about.
//
// The expired/absent split is a message test BEHIND a code gate, which is
// exactly how the gRPC transport's mapLeaseAbsent does it and for the same
// reason: no transport in this repository has an expired-specific code —
// gRPC maps both service.LeaseNotFoundError and service.LeaseExpiredError to
// codes.NotFound, so MCP and QUIC render both as their one not-found — and the
// two are separable only by what the server said.
//
// That is NOT the classification go-qrdb#438 deleted, and the difference is
// the gate. Before this change ANY tool failure whose prose contained "lease
// expired" became a LeaseExpiredError, so a broken node could be reported as a
// gone lease. Now a failure has to be a not-found FIRST, at a call site that
// already knows its subject is a lease, before the message is looked at at
// all: a server fault is excluded by the code before the prose is read.
func mapLeaseAbsent(leaseID int64, err error) error {
	if !isToolNotFound(err) {
		return err
	}
	if leaseExpiredMessage(err) {
		return &transport.LeaseExpiredError{LeaseID: leaseID}
	}
	return &transport.LeaseNotFoundError{LeaseID: leaseID}
}

// mapOpsNotFoundError is mapLeaseAbsent for ExecuteTransaction and
// ExecuteBatch: checkResultError's only not-found-classed outcome for a
// transaction or batch command is a write naming a lease the state machine
// does not hold, so a tool not-found here is unambiguous too, and the
// offending lease ID is read back out of the SAME ops the caller sent
// (success ops before failure ops) rather than guessed from the server's
// message text. See sdk/go/transport/grpc's copy of this function for the
// full rationale, including the known limitation when success and failure
// branches each name a different lease.
func mapOpsNotFoundError(ops []transport.Op, err error) error {
	if !isToolNotFound(err) {
		return err
	}
	if id, found := transport.FirstLeaseID(ops); found {
		return &transport.LeaseNotFoundError{LeaseID: id}
	}
	return err
}
