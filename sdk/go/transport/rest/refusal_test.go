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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
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
