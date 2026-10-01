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

package protocol

import "errors"

// CapabilityError is implemented by an error that reports an ABSENT
// CAPABILITY rather than a fault: a verb this state machine has no handler
// for, or a subsystem it was not built or configured with. It is the
// declaration that the Apply classifier reads, and the ONLY way an error
// reaches a caller from inside Update without halting the state machine.
//
// Why an interface and not a list. The classifier used to name the two error
// types it would forgive, which made "this refusal is benign" a fact recorded
// somewhere OTHER than the error itself. A compiled-out subsystem then shipped
// a bare errors.New sentinel that meant exactly "this build cannot do that",
// was never added to the list, and took the fatal branch: one refused write
// halted the shard and every later linearizable read on it — on unrelated
// engines, on keys written and read successfully seconds earlier — waited out
// the caller's deadline and reported a consensus timeout (go-qrdb#520).
// Implementing this interface puts the declaration beside the refusal it
// describes, so a new subsystem declares its own and the classifier never
// changes.
//
// What this interface does NOT do is make the fatal branch opt-in. An error
// that says nothing still halts Apply, because the alternative — forgetting to
// mark a genuine storage fault and having it silently swallowed — is a
// diverged replica serving wrong data, which is worse than a stalled one.
// Refusal is declared; failure is the default.
type CapabilityError interface {
	error

	// Capability names the absent verb or subsystem, for the log line the
	// classifier writes. It is diagnostic, not dispatch: nothing branches on
	// the string.
	Capability() string
}

// AsCapabilityError reports whether err, or any error it wraps, declares
// itself an absent capability, returning it when so.
func AsCapabilityError(err error) (CapabilityError, bool) {
	var capErr CapabilityError
	if errors.As(err, &capErr) {
		return capErr, true
	}
	return nil, false
}

// RefusalError is implemented by an error that REFUSES a committed request on
// its merits — the request is malformed in a way every replica detects
// identically, such as a transaction naming an empty key — rather than
// reporting a fault. Like CapabilityError it is a declaration the Apply
// classifier reads: the refusal is encoded into the entry's result with
// EncodeAppError and consensus advances, instead of the error halting Apply on
// every replica.
//
// It is a separate declaration from CapabilityError because the two mean
// different things to a caller: "this node cannot do that" versus "that
// request is invalid anywhere".
type RefusalError interface {
	error

	// Refusal names what about the request was refused, for the log line the
	// classifier writes. It is diagnostic, not dispatch.
	Refusal() string
}

// AsRefusalError reports whether err, or any error it wraps, declares itself a
// refusal of the request on its merits, returning it when so.
func AsRefusalError(err error) (RefusalError, bool) {
	var refErr RefusalError
	if errors.As(err, &refErr) {
		return refErr, true
	}
	return nil, false
}
