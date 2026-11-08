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
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func refusalBody(t *testing.T, domain string, refusal *auth.FactorConditionFailedError) []byte {
	t.Helper()
	body, err := wire.EncodeErrorPayload(wire.StatusAlreadyExists, wire.ErrorPayload{
		Message: refusal.Error(),
		Info:    &wire.ErrorInfo{Domain: domain, Reason: auth.ReasonFactorConditionFailed, Metadata: refusal.Metadata()},
	})
	if err != nil {
		t.Fatalf("encode error payload: %v", err)
	}
	return body
}

func TestMapWireError_factorConditionRefusalIsTyped(t *testing.T) {
	want := &auth.FactorConditionFailedError{UserID: 42, Type: auth.AuthFactorMTLS, Condition: auth.FactorConditionNoActiveOfType, CurrentVersion: 1, ExistingFactorID: "f-1"}
	got := mapWireError("userauthfactor.Add", wire.StatusAlreadyExists, refusalBody(t, transport.RefusalDomain, want))

	if !errors.Is(got, auth.ErrFactorConditionFailed) {
		t.Fatalf("err = %v, want ErrFactorConditionFailed", got)
	}
	refusal, ok := errors.AsType[*auth.FactorConditionFailedError](got)
	if !ok || *refusal != *want {
		t.Fatalf("refusal = %+v, want %+v", refusal, want)
	}
	if reqErr, ok := errors.AsType[*transport.RequestError](got); !ok || reqErr.StatusCode != int(wire.StatusAlreadyExists) {
		t.Fatalf("err = %T, want a RequestError carrying ALREADY_EXISTS", got)
	}
}

func TestMapWireError_foreignDomainIsNotDecoded(t *testing.T) {
	refusal := &auth.FactorConditionFailedError{UserID: 42, Type: auth.AuthFactorMTLS, Condition: auth.FactorConditionNoActiveOfType}
	got := mapWireError("userauthfactor.Add", wire.StatusAlreadyExists, refusalBody(t, "other.v1", refusal))
	if errors.Is(got, auth.ErrFactorConditionFailed) {
		t.Fatalf("err = %v decoded a refusal from a foreign domain", got)
	}
}

func userConcurrentUpdateBody(t *testing.T, domain string, refusal *transport.UserConcurrentUpdateError) []byte {
	t.Helper()
	body, err := wire.EncodeErrorPayload(wire.StatusAborted, wire.ErrorPayload{
		Message: refusal.Error(),
		Info:    &wire.ErrorInfo{Domain: domain, Reason: transport.ReasonUserConcurrentUpdate, Metadata: refusal.Metadata()},
	})
	if err != nil {
		t.Fatalf("encode error payload: %v", err)
	}
	return body
}

func TestMapWireError_userConcurrentUpdateRefusalIsTyped(t *testing.T) {
	want := &transport.UserConcurrentUpdateError{ID: 42}
	got := mapWireError("user.Update", wire.StatusAborted, userConcurrentUpdateBody(t, transport.RefusalDomain, want))

	refusal, ok := errors.AsType[*transport.UserConcurrentUpdateError](got)
	if !ok || *refusal != *want {
		t.Fatalf("refusal = %+v, want %+v", refusal, want)
	}
	if !dberrors.Is(got, dberrors.ErrConflict) || !dberrors.IsRetryable(got) {
		t.Fatalf("err = %v, want a retryable ErrConflict", got)
	}
	if reqErr, ok := errors.AsType[*transport.RequestError](got); !ok || reqErr.StatusCode != int(wire.StatusAborted) {
		t.Fatalf("err = %T, want a RequestError carrying ABORTED", got)
	}
}

func TestMapWireError_userConcurrentUpdateForeignDomainIsNotDecoded(t *testing.T) {
	got := mapWireError("user.Update", wire.StatusAborted, userConcurrentUpdateBody(t, "other.v1", &transport.UserConcurrentUpdateError{ID: 42}))
	if _, ok := errors.AsType[*transport.UserConcurrentUpdateError](got); ok {
		t.Fatalf("err = %v decoded a refusal from a foreign domain", got)
	}
	if dberrors.IsRetryable(got) {
		t.Fatalf("err = %v is retryable without the refusal", got)
	}
}

// TestMapWireError_lockRefusalsAreTyped pins that each typed lock outcome
// survives the QUIC wire with its key, holder and tokens, and that a missing
// lock -- NOT_FOUND on the wire -- is not read as a missing key.
func TestMapWireError_lockRefusalsAreTyped(t *testing.T) {
	tests := []struct {
		name   string
		status uint16
		reason string
		err    interface {
			error
			Metadata() map[string]string
		}
		match func(error) (error, bool)
	}{
		{
			name: "held", status: wire.StatusAborted, reason: lock.ReasonLockHeld,
			err: &lock.LockHeldError{Key: "k", HolderID: "h"},
			match: func(err error) (error, bool) {
				e, ok := errors.AsType[*lock.LockHeldError](err)
				return e, ok
			},
		},
		{
			name: "not holder", status: wire.StatusPermissionDenied, reason: lock.ReasonNotLockHolder,
			err: &lock.NotLockHolderError{Key: "k", ExpectedToken: 3, ActualToken: 4},
			match: func(err error) (error, bool) {
				e, ok := errors.AsType[*lock.NotLockHolderError](err)
				return e, ok
			},
		},
		{
			name: "not found", status: wire.StatusNotFound, reason: lock.ReasonLockNotFound,
			err: &lock.LockNotFoundError{Key: "k"},
			match: func(err error) (error, bool) {
				e, ok := errors.AsType[*lock.LockNotFoundError](err)
				return e, ok
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := wire.EncodeErrorPayload(tt.status, wire.ErrorPayload{
				Message: tt.err.Error(),
				Info:    &wire.ErrorInfo{Domain: transport.RefusalDomain, Reason: tt.reason, Metadata: tt.err.Metadata()},
			})
			if err != nil {
				t.Fatalf("encode error payload: %v", err)
			}
			got := mapWireError("lock.Release", tt.status, body)

			if typed, ok := tt.match(got); !ok || !reflect.DeepEqual(typed, tt.err) {
				t.Fatalf("mapWireError() = %#v, want %#v", got, tt.err)
			}
			if _, ok := errors.AsType[*transport.KeyNotFoundError](got); ok {
				t.Fatalf("mapWireError() = %v, a lock outcome read as a missing key", got)
			}
			if reqErr, ok := errors.AsType[*transport.RequestError](got); !ok || reqErr.StatusCode != int(tt.status) {
				t.Fatalf("err = %T, want a RequestError carrying %d", got, tt.status)
			}
		})
	}
}

// TestMapWireError_notFoundWithoutRefusalIsAMissingKey pins the other side:
// NOT_FOUND with no lock reason is still a missing key.
func TestMapWireError_notFoundWithoutRefusalIsAMissingKey(t *testing.T) {
	body, err := wire.EncodeErrorPayload(wire.StatusNotFound, wire.ErrorPayload{Message: "k"})
	if err != nil {
		t.Fatalf("encode error payload: %v", err)
	}
	got := mapWireError("kv.Get", wire.StatusNotFound, body)
	if e, ok := errors.AsType[*transport.KeyNotFoundError](got); !ok || e.Key != "k" {
		t.Fatalf("mapWireError() = %#v, want KeyNotFoundError{Key: k}", got)
	}
}
