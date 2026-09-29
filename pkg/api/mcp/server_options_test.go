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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package mcp

import (
	"testing"
)

// ---------------------------------------------------------------------------
// WithUserService
// ---------------------------------------------------------------------------

func TestServer_WithUserService(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	userSvc := &mockUserService{}

	s := NewServer(kvSvc, adminSvc, ":0", WithUserService(userSvc))

	if s.userSvc != userSvc {
		t.Error("user service not set on server")
	}

	// User tools should be registered when both user and role services are set.
	// Without roleSvc, user tools should still be registered alone.
	// Check that user management tool keys are present.
	registeredTools := s.MCPServer().ListTools()
	if _, ok := registeredTools["qrdb_user_create"]; !ok {
		t.Error("qrdb_user_create not registered")
	}
	if _, ok := registeredTools["qrdb_user_get"]; !ok {
		t.Error("qrdb_user_get not registered")
	}
}

// ---------------------------------------------------------------------------
// WithRoleService
// ---------------------------------------------------------------------------

func TestServer_WithRoleService(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	userSvc := &mockUserService{}
	roleSvc := &mockRoleService{}

	s := NewServer(kvSvc, adminSvc, ":0",
		WithUserService(userSvc),
		WithRoleService(roleSvc),
	)

	if s.roleSvc != roleSvc {
		t.Error("role service not set on server")
	}
	if s.userSvc != userSvc {
		t.Error("user service not set on server")
	}

	registeredTools := s.MCPServer().ListTools()
	if _, ok := registeredTools["qrdb_role_create"]; !ok {
		t.Error("qrdb_role_create not registered")
	}
	if _, ok := registeredTools["qrdb_role_get"]; !ok {
		t.Error("qrdb_role_get not registered")
	}
}

func TestServer_WithRoleService_NoUserService(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	roleSvc := &mockRoleService{}

	// Role tools require userSvc too - should not register without it.
	s := NewServer(kvSvc, adminSvc, ":0", WithRoleService(roleSvc))

	if s.roleSvc != roleSvc {
		t.Error("role service not set on server")
	}

	// Role tools should NOT be registered without user service.
	registeredTools := s.MCPServer().ListTools()
	if _, ok := registeredTools["qrdb_role_create"]; ok {
		t.Error("qrdb_role_create should NOT be registered without user service")
	}
}

// ---------------------------------------------------------------------------
// WithCertProvider
// ---------------------------------------------------------------------------

func TestServer_WithCertProvider(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	certProv := &mockCertProvider{}

	s := NewServer(kvSvc, adminSvc, ":0", WithCertProvider(certProv))

	if s.certProv != certProv {
		t.Error("cert provider not set on server")
	}

	registeredTools := s.MCPServer().ListTools()
	if _, ok := registeredTools["qrdb_cert_generate"]; !ok {
		t.Error("qrdb_cert_generate not registered")
	}
	if _, ok := registeredTools["qrdb_cert_export"]; !ok {
		t.Error("qrdb_cert_export not registered")
	}
	if _, ok := registeredTools["qrdb_cert_list"]; !ok {
		t.Error("qrdb_cert_list not registered")
	}
}
