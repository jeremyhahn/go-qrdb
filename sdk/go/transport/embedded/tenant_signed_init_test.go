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

package embedded

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// signedInitRecorder answers the two halves of a signed tenant barrier init
// and records the service requests the transport built from the wire ones.
type signedInitRecorder struct {
	mockTenantService
	begin    *tenant.TenantBarrierInitBeginRequest
	complete *tenant.TenantBarrierInitCompleteRequest
	seen     []byte
	err      error
}

func (r *signedInitRecorder) TenantBarrierInitBegin(_ context.Context, req *tenant.TenantBarrierInitBeginRequest) (*tenant.TenantBarrierInitBeginResult, error) {
	r.begin = req
	if r.err != nil {
		return nil, r.err
	}
	return &tenant.TenantBarrierInitBeginResult{
		TenantID:    req.TenantID,
		Strategy:    string(req.Strategy),
		Reference:   []byte("ref"),
		Statement:   []byte("statement"),
		ExpiresAt:   time.Unix(1700000000, 0).UTC(),
		Threshold:   req.Threshold,
		TotalShares: req.Shares,
		ShareCount:  req.Shares,
	}, nil
}

func (r *signedInitRecorder) TenantBarrierInitComplete(_ context.Context, req *tenant.TenantBarrierInitCompleteRequest) (*tenant.TenantBarrierInitResult, error) {
	r.complete = req
	_ = req.Passphrase.WithBytes(func(p []byte) error { r.seen = bytes.Clone(p); return nil })
	if r.err != nil {
		return nil, r.err
	}
	return &tenant.TenantBarrierInitResult{TenantID: req.TenantID, Strategy: "passphrase", AdminPending: true}, nil
}

func TestTenantBarrierSignedInitBegin_MapsTheRequestAndResult(t *testing.T) {
	svc := &signedInitRecorder{}
	tr := newExtTransport(t, WithTenantService(svc))
	got, err := tr.TenantBarrierSignedInitBegin(context.Background(), &transport.TenantBarrierSignedInitBeginRequest{
		TenantID:  "acme",
		Strategy:  string(seal.StrategyShamir),
		KeyRef:    "kr",
		Threshold: 2,
		Shares:    3,
		Officers:  []transport.OfficerRegisterRequest{{Username: "alice"}},
		Policy:    "any",
		PublicKey: []byte("spki"),
	})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	b := svc.begin
	if b.TenantID != "acme" || b.Strategy != seal.StrategyShamir || b.KeyRef != "kr" || b.Threshold != 2 || b.Shares != 3 || !bytes.Equal(b.SignerPublicKey, []byte("spki")) {
		t.Fatalf("service request = %+v", b)
	}
	if b.Roster == nil || len(b.Roster.Officers) != 1 || b.Roster.Policy != seal.FactorPolicy("any") {
		t.Fatalf("roster = %+v", b.Roster)
	}
	if got.TenantID != "acme" || !bytes.Equal(got.Reference, []byte("ref")) || !bytes.Equal(got.Statement, []byte("statement")) || got.ExpiresAt.IsZero() || got.Threshold != 2 || got.TotalShares != 3 {
		t.Fatalf("response = %+v", got)
	}
}

func TestTenantBarrierSignedInitBegin_NoOfficersSendsNoRoster(t *testing.T) {
	svc := &signedInitRecorder{}
	tr := newExtTransport(t, WithTenantService(svc))
	if _, err := tr.TenantBarrierSignedInitBegin(context.Background(), &transport.TenantBarrierSignedInitBeginRequest{TenantID: "acme", PublicKey: []byte("spki")}); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if svc.begin.Roster != nil {
		t.Fatalf("roster = %+v, want nil", svc.begin.Roster)
	}
}

func TestTenantBarrierSignedInitComplete_MapsTheRequestAndResult(t *testing.T) {
	svc := &signedInitRecorder{}
	tr := newExtTransport(t, WithTenantService(svc))
	passphrase := []byte("pass")
	got, err := tr.TenantBarrierSignedInitComplete(context.Background(), &transport.TenantBarrierSignedInitCompleteRequest{
		TenantID:   "acme",
		Reference:  []byte("ref"),
		Signature:  []byte("sig"),
		Passphrase: passphrase,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	c := svc.complete
	if c.TenantID != "acme" || !bytes.Equal(c.Reference, []byte("ref")) || !bytes.Equal(c.Signature, []byte("sig")) || !bytes.Equal(svc.seen, []byte("pass")) {
		t.Fatalf("service request = %+v", c)
	}
	// The caller's passphrase is borrowed: left as it was, while the
	// protected copy the service saw is destroyed once the call returns.
	if !bytes.Equal(passphrase, []byte("pass")) || c.Passphrase.IsAlive() {
		t.Fatalf("service request = %+v", c)
	}
	if got.TenantID != "acme" || got.Strategy != "passphrase" || !got.AdminPending {
		t.Fatalf("response = %+v", got)
	}
}

func TestTenantBarrierSignedInit_ServiceErrorPropagates(t *testing.T) {
	refusal := &tenant.SignedInitError{TenantID: "acme", Reason: tenant.SignedInitUnknown}
	svc := &signedInitRecorder{err: refusal}
	tr := newExtTransport(t, WithTenantService(svc))
	var se *tenant.SignedInitError
	if _, err := tr.TenantBarrierSignedInitBegin(context.Background(), &transport.TenantBarrierSignedInitBeginRequest{TenantID: "acme"}); !errors.As(err, &se) {
		t.Fatalf("begin err = %v, want *SignedInitError", err)
	}
	if _, err := tr.TenantBarrierSignedInitComplete(context.Background(), &transport.TenantBarrierSignedInitCompleteRequest{TenantID: "acme"}); !errors.As(err, &se) {
		t.Fatalf("complete err = %v, want *SignedInitError", err)
	}
}

func TestTenantBarrierSignedInit_NilRequestIsRefused(t *testing.T) {
	tr := newExtTransport(t, WithTenantService(&signedInitRecorder{}))
	var nre *transport.NilRequestError
	if _, err := tr.TenantBarrierSignedInitBegin(context.Background(), nil); !errors.As(err, &nre) {
		t.Fatalf("begin err = %v, want NilRequestError", err)
	}
	if _, err := tr.TenantBarrierSignedInitComplete(context.Background(), nil); !errors.As(err, &nre) {
		t.Fatalf("complete err = %v, want NilRequestError", err)
	}
}

func TestTenantBarrierSignedInit_NoTenantServiceIsRefused(t *testing.T) {
	tr := newExtTransport(t)
	var nse *NilServiceError
	if _, err := tr.TenantBarrierSignedInitBegin(context.Background(), &transport.TenantBarrierSignedInitBeginRequest{TenantID: "acme"}); !errors.As(err, &nse) {
		t.Fatalf("begin err = %v, want NilServiceError", err)
	}
	if _, err := tr.TenantBarrierSignedInitComplete(context.Background(), &transport.TenantBarrierSignedInitCompleteRequest{TenantID: "acme"}); !errors.As(err, &nse) {
		t.Fatalf("complete err = %v, want NilServiceError", err)
	}
}

func TestTenantBarrierSignedInit_ClosedTransportIsRefused(t *testing.T) {
	tr := newExtTransport(t, WithTenantService(&signedInitRecorder{}))
	if err := tr.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	var ce *transport.ClosedError
	if _, err := tr.TenantBarrierSignedInitBegin(context.Background(), &transport.TenantBarrierSignedInitBeginRequest{TenantID: "acme"}); !errors.As(err, &ce) {
		t.Fatalf("begin err = %v, want ClosedError", err)
	}
	if _, err := tr.TenantBarrierSignedInitComplete(context.Background(), &transport.TenantBarrierSignedInitCompleteRequest{TenantID: "acme"}); !errors.As(err, &ce) {
		t.Fatalf("complete err = %v, want ClosedError", err)
	}
}

// Both halves are authorized as a plain init: once the barrier is
// initialized the bootstrap window is closed to a bare tenant identity.
func TestTenantBarrierSignedInit_DeniedOnceInitialized(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "acme")
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles))
	svc := &signedInitRecorder{}
	svc.barrierStatusFn = func(context.Context, string) (*tenant.TenantBarrierStatus, error) {
		return &tenant.TenantBarrierStatus{TenantID: "acme", Initialized: true}, nil
	}
	tr := newExtTransport(t, WithTenantService(svc), WithAuthMiddleware(mw))
	ctx := ctxFor(&auth.Identity{Subject: "bootstrap", TenantID: "acme"})
	if _, err := tr.TenantBarrierSignedInitBegin(ctx, &transport.TenantBarrierSignedInitBeginRequest{TenantID: "acme"}); err == nil {
		t.Fatal("begin on an initialized barrier must require the full RBAC grant")
	}
	if _, err := tr.TenantBarrierSignedInitComplete(ctx, &transport.TenantBarrierSignedInitCompleteRequest{TenantID: "acme"}); err == nil {
		t.Fatal("complete on an initialized barrier must require the full RBAC grant")
	}
	if svc.begin != nil || svc.complete != nil {
		t.Fatal("a refused call must not reach the service")
	}
}
