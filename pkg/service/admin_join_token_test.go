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

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

func TestAdminService_MintJoinToken_NotConfigured(t *testing.T) {
	svc := NewAdminService(&mockHost{})
	_, _, err := svc.(*AdminService).MintJoinToken(context.Background(), "node-1", time.Hour)
	var notConfigured *ClusterJoinTokenNotConfiguredError
	if !errors.As(err, &notConfigured) {
		t.Fatalf("MintJoinToken with no store configured = %v, want *ClusterJoinTokenNotConfiguredError", err)
	}
}

func TestAdminService_MintJoinToken_WithStateOption(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	svc := NewAdminService(&mockHost{}, WithClusterJoinTokenStore(store, "test.cluster"))
	admin := svc.(*AdminService)

	token, expiresAt, err := admin.MintJoinToken(context.Background(), "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	if token == "" {
		t.Fatal("MintJoinToken returned an empty token")
	}
	if !expiresAt.After(time.Now()) {
		t.Fatalf("expiresAt = %v, want a future time", expiresAt)
	}

	// The token is single-use: a second presentation is refused.
	boundURI, err := clusterenroll.ConsumeJoinToken(context.Background(), store, "test.cluster", token)
	if err != nil {
		t.Fatalf("ConsumeJoinToken (first use): %v", err)
	}
	if boundURI == "" {
		t.Fatal("ConsumeJoinToken returned an empty bound identity")
	}
	if _, err := clusterenroll.ConsumeJoinToken(context.Background(), store, "test.cluster", token); err == nil {
		t.Fatal("ConsumeJoinToken (second use) succeeded, want refusal")
	}
}

func TestAdminService_MintJoinToken_SetAfterConstruction(t *testing.T) {
	// Mirrors cmd/qrdbd's actual call pattern: AdminService is constructed
	// before the replicated KV store exists, and SetClusterJoinTokenStore
	// binds the store once it does.
	svc := NewAdminService(&mockHost{})
	admin := svc.(*AdminService)
	if _, _, err := admin.MintJoinToken(context.Background(), "node-1", time.Hour); err == nil {
		t.Fatal("MintJoinToken before SetClusterJoinTokenStore succeeded, want *ClusterJoinTokenNotConfiguredError")
	}

	store := kvstore.NewMemoryKVStore()
	admin.SetClusterJoinTokenStore(store, "test.cluster")
	if _, _, err := admin.MintJoinToken(context.Background(), "node-1", time.Hour); err != nil {
		t.Fatalf("MintJoinToken after SetClusterJoinTokenStore: %v", err)
	}
}

func TestAdminService_MintJoinToken_InvalidTTLSurfacesTypedError(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	svc := NewAdminService(&mockHost{}, WithClusterJoinTokenStore(store, "test.cluster"))
	admin := svc.(*AdminService)

	_, _, err := admin.MintJoinToken(context.Background(), "node-1", time.Millisecond)
	var ttlErr *clusterenroll.JoinTokenTTLError
	if !errors.As(err, &ttlErr) {
		t.Fatalf("MintJoinToken with a too-short TTL = %v, want *clusterenroll.JoinTokenTTLError", err)
	}
}
