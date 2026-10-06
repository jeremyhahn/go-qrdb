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

// cert_enrollment_test_double_test.go holds the fakeCertEnrollment double
// shared by every test that needs a service.CertEnrollmentServicer: the
// embedded transport's IssueCert, EnrollCert, AddTrustAnchors,
// ListTrustAnchors and RemoveTrustAnchor all delegate to exactly this
// interface (not cert.CertProvider directly -- see
// sdk/go/transport/embedded/transport_auth.go and tenant.go), so a double at
// this boundary is what those bindings and CLI commands actually exercise.
//
// This file carries NO build constraint deliberately: it is used both by
// !integration-only callers (auth_bindings_coverage_test.go) and by callers
// with no constraint at all (tenant_test.go, certs enrol/csr CLI tests), and
// a helper's constraint must be a superset of every caller's -- the only
// constraint that is a superset of both is none at all.
package main

import (
	"context"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// fakeCertEnrollment is a faithful test double of
// service.CertEnrollmentServicer.
type fakeCertEnrollment struct {
	signFn         func(ctx context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error)
	enrollFn       func(ctx context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error)
	addAnchorsFn   func(ctx context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error)
	listAnchorsFn  func(ctx context.Context, tenantID string) ([]trustanchor.Anchor, error)
	removeAnchorFn func(ctx context.Context, tenantID, fingerprint string) error
}

var _ service.CertEnrollmentServicer = (*fakeCertEnrollment)(nil)

func (f *fakeCertEnrollment) SignCertificate(ctx context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
	if f.signFn != nil {
		return f.signFn(ctx, req)
	}
	return &service.EnrolledCertificate{
		UserID:     1001,
		Username:   req.Username,
		TenantID:   req.TenantID,
		CertPEM:    []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"),
		Serial:     "AABBCCDD",
		Thumbprint: "mock-thumbprint",
		NotAfter:   time.Now().Add(24 * time.Hour),
	}, nil
}

func (f *fakeCertEnrollment) EnrollCertificate(ctx context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
	if f.enrollFn != nil {
		return f.enrollFn(ctx, req)
	}
	return &service.EnrolledCertificate{
		UserID:     1001,
		Username:   req.Username,
		TenantID:   req.TenantID,
		CertPEM:    req.CertPEM,
		Thumbprint: "mock-thumbprint",
		NotAfter:   time.Now().Add(24 * time.Hour),
	}, nil
}

func (f *fakeCertEnrollment) AddTrustAnchors(ctx context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error) {
	if f.addAnchorsFn != nil {
		return f.addAnchorsFn(ctx, tenantID, bundlePEM)
	}
	return []trustanchor.Anchor{}, nil
}

func (f *fakeCertEnrollment) ListTrustAnchors(ctx context.Context, tenantID string) ([]trustanchor.Anchor, error) {
	if f.listAnchorsFn != nil {
		return f.listAnchorsFn(ctx, tenantID)
	}
	return []trustanchor.Anchor{}, nil
}

func (f *fakeCertEnrollment) RemoveTrustAnchor(ctx context.Context, tenantID, fingerprint string) error {
	if f.removeAnchorFn != nil {
		return f.removeAnchorFn(ctx, tenantID, fingerprint)
	}
	return nil
}

// newAuthTestConnectionBindingWithCertEnroll wires an embedded transport
// backed by certEnroll so CertBinding.Issue/Enroll and the trust-anchor
// bindings, which the embedded transport only reaches through
// service.CertEnrollmentServicer, can be exercised without a real CA.
func newAuthTestConnectionBindingWithCertEnroll(t *testing.T, certEnroll service.CertEnrollmentServicer) *ConnectionBinding {
	t.Helper()
	trans, err := embedded.New(newMockKVService(), newMockAdminService(), embedded.WithCertEnrollment(certEnroll))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl, err := sdk.NewClient(trans)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := cl.Connect(context.Background()); err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	conn := NewConnectionBinding(testLogger(t))
	conn.SetClient(cl)
	return conn
}

// newTestRootCmdCertEnroll creates a root command whose PersistentPreRunE
// connects an embedded SDK client backed by certEnroll instead of building a
// real network client -- the same override newTestRootCmdAuth uses, with a
// service.CertEnrollmentServicer double in place of newMockAuthTransport's.
func newTestRootCmdCertEnroll(certEnroll service.CertEnrollmentServicer) *cobra.Command {
	rootCmd := newRootCmd()

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Name() == "version" || cmd.Name() == "help" {
			return nil
		}

		format, _ := cmd.Root().PersistentFlags().GetString("output")
		if err := validateOutputFormat(format); err != nil {
			return err
		}
		outputFormat = format

		trans, err := embedded.New(newMockKVService(), newMockAdminService(), embedded.WithCertEnrollment(certEnroll))
		if err != nil {
			return err
		}
		c, err := sdk.NewClient(trans)
		if err != nil {
			return err
		}
		if err := c.Connect(cmd.Context()); err != nil {
			return err
		}

		client = c
		return nil
	}

	return rootCmd
}

// executeCertEnrollCmd runs the "ca"/"certs"/"tenant" command tree against
// an embedded client backed by certEnroll and returns captured stdout and
// any error.
func executeCertEnrollCmd(t *testing.T, certEnroll service.CertEnrollmentServicer, args ...string) (string, error) {
	t.Helper()
	root := newTestRootCmdCertEnroll(certEnroll)
	return executeCommand(root, args...)
}
