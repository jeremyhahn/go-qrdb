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

package quic

import (
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestWireStatusByClass_IsTotal fails for a class with no row. A missing row
// reads as StatusOK, the zero value — a failure reported as a success.
func TestWireStatusByClass_IsTotal(t *testing.T) {
	for _, c := range errclass.All() {
		if _, ok := wireStatusByClass[c]; !ok {
			t.Errorf("class %s has no wire status", c)
		}
	}
	if len(wireStatusByClass) != len(errclass.All()) {
		t.Errorf("wireStatusByClass has %d rows for %d classes", len(wireStatusByClass), len(errclass.All()))
	}
}

// TestServiceErrorStatus_ReachesFamiliesTheOldLadderNeverListed pins the
// defect go-qrdb#557 removed from this transport: its own ladder never named
// the identity, tenant, PIN or credential families, so each of these reached a
// QUIC caller as StatusError while gRPC and REST answered it precisely.
func TestServiceErrorStatus_ReachesFamiliesTheOldLadderNeverListed(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want uint16
	}{
		{&user.UsernameExistsError{Username: "ada"}, wire.StatusAlreadyExists},
		{&user.ValidationError{Field: "email", Reason: "bad"}, wire.StatusInvalidArgument},
		{&user.ConcurrentUpdateError{}, wire.StatusAborted},
		{tenant.ErrTenantNotFound, wire.StatusNotFound},
		{fmt.Errorf("barrier.Unseal: %w", pin.ErrPINLocked), wire.StatusResourceExhausted},
		{pin.ErrStateCorrupted, wire.StatusDataLoss},
		{&service.RestoreScopeEmptyError{}, wire.StatusInvalidArgument},
		{&service.RestoreApplyError{Key: "k", Err: errors.New("disk")}, wire.StatusError},
	} {
		if got := ServiceErrorStatus(tc.err); got != tc.want {
			t.Errorf("ServiceErrorStatus(%T) = %s, want %s", tc.err, wire.StatusName(got), wire.StatusName(tc.want))
		}
	}
}

// TestServiceErrorStatus_ModuleStatusOnlyForUnclassified: a projected module's
// gRPC status is honoured, but never over qrdb's own classification.
func TestServiceErrorStatus_ModuleStatusOnlyForUnclassified(t *testing.T) {
	if got := ServiceErrorStatus(status.Error(codes.NotFound, "module row")); got != wire.StatusNotFound {
		t.Errorf("module NotFound = %s, want StatusNotFound", wire.StatusName(got))
	}
	if got := ServiceErrorStatus(nil); got != wire.StatusOK {
		t.Errorf("nil = %s, want StatusOK", wire.StatusName(got))
	}
}

// TestLocalErrorsDeclareTheirClass drives this package's own refusals through
// the real mapping.
func TestLocalErrorsDeclareTheirClass(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want uint16
	}{
		{&ServiceNotConfiguredError{Service: "backup"}, wire.StatusUnimplemented},
		{&CeremonyCANotConfiguredError{}, wire.StatusUnimplemented},
		{&InvalidArgumentError{Reason: "missing key"}, wire.StatusInvalidArgument},
		{&ClientCertRequiredError{}, wire.StatusUnauthenticated},
		{&UnmarshalError{Err: errors.New("proto")}, wire.StatusInvalidArgument},
		{&MarshalError{Err: errors.New("proto")}, wire.StatusError},
	} {
		if got := ServiceErrorStatus(tc.err); got != tc.want {
			t.Errorf("ServiceErrorStatus(%T) = %s, want %s", tc.err, wire.StatusName(got), wire.StatusName(tc.want))
		}
	}
}
