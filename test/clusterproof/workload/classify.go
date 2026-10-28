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

//go:build clusterproof

package workload

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
)

// Classifier decides what an operation's error means for the history. It is
// a seam: a run against a transport whose errors say more can pass a sharper
// one, but it must never call a write fail unless the write certainly did not
// apply.
type Classifier func(kind history.Kind, err error) history.Outcome

// definiteWriteRejections are errors a node returns before the write reaches
// the log: the request was refused outright, so it cannot apply later.
var definiteWriteRejections = []func(error) bool{
	isA[*transport.CapabilityUnsupportedError],
	isA[*transport.NotImplementedError],
	isA[*transport.InvalidWriteModeError],
	isA[*transport.ConfigError],
	grpcCodeIn(definiteGRPCRejections),
}

// definiteGRPCRejections are the gRPC status codes a node answers only for a
// request it refused outright: malformed, unauthenticated, not permitted, or
// over a quota or rate limit (pkg/api/grpc/error_status.go); each means the
// write had no effect. Unavailable, DeadlineExceeded, Aborted and Unknown are
// absent on purpose: gRPC returns Unavailable when a connection breaks after
// the request was written as well as before it, so a write that meets one
// stays indeterminate. A sealed node's refusal stays indeterminate too: a
// barrier can seal between a write's commit and its answer.
var definiteGRPCRejections = map[codes.Code]bool{
	codes.InvalidArgument:   true,
	codes.Unauthenticated:   true,
	codes.PermissionDenied:  true,
	codes.ResourceExhausted: true,
}

// grpcCodeIn reports whether an error carries a gRPC status whose code is in
// set. An error from a transport other than gRPC carries no status and never
// matches.
func grpcCodeIn(set map[codes.Code]bool) func(error) bool {
	return func(err error) bool {
		st, ok := grpcStatus(err)
		return ok && set[st.Code()]
	}
}

// grpcStatus returns the gRPC status err wraps, if it wraps one.
func grpcStatus(err error) (*status.Status, bool) {
	var carrier interface{ GRPCStatus() *status.Status }
	if !errors.As(err, &carrier) {
		return nil, false
	}
	return carrier.GRPCStatus(), true
}

// absentOK names the operations for which KeyNotFound is a successful answer.
var absentOK = map[history.Kind]bool{
	history.KindGet:    true,
	history.KindDelete: true,
}

// DefaultClassifier is the conservative classifier.
//
//   - No error is ok, and so is KeyNotFound on a get or a delete.
//   - A read that errs is fail: it changed nothing, so it constrains nothing.
//   - A write that errs is indeterminate -- a timeout, a dropped connection,
//     even NotLeader (the entry may already sit in the old leader's log) --
//     unless the error is a definite rejection that never reached the log.
func DefaultClassifier(kind history.Kind, err error) history.Outcome {
	if err == nil {
		return history.OutcomeOK
	}
	if absentOK[kind] && isA[*transport.KeyNotFoundError](err) {
		return history.OutcomeOK
	}
	if kind == history.KindGet {
		return history.OutcomeFail
	}
	for _, rejected := range definiteWriteRejections {
		if rejected(err) {
			return history.OutcomeFail
		}
	}
	return history.OutcomeIndeterminate
}

// isA reports whether err wraps an error of type T.
func isA[T error](err error) bool {
	var target T
	return errors.As(err, &target)
}

// connectionLost reports whether err means the connection itself is gone, so
// the client should drop it and dial again. A gRPC Unavailable counts: the
// node is down or unreachable, and the redial's health check, which records
// nothing, finds out when it is back instead of one recorded operation per
// retry.
func connectionLost(err error) bool {
	if isA[*transport.ConnectionError](err) || isA[*transport.ClosedError](err) {
		return true
	}
	st, ok := grpcStatus(err)
	return ok && st.Code() == codes.Unavailable
}
