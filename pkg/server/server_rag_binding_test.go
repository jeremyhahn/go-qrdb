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

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// bindingRAGService records the registry the server binds, applying the bind
// options to a real binding so a test can observe what they do.
type bindingRAGService struct {
	service.RAGServicer
	bound    service.Registry
	bindings int
	binding  *service.RAGBinding
}

func (b *bindingRAGService) BindRegistry(reg service.Registry, opts ...service.RAGBindOption) {
	b.bound = reg
	b.bindings++
	b.binding = service.NewRAGBinding()
	b.binding.Bind(reg, opts...)
}

// Start binds the RAG pipeline to the registry the transports resolve
// through, so RAG records are subject to the same seal, ownership and
// isolation; a node with no database registry leaves it unbound.
func TestStart_BindsRAGToTheTransportRegistry(t *testing.T) {
	with := &bindingRAGService{}
	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{},
		WithCeremonyCA(testServerCA), WithRAG(with), WithDatabaseServiceRegistry(newFilledRegistry()))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	srv.Stop()
	if with.bindings != 1 || with.bound == nil {
		t.Fatalf("bound %d times to %v, want once to the transport registry", with.bindings, with.bound)
	}
	if _, err := with.bound.KVService("system", "default"); err != nil {
		t.Fatalf("the bound registry does not resolve the default database: %v", err)
	}

	without := &bindingRAGService{}
	srv = New(testConfig(t, false, false), newMockKVService(), &mockAdminService{},
		WithCeremonyCA(testServerCA), WithRAG(without))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	srv.Stop()
	if without.bindings != 1 || without.bound != nil {
		t.Fatalf("a node without a database registry bound %v, want an unbound RAG pipeline", without.bound)
	}
}

// Only a node without request authentication lets a RAG call that carries no
// tenant act for the system tenant; with authentication such a call is
// refused, because every transport binds the caller's tenant.
func TestStart_RAGTenantForUnauthenticatedCalls(t *testing.T) {
	open := &bindingRAGService{}
	srv := New(testConfig(t, false, false), newMockKVService(), &mockAdminService{},
		WithCeremonyCA(testServerCA), WithRAG(open), WithDatabaseServiceRegistry(newFilledRegistry()))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	srv.Stop()
	if _, err := open.binding.KV(tenant.WithDatabase(context.Background(), "default")); err != nil {
		t.Fatalf("unauthenticated node, call with no tenant: %v", err)
	}

	authed := &bindingRAGService{}
	srv = New(testConfig(t, false, false), newMockKVService(), &mockAdminService{},
		WithCeremonyCA(testServerCA), WithRAG(authed), WithDatabaseServiceRegistry(newFilledRegistry()),
		WithAuthMiddleware(authmw.New(withoutPermissions{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	srv.Stop()
	var required *service.RAGTenantRequiredError
	if _, err := authed.binding.KV(tenant.WithDatabase(context.Background(), "default")); !errors.As(err, &required) {
		t.Fatalf("authenticated node, call with no tenant = %v, want *service.RAGTenantRequiredError", err)
	}
}
