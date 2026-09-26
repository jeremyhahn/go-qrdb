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

// Package errclass is the ONE place qrdb decides what kind of failure a typed
// error represents, independent of any transport (go-qrdb#557).
//
// Every wire transport used to answer that question itself: REST with twelve
// per-family status ladders, gRPC with nine per-family code ladders, QUIC with
// one ~80-clause ladder of its own. Nothing forced them to agree and they did
// not — a backup checksum mismatch was 409 over REST and Internal over gRPC, a
// system role's immutability was 403 over REST and FailedPrecondition over
// gRPC, and QUIC answered its generic error status for most of the identity,
// tenant, credential and ceremony families because its ladder never listed
// them.
//
// The split is the one the authentication classifier
// (pkg/auth/middleware.ClassifyAuthError) already proved out: this package
// owns WHAT KIND OF FAILURE AN ERROR IS; each transport owns HOW IT SAYS THAT
// on its own wire, through a total Class-to-code table of its own:
//
//	pkg/api/rest   Class -> HTTP status
//	pkg/api/grpc   Class -> gRPC code   (the unix socket and MCP reuse it)
//	pkg/api/quic   Class -> QUIC wire status
//
// The embedded transport returns the typed error itself, so it has nothing to
// render.
//
// A class is a MEANING, not a code. Most classes correspond one-to-one with a
// gRPC code; a few exist because HTTP draws a distinction a client acts on
// that gRPC does not — a body too large to send (413) is not a quota to wait
// out (429), although both are ResourceExhausted over gRPC. Each transport's
// projection is a function of the class alone, which is what makes "the same
// error means the same thing on every transport" a property of the table
// rather than of reviewers' attention.
package errclass

import "strconv"

// Class is what kind of failure an error represents.
type Class uint8

const (
	// Unclassified is an error the table does not recognise. Every transport
	// renders it as its internal-error answer, and QUIC and MCP consult a
	// projected module's own gRPC status first, since a capability module
	// answers in gRPC's vocabulary rather than in qrdb's typed errors.
	Unclassified Class = iota

	// InvalidArgument — the request itself is malformed. 400 / InvalidArgument.
	InvalidArgument

	// NotFound — the named resource does not exist. 404 / NotFound.
	NotFound

	// AlreadyExists — the name or identity the caller asked for is taken.
	// 409 / AlreadyExists.
	AlreadyExists

	// Aborted — the operation lost a concurrency race (an optimistic check,
	// a held lock, a conflicting transaction). Re-read and retry.
	// 409 / Aborted.
	Aborted

	// FailedPrecondition — the resource is not in a state this operation can
	// start from, and the caller can change that state.
	// 409 / FailedPrecondition.
	FailedPrecondition

	// Gone — the resource existed and has been permanently retired (revoked,
	// expired). 410 / FailedPrecondition.
	Gone

	// PreconditionFailed — a condition the operation depends on is unmet: a
	// consistency requirement, a PIN or strategy that was never set.
	// 412 / FailedPrecondition.
	PreconditionFailed

	// Unprocessable — the request is well-formed and cannot be carried out on
	// this configuration (a feature not enabled, a ceremony not configured, too
	// few officers). 422 / FailedPrecondition.
	Unprocessable

	// PermissionDenied — the caller is identified and may not do this.
	// 403 / PermissionDenied.
	PermissionDenied

	// Unauthenticated — the caller did not establish who it is, or the
	// credential it presented does not verify. 401 / Unauthenticated.
	Unauthenticated

	// ResourceExhausted — a quota, rate or lockout limit is reached. Back off
	// and retry. 429 / ResourceExhausted.
	ResourceExhausted

	// TooLarge — the request exceeds a size bound. Sending the same request
	// again will never work; sending less will. 413 / ResourceExhausted.
	TooLarge

	// OutOfRange — the request names a position past the retained range (a
	// compacted revision). 416 / OutOfRange.
	OutOfRange

	// Unimplemented — this node was not built or configured to perform the
	// operation. 501 / Unimplemented.
	Unimplemented

	// Unavailable — the node cannot answer right now (sealed, not the leader,
	// closing). Retry. 503 / Unavailable.
	Unavailable

	// Canceled — the operation was canceled before it completed. HTTP has no
	// status for this, so REST answers 500. 500 / Canceled.
	Canceled

	// DataLoss — secondary state may be inconsistent or persisted state is
	// corrupt; a blind retry is not the remedy. HTTP has no status for this,
	// so REST answers 500. 500 / DataLoss.
	DataLoss

	// Internal — a server fault the table has identified as such.
	// 500 / Internal.
	Internal

	// DeadlineExceeded — the caller's deadline passed before the operation
	// completed; it may or may not have taken effect. 504 / DeadlineExceeded.
	DeadlineExceeded

	// numClasses bounds the enumeration for All. It is not a class.
	numClasses
)

// classNames is Class's String table.
var classNames = map[Class]string{
	Unclassified:       "unclassified",
	InvalidArgument:    "invalid-argument",
	NotFound:           "not-found",
	AlreadyExists:      "already-exists",
	Aborted:            "aborted",
	FailedPrecondition: "failed-precondition",
	Gone:               "gone",
	PreconditionFailed: "precondition-failed",
	Unprocessable:      "unprocessable",
	PermissionDenied:   "permission-denied",
	Unauthenticated:    "unauthenticated",
	ResourceExhausted:  "resource-exhausted",
	TooLarge:           "too-large",
	OutOfRange:         "out-of-range",
	Unimplemented:      "unimplemented",
	Unavailable:        "unavailable",
	Canceled:           "canceled",
	DataLoss:           "data-loss",
	Internal:           "internal",
	DeadlineExceeded:   "deadline-exceeded",
}

// String returns the class's short name for logs and test failure messages.
func (c Class) String() string {
	if name, ok := classNames[c]; ok {
		return name
	}
	return "class(" + strconv.Itoa(int(c)) + ")"
}

// All returns every class, Unclassified included, in declaration order.
//
// A transport's projection table is required to be TOTAL over this set; each
// transport's tests assert it, so a class added here without a row in every
// transport fails the build rather than rendering as a zero-valued code.
func All() []Class {
	all := make([]Class, 0, numClasses)
	for c := Unclassified; c < numClasses; c++ {
		all = append(all, c)
	}
	return all
}

// Classed is implemented by an error that declares its own class.
//
// It exists for the transports' OWN error types — a REST body-size refusal, a
// gRPC or QUIC "service not configured" refusal — which live in packages this
// one cannot import without a cycle. Declaring the class beside the type is
// the same move gRPC makes with its GRPCStatus interface. A domain package
// must not implement it: domain errors are classified by the table in
// classify.go, where every other domain error's class is visible beside it.
type Classed interface {
	error

	// ErrorClass reports the error's class.
	ErrorClass() Class
}
