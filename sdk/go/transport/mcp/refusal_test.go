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
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
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

// TestMapToolError_lockRefusalsAreTyped pins that each typed lock outcome
// survives MCP with its key, holder and tokens, and that a missing lock --
// CodeNotFound -- is not read as a missing key.
func TestMapToolError_lockRefusalsAreTyped(t *testing.T) {
	tests := []struct {
		name   string
		code   wire.Code
		reason string
		err    interface {
			error
			Metadata() map[string]string
		}
		match func(error) (error, bool)
	}{
		{
			name: "held", code: wire.CodeAborted, reason: lock.ReasonLockHeld,
			err: &lock.LockHeldError{Key: "k", HolderID: "h"},
			match: func(err error) (error, bool) {
				e, ok := errors.AsType[*lock.LockHeldError](err)
				return e, ok
			},
		},
		{
			name: "not holder", code: wire.CodePermissionDenied, reason: lock.ReasonNotLockHolder,
			err: &lock.NotLockHolderError{Key: "k", ExpectedToken: 3, ActualToken: 4},
			match: func(err error) (error, bool) {
				e, ok := errors.AsType[*lock.NotLockHolderError](err)
				return e, ok
			},
		},
		{
			name: "not found", code: wire.CodeNotFound, reason: lock.ReasonLockNotFound,
			err: &lock.LockNotFoundError{Key: "k"},
			match: func(err error) (error, bool) {
				e, ok := errors.AsType[*lock.LockNotFoundError](err)
				return e, ok
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapToolError("qrdb_lock_release", &wire.ErrorPayload{
				Code: tt.code, Message: tt.err.Error(), Reason: tt.reason, Metadata: tt.err.Metadata(),
			})

			if typed, ok := tt.match(got); !ok || !reflect.DeepEqual(typed, tt.err) {
				t.Fatalf("mapToolError() = %#v, want %#v", got, tt.err)
			}
			if _, ok := errors.AsType[*transport.KeyNotFoundError](got); ok {
				t.Fatalf("mapToolError() = %v, a lock outcome read as a missing key", got)
			}
			if reqErr, ok := errors.AsType[*transport.RequestError](got); !ok || reqErr.StatusCode != int(tt.code) {
				t.Fatalf("err = %T, want a RequestError carrying %d", got, tt.code)
			}
		})
	}
}

// TestMapToolError_notFoundWithoutReasonIsAMissingKey pins the other side:
// CodeNotFound with no lock reason is still a missing key.
func TestMapToolError_notFoundWithoutReasonIsAMissingKey(t *testing.T) {
	got := mapToolError("qrdb_get", &wire.ErrorPayload{Code: wire.CodeNotFound, Message: "k"})
	if e, ok := errors.AsType[*transport.KeyNotFoundError](got); !ok || e.Key != "k" {
		t.Fatalf("mapToolError() = %#v, want KeyNotFoundError{Key: k}", got)
	}
}
