// Copyright 2026 Jeremy Hahn
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

package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
)

// requireSecretArgError fails the test unless err carries the typed error
// secretArg refuses with: *SecretArgumentConflictError when both forms were
// sent, *ValueDecodeError when the _b64 form was not base64.
func requireSecretArgError(t *testing.T, err error, conflict bool) {
	t.Helper()
	if conflict {
		var ce *SecretArgumentConflictError
		if !errors.As(err, &ce) {
			t.Fatalf("err = %v, want *SecretArgumentConflictError", err)
		}
		return
	}
	var de *ValueDecodeError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *ValueDecodeError", err)
	}
}

// requireBothForms fails the test unless tool declares name in both of the
// forms withSecretArg declares.
func requireBothForms(t *testing.T, tool mcp.Tool, name string) {
	t.Helper()
	for _, n := range []string{name, name + secretB64Suffix} {
		if _, ok := tool.InputSchema.Properties[n]; !ok {
			t.Fatalf("%s: %s not declared", tool.Name, n)
		}
	}
}

// TestHolderSOPIN_B64: rekey and officer revocation read so_pin in either
// form, hand the service the exact bytes of so_pin_b64, and refuse both forms
// together or invalid base64 before the service sees a PIN.
func TestHolderSOPIN_B64(t *testing.T) {
	ctx := context.Background()
	names := []string{"qrdb_barrier_rekey_start", "qrdb_barrier_officer_revoke"}

	svc := &holderSvc{}
	tools := barrierHolderTools(svc)
	for _, st := range tools {
		for _, name := range names {
			if st.Tool.Name == name {
				requireBothForms(t, st.Tool, "so_pin")
			}
		}
	}
	for _, name := range names {
		if _, err := holderTool(t, tools, name)(ctx, buildCallToolRequest(t, name, map[string]any{"so_pin_b64": "/4Bzbw==", "username": "u"})); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if len(svc.soPINs) != 2 || svc.soPINs[0] != "\xff\x80so" || svc.soPINs[1] != "\xff\x80so" {
		t.Fatalf("SO PINs handed to the service = %q", svc.soPINs)
	}

	for _, tc := range []struct {
		args     map[string]any
		conflict bool
	}{
		{map[string]any{"so_pin": "a", "so_pin_b64": "YQ==", "username": "u"}, true},
		{map[string]any{"so_pin_b64": "not base64!", "username": "u"}, false},
	} {
		refused := &holderSvc{}
		tools := barrierHolderTools(refused)
		for _, name := range names {
			_, err := holderTool(t, tools, name)(ctx, buildCallToolRequest(t, name, tc.args))
			requireSecretArgError(t, err, tc.conflict)
		}
		if len(refused.soPINs) != 0 {
			t.Fatalf("a refused call reached the service: %q", refused.soPINs)
		}
	}
}

// TestDatabaseUnseal_B64Secret: qrdb_database_unseal reads secret in either
// form and unseals with the exact bytes of secret_b64.
func TestDatabaseUnseal_B64Secret(t *testing.T) {
	requireBothForms(t, toolDatabaseUnseal(), "secret")

	svc := newStubMCPDBWithBarrier()
	var got []byte
	svc.unsealFn = func(_ context.Context, _ string, creds seal.Credentials) error {
		got = sealtest.Snapshot(creds)
		return nil
	}
	req := buildCallToolRequest(t, "qrdb_database_unseal", map[string]any{"name": "mydb", "secret_b64": "/4A="})
	if _, err := handleDatabaseUnseal(svc)(context.Background(), req); err != nil {
		t.Fatalf("unseal: %v", err)
	}
	if string(got) != "\xff\x80" {
		t.Fatalf("secret = %x, want ff80", got)
	}
}

// TestDatabaseUnseal_RefusesBadSecretForms: both forms together, or a
// secret_b64 that is not base64, is a typed error and never reaches the
// service.
func TestDatabaseUnseal_RefusesBadSecretForms(t *testing.T) {
	for _, tc := range []struct {
		args     map[string]any
		conflict bool
	}{
		{map[string]any{"name": "mydb", "secret": "a", "secret_b64": "YQ=="}, true},
		{map[string]any{"name": "mydb", "secret_b64": "%%%"}, false},
	} {
		svc := newStubMCPDBWithBarrier()
		called := false
		svc.unsealFn = func(context.Context, string, seal.Credentials) error {
			called = true
			return nil
		}
		_, err := handleDatabaseUnseal(svc)(context.Background(), buildCallToolRequest(t, "qrdb_database_unseal", tc.args))
		requireSecretArgError(t, err, tc.conflict)
		if called {
			t.Fatal("a refused call reached the service")
		}
	}
}

// TestDatabaseCreate_B64BarrierSecret: qrdb_database_create accepts
// barrier_secret_b64 as a known argument and creates with its exact bytes.
func TestDatabaseCreate_B64BarrierSecret(t *testing.T) {
	requireBothForms(t, toolDatabaseCreate(), "barrier_secret")

	mock := &mcpCredentialDatabaseMock{}
	req := buildCallToolRequest(t, "qrdb_database_create", map[string]any{
		"name": "secured", "backend": "memory",
		"barrier_enabled": true, "barrier_mode": "passphrase",
		"barrier_secret_b64": "77u/cGFzcw==",
	})
	if _, err := handleDatabaseCreate(mock)(context.Background(), req); err != nil {
		t.Fatalf("create: %v", err)
	}
	if string(mock.gotSecret) != "\xef\xbb\xbfpass" {
		t.Fatalf("secret = %x, want the BOM-prefixed bytes", mock.gotSecret)
	}
}

// TestDatabaseCreate_RefusesBadBarrierSecretForms: both forms together, or
// invalid base64, is a typed error and creates nothing.
func TestDatabaseCreate_RefusesBadBarrierSecretForms(t *testing.T) {
	for _, tc := range []struct {
		extra    map[string]any
		conflict bool
	}{
		{map[string]any{"barrier_secret": "a", "barrier_secret_b64": "YQ=="}, true},
		{map[string]any{"barrier_secret_b64": "*"}, false},
	} {
		args := map[string]any{"name": "secured", "backend": "memory", "barrier_enabled": true, "barrier_mode": "passphrase"}
		for k, v := range tc.extra {
			args[k] = v
		}
		mock := &mcpCredentialDatabaseMock{}
		_, err := handleDatabaseCreate(mock)(context.Background(), buildCallToolRequest(t, "qrdb_database_create", args))
		requireSecretArgError(t, err, tc.conflict)
		if mock.credited {
			t.Fatal("a refused call created a database")
		}
	}
}
