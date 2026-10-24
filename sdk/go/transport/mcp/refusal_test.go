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

package mcp

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestMapToolError_factorConditionRefusalIsTyped(t *testing.T) {
	want := &auth.FactorConditionFailedError{UserID: 42, Type: auth.AuthFactorFIDO2, Condition: auth.FactorConditionExpectedVersion, ExpectedVersion: 0, CurrentVersion: 2}
	got := mapToolError("qrdb_user_authfactor_add", &wire.ErrorPayload{
		Code: wire.CodeAlreadyExists, Message: want.Error(),
		Reason: auth.ReasonFactorConditionFailed, Metadata: want.Metadata(),
	})

	refusal, ok := errors.AsType[*auth.FactorConditionFailedError](got)
	if !ok || *refusal != *want {
		t.Fatalf("refusal = %+v, want %+v", refusal, want)
	}
	if reqErr, ok := errors.AsType[*transport.RequestError](got); !ok || reqErr.StatusCode != int(wire.CodeAlreadyExists) {
		t.Fatalf("err = %T, want a RequestError carrying the already-exists code", got)
	}
}

func TestMapToolError_noReasonKeepsTheCodeMapping(t *testing.T) {
	got := mapToolError("qrdb_user_authfactor_add", &wire.ErrorPayload{Code: wire.CodeAlreadyExists, Message: "factor exists"})
	if errors.Is(got, auth.ErrFactorConditionFailed) {
		t.Fatalf("err = %v decoded a refusal the payload does not carry", got)
	}
	if got == nil {
		t.Fatal("mapToolError returned nil")
	}
}

func TestMapToolError_userConcurrentUpdateRefusalIsTyped(t *testing.T) {
	want := &transport.UserConcurrentUpdateError{ID: 42}
	got := mapToolError("qrdb_user_update", &wire.ErrorPayload{
		Code: wire.CodeAborted, Message: want.Error(),
		Reason: transport.ReasonUserConcurrentUpdate, Metadata: want.Metadata(),
	})

	refusal, ok := errors.AsType[*transport.UserConcurrentUpdateError](got)
	if !ok || *refusal != *want {
		t.Fatalf("refusal = %+v, want %+v", refusal, want)
	}
	if !dberrors.Is(got, dberrors.ErrConflict) || !dberrors.IsRetryable(got) {
		t.Fatalf("err = %v, want a retryable ErrConflict", got)
	}
	if reqErr, ok := errors.AsType[*transport.RequestError](got); !ok || reqErr.StatusCode != int(wire.CodeAborted) {
		t.Fatalf("err = %T, want a RequestError carrying the aborted code", got)
	}
}

func TestMapToolError_abortedWithoutReasonIsNotARetryableConflict(t *testing.T) {
	got := mapToolError("qrdb_user_update", &wire.ErrorPayload{Code: wire.CodeAborted, Message: "aborted"})
	if _, ok := errors.AsType[*transport.UserConcurrentUpdateError](got); ok {
		t.Fatalf("err = %v decoded a refusal the payload does not carry", got)
	}
	if dberrors.IsRetryable(got) {
		t.Fatalf("err = %v is retryable without the refusal", got)
	}
}
