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

//go:build qrdb_embedded && !integration

package main

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestSealStore_DesktopBinding_Lifecycle proves the desktop build's embedded
// binding serves the seal store (ruling 1234: seal-store data is
// embedded-only). Before StartEmbedded passed embedded.WithSealStore every
// verb answered "SealStore must not be nil".
func TestSealStore_DesktopBinding_Lifecycle(t *testing.T) {
	m := newTestConnectionManager(t)
	m.ctx = context.Background()
	profile := ConnectionProfile{
		ID:   "local-seal-store",
		Name: "Local Seal Store",
		Type: connectionTypeEmbedded,
		Embedded: &EmbeddedConfig{
			DataDir:        t.TempDir(),
			StorageBackend: "memory",
			SealConfig:     &SealConfig{Mode: "none"},
		},
	}
	if err := m.SaveProfile(profile); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if _, err := m.Connect(profile.ID); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = m.Disconnect(profile.ID) })
	if err := m.SetActive(profile.ID); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	client, err := m.ActiveClient()
	if err != nil {
		t.Fatalf("ActiveClient: %v", err)
	}

	ctx := context.Background()
	ss := client.SealStore()
	const name = "desktop-secret"
	secret := []byte("desktop seal-store value")

	if err := ss.SealStorePut(ctx, &transport.SealStorePutRequest{Name: name, Secret: secret}); err != nil {
		t.Fatalf("SealStorePut: %v", err)
	}
	got, err := ss.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: name})
	if err != nil {
		t.Fatalf("SealStoreGet: %v", err)
	}
	if !bytes.Equal(got.Secret, secret) {
		t.Fatalf("SealStoreGet secret = %q, want %q", got.Secret, secret)
	}
	list, err := ss.SealStoreList(ctx)
	if err != nil {
		t.Fatalf("SealStoreList: %v", err)
	}
	if !slices.Contains(list.Names, name) {
		t.Fatalf("SealStoreList names = %v, want %q", list.Names, name)
	}
	status, err := ss.SealStoreStatus(ctx)
	if err != nil {
		t.Fatalf("SealStoreStatus: %v", err)
	}
	if !status.Available || status.SecretCount < 1 {
		t.Fatalf("SealStoreStatus = %+v, want available with a secret", status)
	}
	if err := ss.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: name}); err != nil {
		t.Fatalf("SealStoreReseal: %v", err)
	}
	if err := ss.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: name}); err != nil {
		t.Fatalf("SealStoreDelete: %v", err)
	}
	if _, err := ss.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: name}); err == nil {
		t.Fatal("SealStoreGet after delete: want an error, got nil")
	}
}
