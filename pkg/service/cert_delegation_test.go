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

package service

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// delegationScope is the scope these tests delegate.
var delegationScope = []string{string(permission.KVGet), string(permission.KVPut)}

// delegationCSR is a principal's CSR; its subject is ignored by issuance.
func delegationCSR(t *testing.T) []byte {
	t.Helper()
	_, csrPEM, err := certtest.NewCSR("ignored")
	if err != nil {
		t.Fatal(err)
	}
	return csrPEM
}

// billingRequest asks acme's CA to delegate delegationScope to billing.
func billingRequest(t *testing.T) *IssueDelegationRequest {
	t.Helper()
	return &IssueDelegationRequest{Principal: "billing", Scope: slices.Clone(delegationScope), CSRPEM: delegationCSR(t)}
}

func newDelegationService(t *testing.T, opts ...CertEnrollmentOption) *CertEnrollmentService {
	t.Helper()
	return NewCertEnrollmentService(newCertEnrollmentProvider(t), nil, nil, opts...)
}

func parseIssued(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("issued certificate is not PEM")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestIssueDelegation_IssuesTheGrant: an acme admin's request yields a
// certificate from acme's CA carrying exactly the grant, named for the
// principal, for the delegation's default lifetime.
func TestIssueDelegation_IssuesTheGrant(t *testing.T) {
	for name, tenantID := range map[string]string{"implicit tenant": "", "explicit own tenant": "acme"} {
		t.Run(name, func(t *testing.T) {
			svc := newDelegationService(t)
			req := billingRequest(t)
			req.TenantID = tenantID

			got, err := svc.IssueDelegation(anchorCaller("acme", "alice"), req)
			if err != nil {
				t.Fatalf("IssueDelegation: %v", err)
			}
			if got.TenantID != "acme" || got.Principal != "billing" || !slices.Equal(got.Scope, delegationScope) || got.Serial == "" {
				t.Fatalf("issued = %+v", got)
			}
			leaf := parseIssued(t, got.CertPEM)
			g, ok, err := delegation.FromCertificate(leaf)
			if err != nil || !ok || g.Tenant != "acme" || g.Principal != "billing" || !slices.Equal(g.Scope, delegationScope) {
				t.Fatalf("certificate grant = %+v, %v, %v", g, ok, err)
			}
			if leaf.Subject.CommonName != auth.ServicePrincipalSubjectPrefix+"billing" {
				t.Fatalf("CN = %q", leaf.Subject.CommonName)
			}
			if life := leaf.NotAfter.Sub(leaf.NotBefore); life != delegation.DefaultLifetime {
				t.Fatalf("lifetime = %v, want %v", life, delegation.DefaultLifetime)
			}
			if !got.NotAfter.Equal(leaf.NotAfter) {
				t.Fatalf("NotAfter = %v, certificate says %v", got.NotAfter, leaf.NotAfter)
			}
		})
	}
}

// TestIssueDelegation_TTL: a lifetime up to delegation.MaxLifetime is the
// certificate's.
func TestIssueDelegation_TTL(t *testing.T) {
	for _, ttl := range []time.Duration{10 * time.Minute, delegation.MaxLifetime} {
		svc := newDelegationService(t)
		req := billingRequest(t)
		req.TTL = ttl
		got, err := svc.IssueDelegation(anchorCaller("acme", "alice"), req)
		if err != nil {
			t.Fatalf("IssueDelegation(%v): %v", ttl, err)
		}
		if leaf := parseIssued(t, got.CertPEM); leaf.NotAfter.Sub(leaf.NotBefore) != ttl {
			t.Fatalf("lifetime = %v, want %v", leaf.NotAfter.Sub(leaf.NotBefore), ttl)
		}
	}
}

// TestIssueDelegation_RefusesBadRequests: a request the tenant may not make
// is refused with a typed error before anything is audited or signed.
func TestIssueDelegation_RefusesBadRequests(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(r *IssueDelegationRequest)
		field  string
	}{
		"ttl above the limit":    {func(r *IssueDelegationRequest) { r.TTL = delegation.MaxLifetime + time.Second }, "ttl"},
		"negative ttl":           {func(r *IssueDelegationRequest) { r.TTL = -time.Minute }, "ttl"},
		"built-in role":          {func(r *IssueDelegationRequest) { r.Principal = permission.RoleAdmin }, "principal"},
		"invalid principal":      {func(r *IssueDelegationRequest) { r.Principal = "bill/ing" }, "grant"},
		"empty principal":        {func(r *IssueDelegationRequest) { r.Principal = "" }, "grant"},
		"empty scope":            {func(r *IssueDelegationRequest) { r.Scope = nil }, "grant"},
		"unregistered scope":     {func(r *IssueDelegationRequest) { r.Scope = []string{"kv:teleport"} }, "scope"},
		"one unregistered scope": {func(r *IssueDelegationRequest) { r.Scope = append(r.Scope, "kv:teleport") }, "scope"},
	} {
		t.Run(name, func(t *testing.T) {
			ring := audit.NewRingBuffer(16)
			svc := newDelegationService(t, WithEnrollmentAuditLogger(ring))
			req := billingRequest(t)
			tc.mutate(req)

			got, err := svc.IssueDelegation(anchorCaller("acme", "alice"), req)
			var reqErr *DelegationRequestError
			if got != nil || !errors.As(err, &reqErr) || reqErr.Field != tc.field {
				t.Fatalf("IssueDelegation = %+v, %v; want *DelegationRequestError on %q", got, err, tc.field)
			}
			if evs := ring.Snapshot(); len(evs) != 0 {
				t.Fatalf("a refused request was audited as an issuance: %+v", evs)
			}
		})
	}
}

// TestIssueDelegation_RefusesOtherTenantsAndTheSystem: a tenant delegates
// only its own authority, and the platform can never mint a delegation.
func TestIssueDelegation_RefusesOtherTenantsAndTheSystem(t *testing.T) {
	t.Run("another tenant", func(t *testing.T) {
		req := billingRequest(t)
		req.TenantID = "globex"
		_, err := newDelegationService(t).IssueDelegation(anchorCaller("acme", "alice"), req)
		var cross *tenant.CrossTenantDeniedError
		if !errors.As(err, &cross) {
			t.Fatalf("err = %v, want *tenant.CrossTenantDeniedError", err)
		}
	})
	for name, ctx := range map[string]context.Context{
		"system caller":     anchorCaller(tenantid.System, "root"),
		"no caller":         context.Background(),
		"system on request": anchorCaller(tenantid.System, "root"),
	} {
		t.Run(name, func(t *testing.T) {
			req := billingRequest(t)
			if name == "system on request" {
				req.TenantID = tenantid.System
			}
			_, err := newDelegationService(t).IssueDelegation(ctx, req)
			var denied *DelegationDeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("err = %v, want *DelegationDeniedError", err)
			}
		})
	}
}

// TestIssueDelegation_RefusesMissingInputs: no CA, no request and no CSR are
// each refused by name; a CSR that does not parse is the CA's refusal.
func TestIssueDelegation_RefusesMissingInputs(t *testing.T) {
	ctx := anchorCaller("acme", "alice")
	var notConfigured *CertEnrollmentNotConfiguredError
	if _, err := NewCertEnrollmentService(nil, nil, nil).IssueDelegation(ctx, billingRequest(t)); !errors.As(err, &notConfigured) {
		t.Fatalf("no CA: err = %v, want *CertEnrollmentNotConfiguredError", err)
	}
	var field *CertEnrollmentFieldError
	for name, req := range map[string]*IssueDelegationRequest{
		"nil request": nil,
		"no csr":      {Principal: "billing", Scope: delegationScope},
	} {
		if _, err := newDelegationService(t).IssueDelegation(ctx, req); !errors.As(err, &field) || field.Field != "csr_pem" {
			t.Fatalf("%s: err = %v, want *CertEnrollmentFieldError on csr_pem", name, err)
		}
	}
	req := billingRequest(t)
	req.CSRPEM = []byte("not a csr")
	var badCSR *cert.InvalidCSRError
	if _, err := newDelegationService(t).IssueDelegation(ctx, req); !errors.As(err, &badCSR) {
		t.Fatalf("bad csr: err = %v, want *cert.InvalidCSRError", err)
	}
}

// TestIssueDelegation_Audited: issuance is recorded on the tenant's trail
// with the principal and scope it delegated, as the issuing caller.
func TestIssueDelegation_Audited(t *testing.T) {
	ring := audit.NewRingBuffer(16)
	svc := newDelegationService(t, WithEnrollmentAuditLogger(ring))
	if _, err := svc.IssueDelegation(anchorCaller("acme", "alice"), billingRequest(t)); err != nil {
		t.Fatalf("IssueDelegation: %v", err)
	}
	ev := findEvent(ring.Snapshot(), ActionCertDelegate)
	if ev == nil {
		t.Fatalf("no %s event; events = %+v", ActionCertDelegate, ring.Snapshot())
	}
	scope, _ := ev.Attributes["scope"].([]string)
	if ev.TenantID != "acme" || ev.Actor != "alice" || ev.Outcome != audit.OutcomeSuccess ||
		ev.Attributes["principal"] != "billing" || !slices.Equal(scope, delegationScope) {
		t.Fatalf("%s event = %+v", ActionCertDelegate, ev)
	}
}

// TestIssueDelegation_FailClosedOnAuditRefusal: a delegation whose gating
// record cannot be written is never signed.
func TestIssueDelegation_FailClosedOnAuditRefusal(t *testing.T) {
	svc := newDelegationService(t, WithEnrollmentAuditLogger(&refusingLogger{}))
	got, err := svc.IssueDelegation(anchorCaller("acme", "alice"), billingRequest(t))
	var gateErr *audit.GateError
	if got != nil || !errors.As(err, &gateErr) {
		t.Fatalf("IssueDelegation = %+v, %v; want *audit.GateError", got, err)
	}
}

// TestDelegationErrors_DescribeAndUnwrap: each error names what was refused,
// and a request error wraps the grant's own.
func TestDelegationErrors_DescribeAndUnwrap(t *testing.T) {
	inner := &delegation.GrantError{Field: "principal", Reason: "empty"}
	reqErr := &DelegationRequestError{Field: "grant", Reason: "invalid", Err: inner}
	if !errors.Is(reqErr, inner) || !strings.Contains(reqErr.Error(), "grant") || !strings.Contains(reqErr.Error(), inner.Error()) {
		t.Fatalf("DelegationRequestError = %q, does not describe or wrap %q", reqErr.Error(), inner.Error())
	}
	bare := &DelegationRequestError{Field: "ttl", Reason: "too long"}
	if bare.Unwrap() != nil || !strings.Contains(bare.Error(), "ttl") || !strings.Contains(bare.Error(), "too long") {
		t.Fatalf("DelegationRequestError = %q", bare.Error())
	}
	denied := &DelegationDeniedError{Reason: "the system tenant cannot delegate"}
	if !strings.Contains(denied.Error(), denied.Reason) {
		t.Fatalf("DelegationDeniedError = %q", denied.Error())
	}
}
