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

package service

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// A write the state machine refused for naming a lease it does not hold
// reaches the caller as the same typed not-found error the pre-proposal
// lease check returns, carrying the lease ID.
func TestCheckResultError_LeaseNotFoundRefusalIsTyped(t *testing.T) {
	err := checkResultError(sm.Result{Data: protocol.EncodeLeaseNotFoundError(9)})
	var nf *LeaseNotFoundError
	if !errors.As(err, &nf) || nf.LeaseID != 9 {
		t.Fatalf("checkResultError = %v, want *LeaseNotFoundError{LeaseID: 9}", err)
	}
}

// An ordinary invalid-kind refusal whose message happens to name a lease is
// not promoted to the typed error: the type comes from the marker, never
// from prose.
func TestCheckResultError_ProseNamingALeaseStaysRejected(t *testing.T) {
	err := checkResultError(sm.Result{Data: protocol.EncodeAppError(protocol.AppErrorInvalid, "lease not found: 9")})
	var nf *LeaseNotFoundError
	if errors.As(err, &nf) {
		t.Fatalf("prose was classified as a lease-not-found refusal: %v", err)
	}
	var rejected *CommandRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("checkResultError = %v, want *CommandRejectedError", err)
	}
}
