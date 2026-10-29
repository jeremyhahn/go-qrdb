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

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// countingClientTrust records every Refresh, answering err.
type countingClientTrust struct {
	calls int
	err   error
}

func (c *countingClientTrust) Refresh(context.Context) error {
	c.calls++
	return c.err
}

var _ ClientTrustRefresher = (*countingClientTrust)(nil)

// TestSignCertificate_SettlesClientTrustBeforeAnswering proves an issued
// certificate is answered only after this node's client trust is brought up
// to date: the issuance may have brought the tenant's login CA into use, and
// the caller's next handshake presents the certificate.
func TestSignCertificate_SettlesClientTrustBeforeAnswering(t *testing.T) {
	users := newCertEnrollmentUserStore(t)
	createTestAccount(t, users, "acme", "alice")
	trust := &countingClientTrust{}
	svc := NewCertEnrollmentService(newCertEnrollmentProvider(t), users, nil, WithEnrollmentClientTrust(trust))

	_, csrPEM, err := certtest.NewCSR("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SignCertificate(context.Background(), &SignCertificateRequest{TenantID: "acme", Username: "alice", CSRPEM: csrPEM, TTL: time.Hour}); err != nil {
		t.Fatalf("SignCertificate: %v", err)
	}
	if trust.calls != 1 {
		t.Fatalf("client trust settled %d times, want 1", trust.calls)
	}
}

// TestSignCertificate_RefusalSettlesNoClientTrust proves a refused issuance
// costs no trust rebuild: no certificate is answered.
func TestSignCertificate_RefusalSettlesNoClientTrust(t *testing.T) {
	trust := &countingClientTrust{}
	svc := NewCertEnrollmentService(newCertEnrollmentProvider(t), newCertEnrollmentUserStore(t), nil, WithEnrollmentClientTrust(trust))

	_, csrPEM, err := certtest.NewCSR("bob")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SignCertificate(context.Background(), &SignCertificateRequest{TenantID: "acme", Username: "bob", CSRPEM: csrPEM}); err == nil {
		t.Fatal("SignCertificate for a nonexistent account succeeded")
	}
	if trust.calls != 0 {
		t.Fatalf("a refused issuance settled client trust %d times, want 0", trust.calls)
	}
}

// TestSignCertificate_ClientTrustFailureStillAnswers proves a failed trust
// rebuild does not fail an issuance already committed: the pool's periodic
// rebuild admits the certificate.
func TestSignCertificate_ClientTrustFailureStillAnswers(t *testing.T) {
	users := newCertEnrollmentUserStore(t)
	createTestAccount(t, users, "acme", "alice")
	trust := &countingClientTrust{err: errors.New("rebuild failed")}
	svc := NewCertEnrollmentService(newCertEnrollmentProvider(t), users, nil, WithEnrollmentClientTrust(trust))

	_, csrPEM, err := certtest.NewCSR("alice")
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.SignCertificate(context.Background(), &SignCertificateRequest{TenantID: "acme", Username: "alice", CSRPEM: csrPEM})
	if err != nil || result == nil || len(result.CertPEM) == 0 {
		t.Fatalf("SignCertificate with a failing trust rebuild = %v, %v; want the certificate", result, err)
	}
	if trust.calls != 1 {
		t.Fatalf("client trust settled %d times, want 1", trust.calls)
	}
}

// TestEnrollCertificate_SettlesClientTrustBeforeAnswering proves an enrolled
// certificate is answered after client trust is brought up to date: the
// anchor it chains to was announced to the handshake asynchronously.
func TestEnrollCertificate_SettlesClientTrustBeforeAnswering(t *testing.T) {
	ctx := context.Background()
	users := newCertEnrollmentUserStore(t)
	createTestAccount(t, users, "acme", "alice")
	registry := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	root := newTestRoot(t, "acme-root")
	if _, err := registry.Add(ctx, "acme", pemOf(root.cert)); err != nil {
		t.Fatal(err)
	}
	trust := &countingClientTrust{}
	svc := NewCertEnrollmentService(nil, users, registry, WithEnrollmentClientTrust(trust))

	leaf := root.clientLeaf(t, "alice", "acme")
	if _, err := svc.EnrollCertificate(ctx, &EnrollCertificateRequest{TenantID: "acme", Username: "alice", CertPEM: pemOf(leaf)}); err != nil {
		t.Fatalf("EnrollCertificate: %v", err)
	}
	if trust.calls != 1 {
		t.Fatalf("client trust settled %d times, want 1", trust.calls)
	}
}

// TestWithEnrollmentClientTrust_NilLeavesTheHandshakeToThePool proves the
// default service settles nothing and still answers.
func TestWithEnrollmentClientTrust_NilLeavesTheHandshakeToThePool(t *testing.T) {
	users := newCertEnrollmentUserStore(t)
	createTestAccount(t, users, "acme", "alice")
	svc := NewCertEnrollmentService(newCertEnrollmentProvider(t), users, nil, WithEnrollmentClientTrust(nil))
	if svc.clientTrust != nil {
		t.Fatal("WithEnrollmentClientTrust(nil) set a refresher")
	}

	_, csrPEM, err := certtest.NewCSR("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SignCertificate(context.Background(), &SignCertificateRequest{TenantID: "acme", Username: "alice", CSRPEM: csrPEM}); err != nil {
		t.Fatalf("SignCertificate: %v", err)
	}
}
