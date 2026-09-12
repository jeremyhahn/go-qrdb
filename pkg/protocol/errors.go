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

// UnknownOpError is returned when a wire protocol message contains an
// unrecognized operation code.
type UnknownOpError struct {
	Op byte
}

// Error returns a human-readable description of the unknown op code.
func (e *UnknownOpError) Error() string {
	return "protocol: unknown op code: " + strconv.Itoa(int(e.Op))
}

// Capability names the absent verb, declaring this an application-level
// refusal rather than a fault so an unrecognised opcode is answered instead of
// halting the state machine that received it. See CapabilityError.
func (e *UnknownOpError) Capability() string {
	return "opcode " + strconv.Itoa(int(e.Op))
}

// UnknownOpError is an absent capability, not a fault.
var _ CapabilityError = (*UnknownOpError)(nil)
