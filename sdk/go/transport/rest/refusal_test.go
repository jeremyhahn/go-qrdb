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
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func conflictResponse(t *testing.T, body transport.ErrorResponse) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &http.Response{StatusCode: http.StatusConflict, Status: "409 Conflict", Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(raw))}
}

func TestDecodeErrorResponse_factorConditionRefusalIsTyped(t *testing.T) {
	want := &auth.FactorConditionFailedError{UserID: 42, Type: auth.AuthFactorPIV, Condition: auth.FactorConditionNoActiveOfType, CurrentVersion: 3, ExistingFactorID: "f-9"}
	resp := conflictResponse(t, transport.ErrorResponse{
		Error: want.Error(), Code: auth.ReasonFactorConditionFailed, Metadata: want.Metadata(),
	})
	defer func() { _ = resp.Body.Close() }()
	got := (&Transport{}).decodeErrorResponse("AddAuthFactor", resp)

	refusal, ok := errors.AsType[*auth.FactorConditionFailedError](got)
	if !ok || *refusal != *want {
		t.Fatalf("refusal = %+v, want %+v", refusal, want)
	}
	if reqErr, ok := errors.AsType[*transport.RequestError](got); !ok || reqErr.StatusCode != http.StatusConflict {
		t.Fatalf("err = %T, want a RequestError carrying 409", got)
	}
}

func TestDecodeErrorResponse_conflictWithoutCodeStaysPlain(t *testing.T) {
	resp := conflictResponse(t, transport.ErrorResponse{Error: "factor exists"})
	defer func() { _ = resp.Body.Close() }()
	got := (&Transport{}).decodeErrorResponse("AddAuthFactor", resp)
	if errors.Is(got, auth.ErrFactorConditionFailed) {
		t.Fatalf("err = %v decoded a refusal the body does not carry", got)
	}
	if reqErr, ok := errors.AsType[*transport.RequestError](got); !ok || reqErr.StatusCode != http.StatusConflict {
		t.Fatalf("err = %T %v, want a RequestError carrying 409", got, got)
	}
}

func TestDecodeErrorResponse_userConcurrentUpdateRefusalIsTyped(t *testing.T) {
	want := &transport.UserConcurrentUpdateError{ID: 42}
	resp := conflictResponse(t, transport.ErrorResponse{
		Error: want.Error(), Code: transport.ReasonUserConcurrentUpdate, Metadata: want.Metadata(),
	})
	defer func() { _ = resp.Body.Close() }()
	got := (&Transport{}).decodeErrorResponse("UpdateUser", resp)

	refusal, ok := errors.AsType[*transport.UserConcurrentUpdateError](got)
	if !ok || *refusal != *want {
		t.Fatalf("refusal = %+v, want %+v", refusal, want)
	}
	if !dberrors.Is(got, dberrors.ErrConflict) || !dberrors.IsRetryable(got) {
		t.Fatalf("err = %v, want a retryable ErrConflict", got)
	}
	if reqErr, ok := errors.AsType[*transport.RequestError](got); !ok || reqErr.StatusCode != http.StatusConflict {
		t.Fatalf("err = %T, want a RequestError carrying 409", got)
	}
}

func TestDecodeErrorResponse_conflictWithoutCodeIsNotARetryableConflict(t *testing.T) {
	resp := conflictResponse(t, transport.ErrorResponse{Error: "aborted"})
	defer func() { _ = resp.Body.Close() }()
	got := (&Transport{}).decodeErrorResponse("UpdateUser", resp)
	if _, ok := errors.AsType[*transport.UserConcurrentUpdateError](got); ok {
		t.Fatalf("err = %v decoded a refusal the body does not carry", got)
	}
	if dberrors.IsRetryable(got) {
		t.Fatalf("err = %v is retryable without the refusal", got)
	}
}

// TestDecodeErrorResponse_lockRefusalsAreTyped pins that each typed lock
// outcome survives REST with its key, holder and tokens, and that a missing
// lock -- a 404 -- is not read as a missing key.
func TestDecodeErrorResponse_lockRefusalsAreTyped(t *testing.T) {
	tests := []struct {
		name   string
		status int
		reason string
		err    interface {
			error
			Metadata() map[string]string
		}
		match func(error) (error, bool)
	}{
		{
			name: "held", status: http.StatusConflict, reason: lock.ReasonLockHeld,
			err: &lock.LockHeldError{Key: "k", HolderID: "h"},
			match: func(err error) (error, bool) {
				e, ok := errors.AsType[*lock.LockHeldError](err)
				return e, ok
			},
		},
		{
			name: "not holder", status: http.StatusForbidden, reason: lock.ReasonNotLockHolder,
			err: &lock.NotLockHolderError{Key: "k", ExpectedToken: 3, ActualToken: 4},
			match: func(err error) (error, bool) {
				e, ok := errors.AsType[*lock.NotLockHolderError](err)
				return e, ok
			},
		},
		{
			name: "not found", status: http.StatusNotFound, reason: lock.ReasonLockNotFound,
			err: &lock.LockNotFoundError{Key: "k"},
			match: func(err error) (error, bool) {
				e, ok := errors.AsType[*lock.LockNotFoundError](err)
				return e, ok
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := conflictResponse(t, transport.ErrorResponse{
				Error: tt.err.Error(), Code: tt.reason, Metadata: tt.err.Metadata(),
			})
			resp.StatusCode = tt.status
			defer func() { _ = resp.Body.Close() }()
			got := (&Transport{}).decodeErrorResponse("LockRelease", resp)

			if typed, ok := tt.match(got); !ok || !reflect.DeepEqual(typed, tt.err) {
				t.Fatalf("decodeErrorResponse() = %#v, want %#v", got, tt.err)
			}
			if _, ok := errors.AsType[*transport.KeyNotFoundError](got); ok {
				t.Fatalf("decodeErrorResponse() = %v, a lock outcome read as a missing key", got)
			}
			if reqErr, ok := errors.AsType[*transport.RequestError](got); !ok || reqErr.StatusCode != tt.status {
				t.Fatalf("err = %T, want a RequestError carrying %d", got, tt.status)
			}
		})
	}
}
