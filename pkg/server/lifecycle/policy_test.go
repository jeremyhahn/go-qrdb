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

package lifecycle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestBuildServices_PolicyRidesTheStoreResolver pins that a node with a
// StoreResolver serves the ABAC policy store, and that a stored policy is
// confined to its tenant.
func TestBuildServices_PolicyRidesTheStoreResolver(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()
	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:            host,
		WatchHub:        hub,
		DataDir:         t.TempDir(),
		KeyWrapRegistry: fakeSoftwareKeyWrapRegistry(),
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()
	if node.Policy == nil {
		t.Fatal("Policy is nil on a node with a StoreResolver")
	}

	ctx := tenant.WithTenantID(context.Background(), "acme")
	p := &abac.Policy{
		APIVersion: "abac.qrdb.io/v1",
		Kind:       abac.PolicyKind,
		Metadata:   abac.PolicyMetadata{Name: "deny-secrets"},
		Spec: abac.PolicySpec{
			Subjects: abac.SubjectSelector{Roles: []string{"auditor"}},
			Rules: []abac.Rule{{
				Name:      "deny-read",
				Effect:    abac.EffectDeny,
				Actions:   []string{"read"},
				Resources: []string{"/secrets/**"},
			}},
		},
	}
	if _, err := node.Policy.CreatePolicy(ctx, p); err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}
	got, err := node.Policy.GetPolicy(ctx, "", "deny-secrets")
	if err != nil || got.Metadata.Tenant != "acme" {
		t.Fatalf("GetPolicy = %+v, %v; want acme's policy", got, err)
	}
	other := tenant.WithTenantID(context.Background(), "globex")
	var notFound *service.PolicyNotFoundError
	if _, err := node.Policy.GetPolicy(other, "", "deny-secrets"); !errors.As(err, &notFound) {
		t.Fatalf("globex GetPolicy error = %v, want *service.PolicyNotFoundError", err)
	}
}
