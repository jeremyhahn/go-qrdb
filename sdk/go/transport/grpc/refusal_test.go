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

package grpc

import (
	"errors"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
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

func TestMapGRPCError_adminCredentialRequiresCARefusalIsTyped(t *testing.T) {
	want := &ceremony.AdminCredentialRequiresCAError{Path: "csr-sign"}
	st, err := status.New(codes.FailedPrecondition, want.Error()).WithDetails(&errdetails.ErrorInfo{
		Domain: transport.RefusalDomain, Reason: ceremony.ReasonAdminCredentialRequiresCA, Metadata: want.Metadata(),
	})
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	got := mapGRPCError("CeremonyInitialize", st.Err())

	refusal, ok := errors.AsType[*ceremony.AdminCredentialRequiresCAError](got)
	if !ok || *refusal != *want {
		t.Fatalf("refusal = %+v, want %+v", refusal, want)
	}
	if reqErr, ok := errors.AsType[*transport.RequestError](got); !ok || reqErr.StatusCode != int(codes.FailedPrecondition) {
		t.Fatalf("err = %T, want a RequestError carrying FailedPrecondition", got)
	}
}

func TestMapGRPCError_failedPreconditionWithoutRefusalIsNotRequiresCA(t *testing.T) {
	got := mapGRPCError("CeremonyInitialize", status.Error(codes.FailedPrecondition, "no PIN backend"))
	if _, ok := errors.AsType[*ceremony.AdminCredentialRequiresCAError](got); ok {
		t.Fatalf("err = %v decoded a refusal the status does not carry", got)
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
