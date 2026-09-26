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

package rest

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// go-qrdb#141 split capability refusals out of SMNotConfiguredError so gRPC
// could answer Unimplemented instead of Internal. REST already answered 501 for
// the old type, so the split silently demoted every REST capability refusal to
// 500 until this mapping was added — reintroducing on REST the exact
// healthy-node-as-server-fault behaviour the split was made to remove.
//
// Only the capability refusal answers 501. A closed subsystem is 503 and the
// refusals decided on the request's merits (an invalid lease TTL, a
// unique-index violation) are 4xx: go-qrdb#557 gave each its own type, and
// they are pinned below.
func TestMapHTTPError_CapabilityRefusalsAreNotImplemented(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{
			name: "capability refusal — an opcode this state machine lacks",
			err:  &service.CapabilityUnsupportedError{Message: "statemachine: unknown opcode: 67"},
		},
		{
			name: "capability refusal — an unwired subsystem",
			err:  &service.CapabilityUnsupportedError{Message: "statemachine: mvcc not configured"},
		},
		{
			name: "capability refusal wrapped by a caller",
			err: fmt.Errorf("kv: %w",
				&service.CapabilityUnsupportedError{Message: "statemachine: unknown opcode: 66"}),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapHTTPError(tc.err); got != http.StatusNotImplemented {
				t.Errorf("mapHTTPError = %d, want %d. A node that is healthy and simply "+
					"does not implement the operation must not be reported as a server "+
					"fault: a client retrying on 5xx would retry something that can "+
					"never succeed", got, http.StatusNotImplemented)
			}
		})
	}
}

// The refusals that are neither a capability nor a fault: the caller's input
// or a transient state of the node. Each must keep its own status rather than
// fall into 501 with the capability refusal they once shared a type with.
func TestMapHTTPError_NonCapabilityRefusalsKeepTheirStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"invalid input is a bad request", &service.CommandRejectedError{Message: "lease: invalid ttl"}, http.StatusBadRequest},
		{"a collision is a conflict", &service.CommandConflictError{Message: "unique index violation: email"}, http.StatusConflict},
		{"a closed subsystem is unavailable", &service.SubsystemClosedError{Message: "lease: lessor closed"}, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapHTTPError(tc.err); got != tc.want {
				t.Errorf("mapHTTPError(%T) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// The control. If everything mapped to 501 the assertions above would hold for
// the wrong reason, and a genuine server fault would be reported as an
// unsupported operation — telling a caller to stop retrying something that
// might well succeed on the next attempt.
func TestMapHTTPError_AGenuineFaultIsStillAServerError(t *testing.T) {
	got := mapHTTPError(&service.ProposeError{Err: fmt.Errorf("disk full")})
	if got == http.StatusNotImplemented {
		t.Fatal("a propose failure mapped to 501 Not Implemented; it is a server fault, " +
			"and reporting it as an unsupported operation tells the caller not to retry " +
			"something that may well succeed")
	}
	if got < 500 {
		t.Errorf("mapHTTPError = %d for a propose failure; want a 5xx", got)
	}
}
