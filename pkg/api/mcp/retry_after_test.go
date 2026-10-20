// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestClassifiedToolError_CarriesRetryAfter: a refusal that says how long to
// wait -- a throttled quorum request or sign-csr, an SO PIN lockout -- carries
// the delay in the error envelope, rounded up to whole seconds, as REST's
// Retry-After does. Any other failure carries none.
func TestClassifiedToolError_CarriesRetryAfter(t *testing.T) {
	cases := map[string]struct {
		err  error
		want int64
	}{
		"quorum throttle":  {&ceremony.QuorumRateLimitedError{RetryAfter: 1500 * time.Millisecond}, 2},
		"SO PIN lockout":   {&pin.SOPINLockedError{RetryAfter: 4 * time.Second}, 4},
		"sign-csr":         {&service.SignCSRThrottledError{RetryAfter: 10 * time.Millisecond}, 1},
		"no delay to name": {errors.New("boom"), 0},
	}
	for name, tc := range cases {
		res := classifiedToolError("qrdb_barrier_quorum_begin", tc.err)
		payload, ok := wire.DecodeErrorPayload(res.StructuredContent)
		if !ok {
			t.Fatalf("%s: no decodable error payload", name)
		}
		if payload.RetryAfterSeconds != tc.want {
			t.Errorf("%s: RetryAfterSeconds = %d, want %d", name, payload.RetryAfterSeconds, tc.want)
		}
	}
}

// A create of an undone id that a serving node has not yet released says
// when to try again.
func TestClassifiedToolError_UndoPendingCarriesRetryAfter(t *testing.T) {
	err := &tenant.TenantUndoPendingError{TenantID: "acme", Pending: []string{"n2:7000"}, RetryAfter: tenant.UndoPendingRetryAfter}
	res := classifiedToolError("qrdb_tenant_create", err)
	payload, ok := wire.DecodeErrorPayload(res.StructuredContent)
	if !ok {
		t.Fatal("no decodable error payload")
	}
	if want := int64(tenant.UndoPendingRetryAfter / time.Second); payload.RetryAfterSeconds != want {
		t.Fatalf("RetryAfterSeconds = %d, want %d", payload.RetryAfterSeconds, want)
	}
}

// TestAuthzToolError_CarriesRetryAfter: an auth-layer refusal that names a
// delay -- a tenant SO PIN lockout from the SO gate -- carries it in the
// envelope as a service-layer one does.
func TestAuthzToolError_CarriesRetryAfter(t *testing.T) {
	res := authzToolError(&auth.SOPINLockedError{RetryAfter: 1500 * time.Millisecond})
	payload, ok := wire.DecodeErrorPayload(res.StructuredContent)
	if !ok {
		t.Fatal("no decodable error payload")
	}
	if payload.Code != wire.CodeResourceExhausted || payload.RetryAfterSeconds != 2 {
		t.Fatalf("payload = %+v, want ResourceExhausted with retry_after_seconds 2", payload)
	}
}
