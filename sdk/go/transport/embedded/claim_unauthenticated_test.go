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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestEmbeddedClaim_WithoutACertificateIsUnauthenticated proves an in-process
// claim with no verified peer certificate on ctx fails with the service
// layer's ClientCertRequiredError, which classifies as unauthenticated: the
// class the network transports render as 401 and codes.Unauthenticated.
func TestEmbeddedClaim_WithoutACertificateIsUnauthenticated(t *testing.T) {
	ctx := context.Background()
	tr := newBarrierTransport(t, &mockBarrierService{})
	begin := &transport.ShareClaimBeginRequest{Username: "carol"}
	complete := &transport.ShareClaimCompleteRequest{Username: "carol"}
	calls := map[string]func() error{
		"claim-begin":       func() error { _, err := tr.ShareClaimBegin(ctx, begin); return err },
		"claim-complete":    func() error { _, err := tr.ShareClaimComplete(ctx, complete); return err },
		"recovery-begin":    func() error { _, err := tr.RecoveryShareClaimBegin(ctx, begin); return err },
		"recovery-complete": func() error { _, err := tr.RecoveryShareClaimComplete(ctx, complete); return err },
	}
	tenantTr, err := New(newMockKVService(), &mockAdminService{}, WithTenantService(&mockTenantService{}))
	if err != nil {
		t.Fatal(err)
	}
	calls["tenant-claim-begin"] = func() error {
		_, err := tenantTr.TenantBarrierShareClaimBegin(ctx, &transport.TenantShareClaimBeginRequest{TenantID: "acme", Username: "carol"})
		return err
	}
	calls["tenant-claim-complete"] = func() error {
		_, err := tenantTr.TenantBarrierShareClaimComplete(ctx, &transport.TenantShareClaimCompleteRequest{TenantID: "acme", Username: "carol"})
		return err
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			var certErr *service.ClientCertRequiredError
			if !errors.As(err, &certErr) {
				t.Fatalf("err = %v, want *service.ClientCertRequiredError", err)
			}
			if class := errclass.Classify(err); class != errclass.Unauthenticated {
				t.Fatalf("class = %v, want Unauthenticated", class)
			}
		})
	}
}
