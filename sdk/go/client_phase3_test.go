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

package sdk_test

import (
	"context"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// The Phase 3 SDK accessors (Permissions, RBAC, Factors, Login,
// Sessions, VolumePools) probe the underlying transport for the
// matching Provider interface. The embedded transport satisfies all
// of them; the SDK accessor must therefore return a non-nil typed
// client when the transport is open and nil after Close.

func newPhase3Client(t *testing.T) (*sdk.Client, *embedded.Transport) {
	t.Helper()
	tr, err := embedded.New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	client, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	return client, tr
}

func TestClient_Permissions_OpenAndClosed(t *testing.T) {
	client, _ := newPhase3Client(t)
	if client.Permissions() == nil {
		t.Fatal("expected non-nil PermissionClient on open client")
	}
	// Touch the underlying call to confirm it works through the SDK
	// surface end-to-end.
	if _, err := client.Permissions().List(context.Background()); err != nil {
		t.Errorf("Permissions().List: %v", err)
	}

	_ = client.Close()
	if client.Permissions() != nil {
		t.Error("expected nil PermissionClient after Close")
	}
}

func TestClient_RBAC_OpenAndClosed(t *testing.T) {
	client, _ := newPhase3Client(t)
	if client.RBAC() == nil {
		t.Fatal("expected non-nil RBACClient on open client")
	}
	_ = client.Close()
	if client.RBAC() != nil {
		t.Error("expected nil RBACClient after Close")
	}
}

func TestClient_Factors_OpenAndClosed(t *testing.T) {
	client, _ := newPhase3Client(t)
	if client.Factors() == nil {
		t.Fatal("expected non-nil FactorClient on open client")
	}
	_ = client.Close()
	if client.Factors() != nil {
		t.Error("expected nil FactorClient after Close")
	}
}

func TestClient_Login_OpenAndClosed(t *testing.T) {
	client, _ := newPhase3Client(t)
	if client.Login() == nil {
		t.Fatal("expected non-nil LoginClient on open client")
	}
	_ = client.Close()
	if client.Login() != nil {
		t.Error("expected nil LoginClient after Close")
	}
}

func TestClient_Sessions_OpenAndClosed(t *testing.T) {
	client, _ := newPhase3Client(t)
	if client.Sessions() == nil {
		t.Fatal("expected non-nil SessionClient on open client")
	}
	_ = client.Close()
	if client.Sessions() != nil {
		t.Error("expected nil SessionClient after Close")
	}
}

func TestClient_VolumePools_OpenAndClosed(t *testing.T) {
	client, _ := newPhase3Client(t)
	if client.VolumePools() == nil {
		t.Fatal("expected non-nil VolumePoolClient on open client")
	}
	_ = client.Close()
	if client.VolumePools() != nil {
		t.Error("expected nil VolumePoolClient after Close")
	}
}
