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

// This file is in the external test package because pkg/auth/authtest, the
// only maker of a delegated identity, imports this package.
package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestIssueDelegation_DelegatedCallerCannotDelegate: a caller acting under a
// delegation cannot issue one -- not even to itself, which would let a
// delegation outlive its own lifetime by renewing itself.
func TestIssueDelegation_DelegatedCallerCannotDelegate(t *testing.T) {
	g := delegation.Grant{Tenant: "acme", Principal: "billing", Scope: []string{"certs:delegate", "kv:get"}}
	id := authtest.NewDelegation(t, g).Identity(t)
	ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), id), "acme")
	_, csrPEM, err := certtest.NewCSR("ignored")
	if err != nil {
		t.Fatal(err)
	}
	// The refusal precedes the CA, so an uninitialized one is enough.
	svc := service.NewCertEnrollmentService(cert.NewDefaultProvider(kvstoretest.NewGuardedMemoryStore(), nil), nil, nil)

	for _, principal := range []string{g.Principal, "reports"} {
		got, err := svc.IssueDelegation(ctx, &service.IssueDelegationRequest{Principal: principal, Scope: []string{"kv:get"}, CSRPEM: csrPEM})
		var denied *service.DelegationDeniedError
		if got != nil || !errors.As(err, &denied) {
			t.Fatalf("delegated caller delegating to %s = %+v, %v; want *service.DelegationDeniedError", principal, got, err)
		}
	}
}
