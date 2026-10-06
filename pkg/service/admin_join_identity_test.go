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

func TestAdminService_RetireJoinIdentity_NotConfigured(t *testing.T) {
	svc := NewAdminService(&mockHost{})
	err := svc.(*AdminService).RetireJoinIdentity(context.Background(), "node-1", "")
	var notConfigured *ClusterJoinTokenNotConfiguredError
	if !errors.As(err, &notConfigured) {
		t.Fatalf("RetireJoinIdentity with no store configured = %v, want *ClusterJoinTokenNotConfiguredError", err)
	}
}

func TestAdminService_RetireJoinIdentity_WithStateOption(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	svc := NewAdminService(&mockHost{}, WithClusterJoinTokenStore(store, "test.cluster"))
	admin := svc.(*AdminService)

	if err := admin.RetireJoinIdentity(context.Background(), "node-1", "compromised key"); err != nil {
		t.Fatalf("RetireJoinIdentity: %v", err)
	}

	// The SAME join-identity ledger MintJoinToken consults now refuses it.
	_, _, err := admin.MintJoinToken(context.Background(), "node-1", time.Hour)
	var retired *clusterenroll.RetiredJoinIdentityError
	if !errors.As(err, &retired) {
		t.Fatalf("MintJoinToken for a retired identity = %v, want *clusterenroll.RetiredJoinIdentityError", err)
	}
}

func TestAdminService_RetireJoinIdentity_SetAfterConstruction(t *testing.T) {
	// Mirrors cmd/qrdbd's actual call pattern: AdminService is constructed
	// before the replicated KV store exists, and SetClusterJoinTokenStore
	// binds the store once it does -- the SAME call MintJoinToken uses, so
	// RetireJoinIdentity needs no separate wiring.
	svc := NewAdminService(&mockHost{})
	admin := svc.(*AdminService)
	if err := admin.RetireJoinIdentity(context.Background(), "node-1", ""); err == nil {
		t.Fatal("RetireJoinIdentity before SetClusterJoinTokenStore succeeded, want *ClusterJoinTokenNotConfiguredError")
	}

	store := kvstore.NewMemoryKVStore()
	admin.SetClusterJoinTokenStore(store, "test.cluster")
	if err := admin.RetireJoinIdentity(context.Background(), "node-1", ""); err != nil {
		t.Fatalf("RetireJoinIdentity after SetClusterJoinTokenStore: %v", err)
	}
}

func TestAdminService_RetireJoinIdentity_InvalidIdentitySurfacesTypedError(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	svc := NewAdminService(&mockHost{}, WithClusterJoinTokenStore(store, "test.cluster"))
	admin := svc.(*AdminService)

	err := admin.RetireJoinIdentity(context.Background(), "", "")
	var invalid *clusterenroll.InvalidJoinIdentityError
	if !errors.As(err, &invalid) {
		t.Fatalf("RetireJoinIdentity with an empty node id = %v, want *clusterenroll.InvalidJoinIdentityError", err)
	}
}

var _ JoinIdentityRetirer = (*AdminService)(nil)
