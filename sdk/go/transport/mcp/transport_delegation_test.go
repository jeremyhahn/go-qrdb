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

package mcp

import (
	"context"
	"slices"
	"testing"
	"time"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestIssueDelegation_Success(t *testing.T) {
	notAfter := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	enroll := newMockCertEnrollmentServicer()
	enroll.delegationResp = &service.IssuedDelegation{
		TenantID: "t1", Principal: "billing", Scope: []string{"kv:get"},
		CertPEM: []byte("cert"), Serial: "d1", NotAfter: notAfter,
	}
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil, mcpapi.WithCertEnrollment(enroll))
	defer cleanup()

	got, err := tr.IssueDelegation(context.Background(), &transport.IssueDelegationRequest{
		Principal: "billing", Scope: []string{"kv:get"}, CSRPEM: "csr-bytes", TTL: "15m",
	})
	if err != nil {
		t.Fatalf("IssueDelegation: %v", err)
	}
	if got.TenantID != "t1" || got.Principal != "billing" || got.Serial != "d1" ||
		!slices.Equal(got.Scope, []string{"kv:get"}) || !got.NotAfter.Equal(notAfter) {
		t.Errorf("got %+v", got)
	}
	enroll.mu.Lock()
	defer enroll.mu.Unlock()
	req := enroll.lastDelegation
	if req == nil || req.Principal != "billing" || !slices.Equal(req.Scope, []string{"kv:get"}) ||
		string(req.CSRPEM) != "csr-bytes" || req.TTL != 15*time.Minute {
		t.Errorf("server received %+v; want Principal=billing Scope=[kv:get] CSRPEM=csr-bytes TTL=15m", req)
	}
}

// TestIssueDelegation_NoTTLIsOmitted: an unset lifetime reaches the service
// as zero, the server's default, not as a parse of "".
func TestIssueDelegation_NoTTLIsOmitted(t *testing.T) {
	enroll := newMockCertEnrollmentServicer()
	enroll.delegationResp = &service.IssuedDelegation{TenantID: "t1", Principal: "billing"}
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil, mcpapi.WithCertEnrollment(enroll))
	defer cleanup()

	if _, err := tr.IssueDelegation(context.Background(), &transport.IssueDelegationRequest{
		Principal: "billing", Scope: []string{"kv:get"}, CSRPEM: "csr-bytes",
	}); err != nil {
		t.Fatalf("IssueDelegation: %v", err)
	}
	enroll.mu.Lock()
	defer enroll.mu.Unlock()
	if enroll.lastDelegation == nil || enroll.lastDelegation.TTL != 0 {
		t.Errorf("server received %+v; want TTL 0", enroll.lastDelegation)
	}
}

func TestIssueDelegation_ServiceError(t *testing.T) {
	enroll := newMockCertEnrollmentServicer()
	enroll.delegationErr = &service.DelegationDeniedError{Reason: "a delegated caller cannot delegate"}
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil, mcpapi.WithCertEnrollment(enroll))
	defer cleanup()

	if _, err := tr.IssueDelegation(context.Background(), &transport.IssueDelegationRequest{
		Principal: "billing", Scope: []string{"kv:get"}, CSRPEM: "csr",
	}); err == nil {
		t.Fatal("expected error")
	}
}
