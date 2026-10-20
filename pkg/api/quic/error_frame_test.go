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

// No build constraint: frameMessage is shared by tests in every build of this
// package.

package quic

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// frameDecoded decodes an error frame body, failing the test if it is not a
// wire.ErrorPayload.
func frameDecoded(t *testing.T, body []byte) wire.ErrorPayload {
	t.Helper()
	p, err := wire.DecodeErrorPayload(body)
	if err != nil {
		t.Fatalf("error frame body does not decode: %v (body %q)", err, body)
	}
	return p
}

// frameMessage is the message an error frame body carries.
func frameMessage(t *testing.T, body []byte) string {
	t.Helper()
	return frameDecoded(t, body).Message
}

// delayingRefusals are the refusals that name a retry delay, as every
// transport's retry test lists them: the service-layer throttles and the node
// SO PIN lockout, and the auth-layer rate limit and tenant SO PIN lockout.
var delayingRefusals = map[string]struct {
	err  error
	want time.Duration
}{
	"quorum throttle":       {&ceremony.QuorumRateLimitedError{RetryAfter: 1500 * time.Millisecond}, 1500 * time.Millisecond},
	"SO PIN lockout":        {&pin.SOPINLockedError{RetryAfter: 4 * time.Second}, 4 * time.Second},
	"sign-csr":              {&service.SignCSRThrottledError{RetryAfter: 2 * time.Second}, 2 * time.Second},
	"tenant SO PIN lockout": {&auth.SOPINLockedError{RetryAfter: 3 * time.Second}, 3 * time.Second},
	"auth rate limit":       {&authmw.RateLimitedError{RetryAfter: 5 * time.Second}, 5 * time.Second},
}

// TestMapServiceError_CarriesTheRetryDelay: a refusal that says how long to
// wait reaches a QUIC caller with StatusResourceExhausted and the exact delay
// in the error frame -- REST's Retry-After, gRPC's RetryInfo and MCP's
// retry_after_seconds on this transport. Any other failure carries none.
func TestMapServiceError_CarriesTheRetryDelay(t *testing.T) {
	for name, tc := range delayingRefusals {
		st, body := mapServiceError(tc.err)
		if st != wire.StatusResourceExhausted {
			t.Errorf("%s: status %s, want RESOURCE_EXHAUSTED", name, wire.StatusName(st))
		}
		p := frameDecoded(t, body)
		if !p.HasRetryAfter || p.RetryAfter != tc.want || p.Message != tc.err.Error() {
			t.Errorf("%s: payload %+v, want message %q and delay %v", name, p, tc.err.Error(), tc.want)
		}
	}
	if p := frameDecoded(t, bodyOf(mapServiceError(errors.New("boom")))); p.HasRetryAfter {
		t.Errorf("an error naming no delay carries one: %+v", p)
	}
}

// A create of an undone id that a serving node has not yet released is
// UNAVAILABLE, not a throttle, and still says when to try again.
func TestMapServiceError_UndoPendingIsUnavailableWithTheDelay(t *testing.T) {
	err := &tenant.TenantUndoPendingError{TenantID: "acme", Pending: []string{"n2:7000"}, RetryAfter: tenant.UndoPendingRetryAfter}
	st, body := mapServiceError(err)
	if st != wire.StatusUnavailable {
		t.Fatalf("status %s, want UNAVAILABLE", wire.StatusName(st))
	}
	p := frameDecoded(t, body)
	if !p.HasRetryAfter || p.RetryAfter != tenant.UndoPendingRetryAfter || p.Message != err.Error() {
		t.Fatalf("payload %+v, want message %q and delay %v", p, err.Error(), tenant.UndoPendingRetryAfter)
	}
}

// TestAuthGateFrame_CarriesTheRetryDelay: the auth and SO gates' refusals are
// rendered through errorFrame with authzStatus, so a tenant SO PIN lockout or
// a rate-limited caller is told the delay on the gate path too.
func TestAuthGateFrame_CarriesTheRetryDelay(t *testing.T) {
	for _, err := range []error{&auth.SOPINLockedError{RetryAfter: 3 * time.Second}, &authmw.RateLimitedError{RetryAfter: 3 * time.Second}} {
		st, body := errorFrame(authzStatus(err), err)
		if st != wire.StatusResourceExhausted {
			t.Errorf("%T: status %s, want RESOURCE_EXHAUSTED", err, wire.StatusName(st))
		}
		if p := frameDecoded(t, body); !p.HasRetryAfter || p.RetryAfter != 3*time.Second {
			t.Errorf("%T: payload %+v, want a 3s delay", err, p)
		}
	}
}

// TestErrorFrameBody_KeepsTheStatus: a payload that encodes keeps the status
// it was built for.
func TestErrorFrameBody_KeepsTheStatus(t *testing.T) {
	st, body := errorFrameBody(wire.StatusNotFound, wire.ErrorPayload{Message: "absent \xff"})
	if st != wire.StatusNotFound {
		t.Fatalf("status %s, want NOT_FOUND", wire.StatusName(st))
	}
	if got := frameMessage(t, body); got != "absent \uFFFD" {
		t.Fatalf("message %q", got)
	}
}

// bodyOf drops the status from a (status, body) pair.
func bodyOf(_ uint16, body []byte) []byte { return body }

// TestIntegration_ErrorFrameCarriesTheRetryDelay drives a refusal through a
// live listener: the frame a client reads is RESOURCE_EXHAUSTED with an error
// payload naming the exact delay.
func TestIntegration_ErrorFrameCarriesTheRetryDelay(t *testing.T) {
	bundle := generateIntegrationTLS(t)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithTLS(bundle.serverCfg))
	srv.handlers["test.throttled"] = func(context.Context, []byte) ([]byte, error) {
		return nil, &ceremony.QuorumRateLimitedError{RetryAfter: 1500 * time.Millisecond}
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })
	conn := (&integrationEnv{srv: srv, clientCfg: bundle.clientCfg}).dialServer(t)

	_, status, body := roundTrip(t, conn, "test.throttled", 7, nil)
	if status != wire.StatusResourceExhausted {
		t.Fatalf("status %s, want RESOURCE_EXHAUSTED", wire.StatusName(status))
	}
	if p := frameDecoded(t, body); !p.HasRetryAfter || p.RetryAfter != 1500*time.Millisecond {
		t.Fatalf("payload %+v, want a 1.5s delay", p)
	}
}
