//go:build integration

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

package integration_test

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestSealStore_SDK_PerProtocol drives client.SealStore() over a real qrdbd
// on every network transport (REST, gRPC, QUIC, MCP, unix): put, get, list,
// reseal, status, delete, and a get of the deleted name that must fail.
// Subtests run sequentially against one node; each uses its own secret name.
func TestSealStore_SDK_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			client := sdkClientForProtocolAs(t, node, proto, node.clientCert, node.clientKey)
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := client.Connect(ctx); err != nil {
				t.Fatalf("[%s] connect: %v", proto.name, err)
			}

			store := client.SealStore()
			name := "e2e-" + proto.name
			secret := []byte("seal-store-secret-" + proto.name)

			if err := store.SealStorePut(ctx, &transport.SealStorePutRequest{Name: name, Secret: secret}); err != nil {
				t.Fatalf("[%s] SealStorePut: %v", proto.name, err)
			}

			got, err := store.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: name})
			if err != nil {
				t.Fatalf("[%s] SealStoreGet: %v", proto.name, err)
			}
			if !bytes.Equal(got.Secret, secret) {
				t.Fatalf("[%s] SealStoreGet secret = %q, want %q", proto.name, got.Secret, secret)
			}

			list, err := store.SealStoreList(ctx)
			if err != nil {
				t.Fatalf("[%s] SealStoreList: %v", proto.name, err)
			}
			if !slices.Contains(list.Names, name) {
				t.Fatalf("[%s] SealStoreList names %v lack %q", proto.name, list.Names, name)
			}

			if err := store.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: name}); err != nil {
				t.Fatalf("[%s] SealStoreReseal: %v", proto.name, err)
			}

			status, err := store.SealStoreStatus(ctx)
			if err != nil {
				t.Fatalf("[%s] SealStoreStatus: %v", proto.name, err)
			}
			if !status.Available {
				t.Fatalf("[%s] SealStoreStatus Available = false", proto.name)
			}
			if !slices.Contains(status.SecretNames, name) {
				t.Fatalf("[%s] SealStoreStatus secret names %v lack %q", proto.name, status.SecretNames, name)
			}

			if err := store.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: name}); err != nil {
				t.Fatalf("[%s] SealStoreDelete: %v", proto.name, err)
			}

			if _, err := store.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: name}); err == nil {
				t.Fatalf("[%s] SealStoreGet after delete succeeded, want an error", proto.name)
			}
		})
	}
}
