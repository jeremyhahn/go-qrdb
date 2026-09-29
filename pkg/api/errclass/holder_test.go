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

package errclass

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestClassify_HolderConditions pins the class of each share-holder, quorum
// and envelope condition, which every transport renders from this table.
func TestClassify_HolderConditions(t *testing.T) {
	cases := []struct {
		err  error
		want Class
	}{
		// A holder whose factor proof failed is refused, not merely
		// unauthenticated: the holder row wins over the factor's own error.
		{&seal.HolderAuthenticationError{Holder: "a", Err: &authfactors.VerificationFailedError{}}, PermissionDenied},
		{&seal.HolderAlreadySubmittedError{Holder: "a"}, AlreadyExists},
		// A rekey that would break the quorum is a precondition, not the
		// invalid threshold it also matches.
		{&seal.RevocationBreaksQuorumError{Remaining: 1, Threshold: 2}, FailedPrecondition},
		{seal.ErrQuorumRoundInProgress, AlreadyExists},
		{seal.ErrShamirInitializeRequiresHolders, FailedPrecondition},
		{seal.ErrShamirUnsealRequiresQuorum, Unprocessable},
		{ceremony.ErrSelfEnrolment, PermissionDenied},
		{ceremony.ErrNoPendingShare, NotFound},
		{ceremony.ErrShareExpired, Gone},
		{ceremony.ErrTooManyPendingChallenges, ResourceExhausted},
		{ceremony.ErrNoOfficers, InvalidArgument},
		{ceremony.ErrInvalidThreshold, InvalidArgument},
		{ceremony.ErrAlreadyInitialized, FailedPrecondition},
		{&ceremony.InvalidHolderNameError{Name: "a/b"}, InvalidArgument},
		{&keydelivery.UnsupportedKEMError{Name: "x"}, InvalidArgument},
		{&keydelivery.SuiteNotApprovedError{Name: "x"}, InvalidArgument},
		{&authfactors.FactorNotFoundError{Name: "piv"}, NotFound},
		{pin.ErrBackendNotConfigured, PreconditionFailed},
		{service.ErrCeremonyNotConfigured, Unprocessable},
		{service.ErrNilRequest, InvalidArgument},
	}
	for _, tc := range cases {
		if got := Classify(tc.err); got != tc.want {
			t.Errorf("Classify(%T %v) = %v, want %v", tc.err, tc.err, got, tc.want)
		}
	}
}

// TestClassify_HolderRowsDoNotClaimOtherErrors confirms an unrelated error
// stays unclassified rather than falling into a holder row.
func TestClassify_HolderRowsDoNotClaimOtherErrors(t *testing.T) {
	if got := Classify(errors.New("unrelated")); got != Unclassified {
		t.Fatalf("Classify(unrelated) = %v, want Unclassified", got)
	}
}
