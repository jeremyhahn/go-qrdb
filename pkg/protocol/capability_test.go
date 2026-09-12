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

import (
	"errors"
	"fmt"
	"testing"
)

// subsystemStubError is a refusal declared by a package that this one has never
// heard of — the shape of every future compiled-out subsystem. It exists to
// prove the predicate is OPEN: nothing in pkg/protocol names this type, and it
// is still classified as a refusal.
type subsystemStubError struct{ name string }

func (e *subsystemStubError) Error() string      { return e.name + " support not compiled" }
func (e *subsystemStubError) Capability() string { return e.name }

// TestAsCapabilityErrorRecognisesAnUnknownDeclaringType is the whole point of
// replacing the allowlist: a refusal type the classifier has never been told
// about is still a refusal, because the declaration travels with the error.
func TestAsCapabilityErrorRecognisesAnUnknownDeclaringType(t *testing.T) {
	capErr, ok := AsCapabilityError(&subsystemStubError{name: "widgets"})
	if !ok {
		t.Fatal("a declaring error was not recognised; the predicate is not open")
	}
	if got := capErr.Capability(); got != "widgets" {
		t.Errorf("Capability() = %q, want %q", got, "widgets")
	}
}

// TestAsCapabilityErrorReadsThroughWrapping proves a handler may add context
// to a refusal without silently converting it into a shard-killing fault.
func TestAsCapabilityErrorReadsThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("ts-write: %w", &subsystemStubError{name: "timeseries"})
	capErr, ok := AsCapabilityError(wrapped)
	if !ok {
		t.Fatal("a wrapped refusal stopped being a refusal")
	}
	if got := capErr.Capability(); got != "timeseries" {
		t.Errorf("Capability() = %q, want %q", got, "timeseries")
	}
}

// TestAsCapabilityErrorRejectsASilentError is the property that must survive:
// an error that says nothing is a FAULT. Making the fatal branch opt-in would
// turn a forgotten declaration into a diverged replica serving wrong data,
// which is worse than a stalled one.
func TestAsCapabilityErrorRejectsASilentError(t *testing.T) {
	for _, err := range []error{
		errors.New("disk I/O error"),
		&InvalidCommandError{Reason: "truncated"},
		fmt.Errorf("wrapped: %w", errors.New("corrupt sstable")),
	} {
		if _, ok := AsCapabilityError(err); ok {
			t.Errorf("AsCapabilityError(%v) = true; a silent error must stay fatal", err)
		}
	}
}

// TestAsCapabilityErrorOnNil keeps the nil case explicit rather than relying
// on errors.As's behaviour being remembered.
func TestAsCapabilityErrorOnNil(t *testing.T) {
	if capErr, ok := AsCapabilityError(nil); ok || capErr != nil {
		t.Errorf("AsCapabilityError(nil) = (%v, %v), want (nil, false)", capErr, ok)
	}
}

// TestUnknownOpErrorDeclaresItsCapability pins the message the classifier logs
// for the opcode this repository's engines refuse most often.
func TestUnknownOpErrorDeclaresItsCapability(t *testing.T) {
	capErr, ok := AsCapabilityError(&UnknownOpError{Op: 254})
	if !ok {
		t.Fatal("UnknownOpError is not recognised as an absent capability")
	}
	if got, want := capErr.Capability(), "opcode 254"; got != want {
		t.Errorf("Capability() = %q, want %q", got, want)
	}
}
