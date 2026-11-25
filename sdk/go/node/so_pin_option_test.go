// Copyright (c) 2025 Jeremy Hahn
// SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Commercial

package node_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
)

// TestNode_StrictTenantCreateWithoutSOAuthenticatorNamesTheOption proves an
// embedded node built without server.WithSOAuthenticator refuses a strict
// tenant create with an error that names the option the embedder passes,
// and that errors.As still finds the bootstrap refusal.
func TestNode_StrictTenantCreateWithoutSOAuthenticatorNamesTheOption(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST, cfg.Protocols.GRPC, cfg.Protocols.QUIC = false, false, false
	cfg.Protocols.Unix, cfg.Protocols.MCP = false, false
	cfg.Auth.SeparationOfDuties = true

	n, err := node.NewStandaloneNode(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	_, csrPEM, err := certtest.NewCSR("acme-admin")
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}
	_, soCSRPEM, err := certtest.NewCSR("acme-so")
	if err != nil {
		t.Fatalf("NewCSR(SO): %v", err)
	}
	_, err = n.Services().TenantOnboarding.OnboardTenant(context.Background(), &service.OnboardTenantRequest{
		ID: "acme", Name: "acme",
		AdminUsername: "acme-admin", AdminCSRPEM: csrPEM,
		SOUsername: "acme-so", SOCSRPEM: soCSRPEM, SOPIN: []byte("acme-so-pin-1234"),
	})
	if err == nil {
		t.Fatal("a strict tenant create without server.WithSOAuthenticator succeeded")
	}
	t.Logf("refusal: %v", err)
	if !strings.Contains(err.Error(), "server.WithSOAuthenticator") {
		t.Fatalf("refusal %q does not name server.WithSOAuthenticator", err)
	}
	var seam *bootstrap.MissingProvisioningSeamError
	if !errors.As(err, &seam) {
		t.Fatalf("errors.As found no *bootstrap.MissingProvisioningSeamError in %v", err)
	}
}
