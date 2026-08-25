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

import "bytes"

// appErrorPrefix marks a state-machine result payload (sm.Result.Data) as an
// encoded APPLICATION error rather than an ordinary result payload.
//
// Raft semantics make this necessary: go-quicraft treats any non-nil error
// returned from a state machine's Update()/Apply() as a FATAL apply error that
// halts lastApplied and stalls every subsequent read and write on the group.
// Therefore a handler must NEVER return a Go error for an outcome that is a
// normal, deterministic consequence of valid (committed) client input — for
// example a unique-index violation, an invalid lease TTL, or an operation
// against a subsystem that is not configured on this state machine. Those
// outcomes are encoded into Data via EncodeAppError and surfaced to the client
// by the service layer (see service.checkResultError); only genuine
// non-deterministic corruption (failed decode of a committed entry, storage IO
// failure) is returned as a Go error.
//
// The prefix is multi-byte and NUL-led so it cannot collide with the single
// flag bytes other handlers place in Data (e.g. the transaction success flag,
// which is a one-byte 0x00/0x01).
var appErrorPrefix = []byte("\x00qrdb.apperr\x00")

// capabilityMarker distinguishes the subset of application errors that mean
// "this state machine was not built to do that" from the rest, which mean
// "your request was understood and refused on its merits" — a unique-index
// violation, an invalid lease TTL, a closed engine.
//
// The distinction has to be carried, not inferred. Both kinds arrive as the
// same encoded message, so without a marker the only way to tell a capability
// refusal from a constraint violation is to match on message text, and callers
// end up doing that (go-qrdb#141). It sits AFTER appErrorPrefix rather than
// replacing it so every existing DecodeAppError caller keeps working and keeps
// seeing the same message.
//
// sm.Result.Data never crosses a node boundary — each replica decodes the
// result its own state machine produced — so extending this encoding is
// internal, not a wire change.
var capabilityMarker = []byte("\x00cap\x00")

// EncodeAppError encodes an application-level error message into a state-machine
// result payload. The returned bytes are placed in sm.Result.Data with a nil
// Go error so consensus continues to advance; the service layer decodes them
// with DecodeAppError and returns a typed error to the client.
func EncodeAppError(msg string) []byte {
	out := make([]byte, 0, len(appErrorPrefix)+len(msg))
	out = append(out, appErrorPrefix...)
	out = append(out, msg...)
	return out
}

// EncodeCapabilityError encodes an application error that specifically reports
// an ABSENT CAPABILITY: an opcode this state machine has no handler for, or a
// subsystem it was not constructed with. The service layer turns it into a
// distinct typed error so a caller can tell "this node cannot do that" from
// "this node broke", which the generic encoding cannot express.
func EncodeCapabilityError(msg string) []byte {
	out := make([]byte, 0, len(appErrorPrefix)+len(capabilityMarker)+len(msg))
	out = append(out, appErrorPrefix...)
	out = append(out, capabilityMarker...)
	out = append(out, msg...)
	return out
}

// DecodeAppError reports whether data carries an encoded application error and,
// if so, returns the error message. Ordinary result payloads (including the
// one-byte transaction success flag) do not carry the prefix and return ok
// false.
//
// The capability marker is stripped, so a caller that only wants the message
// reads the same text whichever encoder produced it.
func DecodeAppError(data []byte) (msg string, ok bool) {
	msg, _, ok = DecodeAppErrorKind(data)
	return msg, ok
}

// DecodeAppErrorKind decodes an application error and additionally reports
// whether it is a capability refusal.
func DecodeAppErrorKind(data []byte) (msg string, capability bool, ok bool) {
	if !bytes.HasPrefix(data, appErrorPrefix) {
		return "", false, false
	}
	body := data[len(appErrorPrefix):]
	if bytes.HasPrefix(body, capabilityMarker) {
		return string(body[len(capabilityMarker):]), true, true
	}
	return string(body), false, true
}
