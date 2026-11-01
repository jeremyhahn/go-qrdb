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

package unix

import (
	"errors"
	"reflect"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestMapGRPCError_factorConditionRefusalIsTyped(t *testing.T) {
	want := &auth.FactorConditionFailedError{UserID: 42, Type: auth.AuthFactorMTLS, Condition: auth.FactorConditionNoActiveOfType, CurrentVersion: 1, ExistingFactorID: "f-1"}
	st, err := status.New(codes.AlreadyExists, want.Error()).WithDetails(&errdetails.ErrorInfo{
		Domain: transport.RefusalDomain, Reason: auth.ReasonFactorConditionFailed, Metadata: want.Metadata(),
	})
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	got := mapGRPCError("AddAuthFactor", st.Err())

	refusal, ok := errors.AsType[*auth.FactorConditionFailedError](got)
	if !ok || *refusal != *want {
		t.Fatalf("refusal = %+v, want %+v", refusal, want)
	}
	if reqErr, ok := errors.AsType[*transport.RequestError](got); !ok || reqErr.StatusCode != int(codes.AlreadyExists) {
		t.Fatalf("err = %T, want a RequestError carrying AlreadyExists", got)
	}
}

func TestMapGRPCError_alreadyExistsWithoutRefusalStaysPlain(t *testing.T) {
	got := mapGRPCError("AddAuthFactor", status.Error(codes.AlreadyExists, "factor exists"))
	if errors.Is(got, auth.ErrFactorConditionFailed) {
		t.Fatalf("err = %v decoded a refusal the status does not carry", got)
	}
	if reqErr, ok := errors.AsType[*transport.RequestError](got); !ok || reqErr.StatusCode != int(codes.AlreadyExists) {
		t.Fatalf("err = %T %v, want a RequestError carrying AlreadyExists", got, got)
	}
}

func TestMapGRPCError_userConcurrentUpdateRefusalIsTyped(t *testing.T) {
	want := &transport.UserConcurrentUpdateError{ID: 42}
	st, err := status.New(codes.Aborted, want.Error()).WithDetails(&errdetails.ErrorInfo{
		Domain: transport.RefusalDomain, Reason: transport.ReasonUserConcurrentUpdate, Metadata: want.Metadata(),
	})
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	got := mapGRPCError("UpdateUser", st.Err())

	refusal, ok := errors.AsType[*transport.UserConcurrentUpdateError](got)
	if !ok || *refusal != *want {
		t.Fatalf("refusal = %+v, want %+v", refusal, want)
	}
	if !dberrors.Is(got, dberrors.ErrConflict) || !dberrors.IsRetryable(got) {
		t.Fatalf("err = %v, want a retryable ErrConflict", got)
	}
	if reqErr, ok := errors.AsType[*transport.RequestError](got); !ok || reqErr.StatusCode != int(codes.Aborted) {
		t.Fatalf("err = %T, want a RequestError carrying Aborted", got)
	}
}

func TestMapGRPCError_abortedWithoutRefusalIsNotAConflict(t *testing.T) {
	got := mapGRPCError("UpdateUser", status.Error(codes.Aborted, "aborted"))
	if _, ok := errors.AsType[*transport.UserConcurrentUpdateError](got); ok {
		t.Fatalf("err = %v decoded a refusal the status does not carry", got)
	}
	if dberrors.IsRetryable(got) {
		t.Fatalf("err = %v is retryable without the refusal", got)
	}
}

// TestMapGRPCError_lockRefusalsAreTyped pins that each typed lock outcome
// survives the socket with its fields -- including the missing lock, whose
// NotFound code would otherwise be read as a missing key.
func TestMapGRPCError_lockRefusalsAreTyped(t *testing.T) {
	tests := []struct {
		name   string
		code   codes.Code
		reason string
		err    interface {
			error
			Metadata() map[string]string
		}
		match func(error) (error, bool)
	}{
		{
			name: "held", code: codes.Aborted, reason: lock.ReasonLockHeld,
			err: &lock.LockHeldError{Key: "k", HolderID: "h"},
			match: func(err error) (error, bool) {
				e, ok := errors.AsType[*lock.LockHeldError](err)
				return e, ok
			},
		},
		{
			name: "not holder", code: codes.PermissionDenied, reason: lock.ReasonNotLockHolder,
			err: &lock.NotLockHolderError{Key: "k", ExpectedToken: 3, ActualToken: 4},
			match: func(err error) (error, bool) {
				e, ok := errors.AsType[*lock.NotLockHolderError](err)
				return e, ok
			},
		},
		{
			name: "not found", code: codes.NotFound, reason: lock.ReasonLockNotFound,
			err: &lock.LockNotFoundError{Key: "k"},
			match: func(err error) (error, bool) {
				e, ok := errors.AsType[*lock.LockNotFoundError](err)
				return e, ok
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := status.New(tt.code, tt.err.Error()).WithDetails(&errdetails.ErrorInfo{
				Domain: transport.RefusalDomain, Reason: tt.reason, Metadata: tt.err.Metadata(),
			})
			if err != nil {
				t.Fatalf("WithDetails: %v", err)
			}
			got := mapGRPCError("LockRelease", st.Err())

			if typed, ok := tt.match(got); !ok || !reflect.DeepEqual(typed, tt.err) {
				t.Fatalf("mapGRPCError() = %#v, want %#v", got, tt.err)
			}
			if _, ok := errors.AsType[*transport.KeyNotFoundError](got); ok {
				t.Fatalf("mapGRPCError() = %v, a lock outcome read as a missing key", got)
			}
			if reqErr, ok := errors.AsType[*transport.RequestError](got); !ok || reqErr.StatusCode != int(tt.code) {
				t.Fatalf("err = %T, want a RequestError carrying %v", got, tt.code)
			}
		})
	}
}

// TestMapGRPCError_notFoundWithoutRefusalIsAMissingKey pins that NotFound
// with no structured refusal still maps to the missing key.
func TestMapGRPCError_notFoundWithoutRefusalIsAMissingKey(t *testing.T) {
	got := mapGRPCError("Get", status.Error(codes.NotFound, "k"))
	if e, ok := errors.AsType[*transport.KeyNotFoundError](got); !ok || e.Key != "k" {
		t.Fatalf("mapGRPCError() = %#v, want KeyNotFoundError{Key: k}", got)
	}
}
