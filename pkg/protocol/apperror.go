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

// DecodeAppError reports whether data carries an encoded application error and,
// if so, returns the error message. Ordinary result payloads (including the
// one-byte transaction success flag) do not carry the prefix and return ok
// false.
func DecodeAppError(data []byte) (msg string, ok bool) {
	if !bytes.HasPrefix(data, appErrorPrefix) {
		return "", false
	}
	return string(data[len(appErrorPrefix):]), true
}
