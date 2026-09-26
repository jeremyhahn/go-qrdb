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
// non-deterministic corruption (storage IO failure) is returned as a Go error.
//
// The prefix is multi-byte and NUL-led so it cannot collide with the single
// flag bytes other handlers place in Data (e.g. the transaction success flag,
// which is a one-byte 0x00/0x01).
var appErrorPrefix = []byte("\x00qrdb.apperr\x00")

// AppErrorKind is WHAT KIND of refusal an encoded application error is.
//
// The kind has to be carried, not inferred. Every kind arrives as an encoded
// message, and without a marker the only way to tell "this state machine was
// not built to do that" from "your TTL is invalid" from "that key is taken" is
// to match on message text — which is what go-qrdb#141 split capability
// refusals out to stop, and what go-qrdb#557 finished by giving every other
// refusal a kind of its own.
//
// sm.Result.Data never crosses a node boundary — each replica decodes the
// result its own state machine produced — so extending this encoding is
// internal, not a wire change.
type AppErrorKind uint8

const (
	// AppErrorCorrupt — the committed command could not be decoded, or a
	// payload carries the error prefix with no kind this code writes. Either
	// way the bytes are not what a writer of this version produced.
	AppErrorCorrupt AppErrorKind = iota

	// AppErrorCapability — this state machine was not built to do that: an
	// opcode it has no handler for, or a subsystem it was constructed without.
	AppErrorCapability

	// AppErrorInvalid — the command was understood and refused on its merits:
	// an invalid lease TTL, a malformed time-series point.
	AppErrorInvalid

	// AppErrorConflict — the command would violate a uniqueness constraint.
	AppErrorConflict

	// AppErrorUnavailable — the subsystem that would serve the command is
	// closed on this replica.
	AppErrorUnavailable

	// AppErrorFault — the subsystem failed while applying the command.
	AppErrorFault
)

// appErrorMarkers is each kind's marker, written after appErrorPrefix. A map
// rather than a switch: the kinds are values, the encoding is a lookup, and a
// kind added without a marker is a missing key the tests catch.
var appErrorMarkers = map[AppErrorKind][]byte{
	AppErrorCorrupt:     []byte("\x00corrupt\x00"),
	AppErrorCapability:  []byte("\x00cap\x00"),
	AppErrorInvalid:     []byte("\x00invalid\x00"),
	AppErrorConflict:    []byte("\x00conflict\x00"),
	AppErrorUnavailable: []byte("\x00unavail\x00"),
	AppErrorFault:       []byte("\x00fault\x00"),
}

// EncodeAppError encodes an application error of the given kind into a
// state-machine result payload. The returned bytes are placed in
// sm.Result.Data with a nil Go error so consensus continues to advance; the
// service layer decodes them with DecodeAppErrorKind and returns the typed
// error for the kind.
func EncodeAppError(kind AppErrorKind, msg string) []byte {
	marker := appErrorMarkers[kind]
	out := make([]byte, 0, len(appErrorPrefix)+len(marker)+len(msg))
	out = append(out, appErrorPrefix...)
	out = append(out, marker...)
	out = append(out, msg...)
	return out
}

// EncodeCapabilityError encodes an AppErrorCapability refusal. It is the
// encoder statemachine.ResolveApplyError reaches for, kept as a named entry
// point because an absent capability is the refusal most call sites raise.
func EncodeCapabilityError(msg string) []byte {
	return EncodeAppError(AppErrorCapability, msg)
}

// DecodeAppError reports whether data carries an encoded application error and,
// if so, returns the error message with its kind marker stripped. Ordinary
// result payloads (including the one-byte transaction success flag) do not
// carry the prefix and return ok false.
func DecodeAppError(data []byte) (msg string, ok bool) {
	msg, _, ok = DecodeAppErrorKind(data)
	return msg, ok
}

// DecodeAppErrorKind decodes an application error and its kind.
//
// A payload that carries the prefix but no marker this code writes decodes
// as AppErrorCorrupt with the whole body as its message: it is an error —
// reading it as a result would report a refusal as a success — and it is not
// one this version produced.
func DecodeAppErrorKind(data []byte) (msg string, kind AppErrorKind, ok bool) {
	if !bytes.HasPrefix(data, appErrorPrefix) {
		return "", AppErrorCorrupt, false
	}
	body := data[len(appErrorPrefix):]
	for k, marker := range appErrorMarkers {
		if bytes.HasPrefix(body, marker) {
			return string(body[len(marker):]), k, true
		}
	}
	return string(body), AppErrorCorrupt, true
}
