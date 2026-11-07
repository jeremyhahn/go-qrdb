// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// The helpers below are built from the exported API alone: gosec type-checks
// this external test package without the package's own _test.go files, so a
// helper exported from one of those does not resolve here.

// newPolicyService is a CertEnrollmentService whose provider keeps the
// tenants' standing delegation policies.
func newPolicyService(t *testing.T, opts ...service.CertEnrollmentOption) *service.CertEnrollmentService {
	t.Helper()
	p := cert.NewDefaultProvider(kvstoretest.NewGuardedMemoryStore(), newPolicyBarrier(t),
		cert.WithTrustDomain("qrdb.example.com"))
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	store, ok := any(p).(cert.DelegationPolicyStore)
	if !ok {
		t.Fatalf("%T keeps no delegation policies", p)
	}
	return service.NewCertEnrollmentService(p, nil, nil, append([]service.CertEnrollmentOption{service.WithDelegationPolicyStore(store)}, opts...)...)
}

// policyBarrier is an unsealed seal.EncryptionBarrier under a random
// AES-256-GCM key, standing in for the node's barrier.
type policyBarrier struct{ aead cipher.AEAD }

var _ seal.EncryptionBarrier = (*policyBarrier)(nil)

func newPolicyBarrier(t *testing.T) *policyBarrier {
	t.Helper()
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("generating a test barrier key: %v", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		t.Fatal(err)
	}
	return &policyBarrier{aead: aead}
}

func (b *policyBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	// #nosec G407 -- cipher.NewGCMWithRandomNonce samples its own nonce and
	// prepends it; passing anything but nil here makes Seal panic.
	return b.aead.Seal(nil, nil, plaintext, nil), nil
}

func (b *policyBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	return b.aead.Open(nil, nil, ciphertext, nil)
}

func (b *policyBarrier) IsSealed() bool { return false }

// billingPolicy lets billing hold certs:delegate, kv:get and kv:put.
func billingPolicy() *service.DelegationPolicyRequest {
	return &service.DelegationPolicyRequest{Principal: "billing", Scope: []string{"certs:delegate", "kv:get", "kv:put"}, TTL: 30 * time.Minute}
}

func anchorCaller(tenantID, subject string) context.Context {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: subject, TenantID: tenantID})
	return tenant.WithTenantID(ctx, tenantID)
}

// servicePrincipalCtx is the embedder's server-owned work, running as the
// named service principal in the system tenant.
func servicePrincipalCtx(name string) context.Context {
	id := auth.NewServicePrincipal(name, tenantid.System, nil)
	return tenant.WithTenantID(auth.WithIdentity(context.Background(), id), tenantid.System)
}

func delegationCSR(t *testing.T) []byte {
	t.Helper()
	_, csrPEM, err := certtest.NewCSR("ignored")
	if err != nil {
		t.Fatal(err)
	}
	return csrPEM
}

func findEvent(evs []audit.AuditEvent, action string) *audit.AuditEvent {
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Action == action {
			return &evs[i]
		}
	}
	return nil
}

// delegatedCtx is a caller presenting a delegation of g.Tenant.
func delegatedCtx(t *testing.T, g delegation.Grant) context.Context {
	t.Helper()
	id := authtest.NewDelegation(t, g).Identity(t)
	return tenant.WithTenantID(auth.WithIdentity(context.Background(), id), g.Tenant)
}

func mintedGrant(t *testing.T, certPEM []byte) (delegation.Grant, *x509.Certificate) {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("minted certificate is not PEM")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	g, found, err := delegation.FromCertificate(leaf)
	if !found || err != nil {
		t.Fatalf("minted certificate carries no delegation: found %v, err %v", found, err)
	}
	return g, leaf
}

func TestDelegationPolicy_ADelegatedCallerNeverWritesIt(t *testing.T) {
	svc := newPolicyService(t)
	delegated := delegatedCtx(t, delegation.Grant{Tenant: "acme", Principal: "billing", Scope: []string{"certs:delegate"}})
	var denied *service.DelegationDeniedError
	if _, err := svc.PutDelegationPolicy(delegated, billingPolicy()); !errors.As(err, &denied) {
		t.Fatalf("a delegated writer: err = %v, want *service.DelegationDeniedError", err)
	}
	if err := svc.DeleteDelegationPolicy(delegated, "", "billing"); !errors.As(err, &denied) {
		t.Fatalf("a delegated deleter: err = %v, want *service.DelegationDeniedError", err)
	}
	var cross *tenant.CrossTenantDeniedError
	if _, err := svc.MintDelegation(delegated, &service.MintDelegationRequest{TenantID: "globex", CSRPEM: delegationCSR(t)}); !errors.As(err, &cross) {
		t.Fatalf("a delegation minting in another tenant: err = %v, want *tenant.CrossTenantDeniedError", err)
	}
}

func TestMintDelegation_WhileThePolicyStands(t *testing.T) {
	ring := audit.NewRingBuffer(16)
	svc := newPolicyService(t, service.WithEnrollmentAuditLogger(ring))
	admin := anchorCaller("acme", "alice")
	sp := servicePrincipalCtx("billing")
	csr := delegationCSR
	mint := &service.MintDelegationRequest{TenantID: "acme", CSRPEM: csr(t)}

	var notFound *service.DelegationPolicyNotFoundError
	if _, err := svc.MintDelegation(sp, mint); !errors.As(err, &notFound) {
		t.Fatalf("mint before the policy: err = %v, want *service.DelegationPolicyNotFoundError", err)
	}
	policy := billingPolicy()
	if _, err := svc.PutDelegationPolicy(admin, policy); err != nil {
		t.Fatalf("PutDelegationPolicy: %v", err)
	}

	// The first mint, in process: the whole policy.
	issued, err := svc.MintDelegation(sp, mint)
	if err != nil {
		t.Fatalf("MintDelegation: %v", err)
	}
	g, leaf := mintedGrant(t, issued.CertPEM)
	if g.Tenant != "acme" || g.Principal != "billing" || !slices.Equal(g.Scope, policy.Scope) || leaf.NotAfter.Sub(leaf.NotBefore) != policy.TTL {
		t.Fatalf("minted %+v for %s", g, leaf.NotAfter.Sub(leaf.NotBefore))
	}
	if ev := findEvent(ring.Snapshot(), service.ActionCertDelegationMint); ev == nil || ev.TenantID != "acme" || ev.Attributes["principal"] != "billing" {
		t.Fatalf("%s event = %+v", service.ActionCertDelegationMint, ev)
	}

	// The renewal, by the delegation itself through IssueDelegation: within
	// the policy it narrows, beyond it it is refused.
	renewing := delegatedCtx(t, g)
	renewed, err := svc.IssueDelegation(renewing, &service.IssueDelegationRequest{Scope: []string{"kv:get"}, TTL: 10 * time.Minute, CSRPEM: csr(t)})
	if err != nil {
		t.Fatalf("renewal: %v", err)
	}
	if rg, rleaf := mintedGrant(t, renewed.CertPEM); !slices.Equal(rg.Scope, []string{"kv:get"}) || rleaf.NotAfter.Sub(rleaf.NotBefore) != 10*time.Minute {
		t.Fatalf("renewed %+v", rg)
	}
	var reqErr *service.DelegationRequestError
	for name, req := range map[string]*service.IssueDelegationRequest{
		"a wider scope": {Scope: []string{"kv:delete"}, CSRPEM: csr(t)},
		"a longer life": {TTL: policy.TTL + time.Minute, CSRPEM: csr(t)},
	} {
		if _, err := svc.IssueDelegation(renewing, req); !errors.As(err, &reqErr) {
			t.Fatalf("renewal with %s: err = %v, want *service.DelegationRequestError", name, err)
		}
	}

	// Revoked by deleting the policy: the next mint, either way, is refused.
	if err := svc.DeleteDelegationPolicy(admin, "", "billing"); err != nil {
		t.Fatalf("DeleteDelegationPolicy: %v", err)
	}
	if _, err := svc.MintDelegation(sp, mint); !errors.As(err, &notFound) {
		t.Fatalf("mint after the delete: err = %v, want *service.DelegationPolicyNotFoundError", err)
	}
	if _, err := svc.IssueDelegation(renewing, &service.IssueDelegationRequest{CSRPEM: csr(t)}); !errors.As(err, &notFound) {
		t.Fatalf("renewal after the delete: err = %v, want *service.DelegationPolicyNotFoundError", err)
	}
}
