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

package statemachine

import (
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// InvalidOpcodeError and ServiceNotConfiguredError are the two absent-capability
// refusals this package raises. Both declare themselves to the Apply classifier
// by implementing protocol.CapabilityError; the assertions below are what fail
// at compile time if a Capability method is ever dropped, rather than at a
// halted shard (go-qrdb#520).
var (
	_ protocol.CapabilityError = (*InvalidOpcodeError)(nil)
	_ protocol.CapabilityError = (*ServiceNotConfiguredError)(nil)
)

// InvalidOpcodeError is returned when the state machine receives an entry
// with an unrecognized operation code.
type InvalidOpcodeError struct {
	Op byte
}

// Error returns a human-readable description of the unknown op code.
func (e *InvalidOpcodeError) Error() string {
	return "statemachine: unknown opcode: " + strconv.Itoa(int(e.Op))
}

// Capability names the absent verb, declaring this an application-level
// refusal rather than a fault. See protocol.CapabilityError.
func (e *InvalidOpcodeError) Capability() string {
	return "opcode " + strconv.Itoa(int(e.Op))
}

// IncompleteDataError is returned when a command payload is truncated
// or missing required fields.
type IncompleteDataError struct {
	Reason string
}

// Error returns a human-readable description of the data deficiency.
func (e *IncompleteDataError) Error() string {
	return "statemachine: incomplete data: " + e.Reason
}

// SnapshotError is returned when a snapshot save or recovery operation fails.
type SnapshotError struct {
	Op    string
	Inner error
}

// Error returns a human-readable description of the snapshot failure.
func (e *SnapshotError) Error() string {
	return "statemachine: snapshot " + e.Op + ": " + e.Inner.Error()
}

// Unwrap returns the underlying error for errors.Is/As support.
func (e *SnapshotError) Unwrap() error {
	return e.Inner
}

// StorageError is returned when the backing storage layer returns an error.
type StorageError struct {
	Op    string
	Inner error
}

// Error returns a human-readable description of the storage failure.
func (e *StorageError) Error() string {
	return "statemachine: storage " + e.Op + ": " + e.Inner.Error()
}

// Unwrap returns the underlying error for errors.Is/As support.
func (e *StorageError) Unwrap() error {
	return e.Inner
}

// ServiceNotConfiguredError is returned when a handler is invoked for a
// service subsystem (e.g. lease, index) that has not been wired into the
// state machine via its corresponding Option.
type ServiceNotConfiguredError struct {
	Service string
}

// Error returns a human-readable description of the missing service.
func (e *ServiceNotConfiguredError) Error() string {
	return "statemachine: " + e.Service + " not configured"
}

// Capability names the unwired subsystem, declaring this an application-level
// refusal rather than a fault. See protocol.CapabilityError.
func (e *ServiceNotConfiguredError) Capability() string {
	return e.Service
}
