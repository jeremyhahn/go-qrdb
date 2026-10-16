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
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestCertDelegateTool_Success(t *testing.T) {
	notAfter := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	var got *service.IssueDelegationRequest
	svc := &fakeCertEnrollTool{
		delegFn: func(_ context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
			got = req
			return &service.IssuedDelegation{TenantID: "acme", Principal: req.Principal, Scope: req.Scope, CertPEM: []byte("cert"), Serial: "0A0B", NotAfter: notAfter}, nil
		},
	}

	// The scope arrives as a decoded JSON array, as it does off the wire.
	result, err := handleCertDelegate(svc, nil)(context.Background(), makeReq(map[string]any{
		"principal": "billing",
		"scope":     []any{"kv:get", "kv:put"},
		"csr_pem":   "csr-pem",
		"ttl":       "30m",
	}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got.Principal != "billing" || !slices.Equal(got.Scope, []string{"kv:get", "kv:put"}) ||
		string(got.CSRPEM) != "csr-pem" || got.TTL != 30*time.Minute {
		t.Fatalf("service request = %+v", got)
	}
	var out issuedDelegationResult
	if err := json.Unmarshal([]byte(resultText(t, result)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.TenantID != "acme" || out.Principal != "billing" || out.Serial != "0A0B" ||
		!slices.Equal(out.Scope, []string{"kv:get", "kv:put"}) || !out.NotAfter.Equal(notAfter) {
		t.Fatalf("result = %+v", out)
	}
}

func TestCertDelegateTool_InvalidTTL(t *testing.T) {
	result, err := handleCertDelegate(&fakeCertEnrollTool{}, nil)(context.Background(), makeReq(map[string]any{
		"principal": "billing",
		"scope":     []any{"kv:get"},
		"csr_pem":   "csr-pem",
		"ttl":       "soon",
	}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !result.IsError {
		t.Fatal("an invalid ttl is not an error result")
	}
}

func TestCertDelegateTool_ServiceError(t *testing.T) {
	denied := &service.DelegationDeniedError{Reason: "a delegated caller cannot delegate"}
	svc := &fakeCertEnrollTool{
		delegFn: func(context.Context, *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
			return nil, denied
		},
	}

	_, err := handleCertDelegate(svc, nil)(context.Background(), makeReq(map[string]any{
		"principal": "billing",
		"scope":     []any{"kv:get"},
		"csr_pem":   "csr-pem",
	}))
	var toolErr *ToolError
	if !errors.As(err, &toolErr) || toolErr.Tool != "qrdb_cert_delegate" || !errors.Is(err, denied) {
		t.Fatalf("err = %T: %v; want a qrdb_cert_delegate *ToolError wrapping the refusal", err, err)
	}
}
