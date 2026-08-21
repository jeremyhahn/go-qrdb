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

import "strconv"

// InvalidCommandError is returned when a wire protocol message cannot
// be decoded due to malformed or truncated data.
type InvalidCommandError struct {
	Reason string
}

// Error returns a human-readable description of the decoding failure.
func (e *InvalidCommandError) Error() string {
	return "protocol: invalid command: " + e.Reason
}

// MaxEncodableKeyLength is the largest key a two-byte length field can
// describe. It matches go-quicraft's kv.MaxKeyLength, because both protocols
// frame a key the same way.
const MaxEncodableKeyLength = 65535

// KeyTooLongError is returned when a key cannot be described by the length
// field that frames it.
//
// The narrow cast this prevents is not a truncation the caller notices: a
// 65,537-byte key writes a length of 1, so the DECODER reads a one-byte key
// and folds the remaining 65,536 bytes into the next field. On a guarded
// delete that means comparing and deleting a DIFFERENT, shorter key than the
// caller named — silent wrong-record targeting inside a replicated state
// machine, from input a caller can craft.
type KeyTooLongError struct {
	// Length is the offending key length in bytes.
	Length int
	// Max is the largest length the field can describe.
	Max int
}

func (e *KeyTooLongError) Error() string {
	return "protocol: key too long: " + strconv.Itoa(e.Length) +
		" bytes exceeds the " + strconv.Itoa(e.Max) + "-byte encodable maximum"
}

// UnknownOpError is returned when a wire protocol message contains an
// unrecognized operation code.
type UnknownOpError struct {
	Op byte
}

// Error returns a human-readable description of the unknown op code.
func (e *UnknownOpError) Error() string {
	return "protocol: unknown op code: " + strconv.Itoa(int(e.Op))
}
