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

// MCP half of the go-qrdb#234 cross-transport verification, driven against the
// REAL service: stubbing it would assert only that the stub returned what it
// was told to.
package mcp

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func realUserFactorService(t *testing.T) service.UserAuthFactorServicer {
	t.Helper()
	svc, err := service.NewUserAuthFactorService(service.UserAuthFactorServiceConfig{
		KV: newMockKVService(),
	})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	return svc
}

// assertReservedRejectedTool checks the tool surfaced the refusal as an error
// naming the key and the prefix, rather than recording the forged claim.
func assertReservedRejectedTool(t *testing.T, err error, wantKey string) {
	t.Helper()
	if err == nil {
		t.Fatal("tool succeeded with a server-owned metadata key present")
	}
	var reserved *auth.ReservedMetadataKeyError
	if !errors.As(err, &reserved) {
		t.Fatalf("error = %v (%T), want it to wrap *auth.ReservedMetadataKeyError", err, err)
	}
	if reserved.Key != wantKey {
		t.Errorf("error names key %q, want %q", reserved.Key, wantKey)
	}
	if reserved.Prefix != auth.ReservedMetadataPrefix {
		t.Errorf("error names prefix %q, want %q", reserved.Prefix, auth.ReservedMetadataPrefix)
	}
}

func TestMCP_AddRefusesReservedMetadata(t *testing.T) {
	handler := handleUserAuthFactorAddTool(realUserFactorService(t))

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"user_id":        "42",
		"type":           "mtls",
		"credential_b64": base64.StdEncoding.EncodeToString([]byte("cred")),
		"metadata_json":  `{"attest_level":"hardware"}`,
	}
	_, err := handler(context.Background(), req)
	assertReservedRejectedTool(t, err, "attest_level")
}

func TestMCP_RotateRefusesReservedMetadata(t *testing.T) {
	svc := realUserFactorService(t)

	seed := &auth.AuthFactor{
		ID: "old", UserID: 42, Type: auth.AuthFactorMTLS, Credential: []byte("cred-old"),
	}
	if err := svc.Add(context.Background(), 42, seed); err != nil {
		t.Fatalf("seed Add: %v", err)
	}

	handler := handleUserAuthFactorRotateTool(svc)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"old_factor_id":  "old",
		"type":           "mtls",
		"credential_b64": base64.StdEncoding.EncodeToString([]byte("cred-new")),
		"metadata_json":  `{"attest_aaguid":"forged"}`,
	}
	_, err := handler(context.Background(), req)
	assertReservedRejectedTool(t, err, "attest_aaguid")
}

// TestMCP_AddAllowsOrdinaryMetadata is the control: a namespace, not a ban.
func TestMCP_AddAllowsOrdinaryMetadata(t *testing.T) {
	handler := handleUserAuthFactorAddTool(realUserFactorService(t))

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"user_id":        "42",
		"type":           "mtls",
		"credential_b64": base64.StdEncoding.EncodeToString([]byte("cred")),
		"metadata_json":  `{"vendor":"yubico"}`,
	}
	if _, err := handler(context.Background(), req); err != nil {
		t.Fatalf("Add with ordinary metadata: %v", err)
	}
}
