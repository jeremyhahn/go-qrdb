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
	"errors"
	"fmt"
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

// TestSealStore_SDK_DeniedWithoutStoreManage_PerProtocol proves the refusal
// path over a real qrdbd on every network transport: a tenant identity cannot
// hold seal:store_manage (ScopeCross), so every client.SealStore() call it
// makes returns the transport's typed permission denial and no secret or name
// data, and the refused delete and reseal leave the admin's control secret
// unchanged.
func TestSealStore_SDK_DeniedWithoutStoreManage_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			admin := sdkClientForProtocolAs(t, node, proto, node.clientCert, node.clientKey)
			defer admin.Close()
			if err := admin.Connect(ctx); err != nil {
				t.Fatalf("[%s] admin connect: %v", proto.name, err)
			}
			adminStore := admin.SealStore()

			control := "deny-" + proto.name
			controlSecret := []byte("seal-store-control-" + proto.name)
			refusedPut := "deny-put-" + proto.name
			if err := adminStore.SealStorePut(ctx, &transport.SealStorePutRequest{Name: control, Secret: controlSecret}); err != nil {
				t.Fatalf("[%s] admin SealStorePut: %v", proto.name, err)
			}

			tenantID := fmt.Sprintf("sealdeny-%s-%d", proto.name, time.Now().UnixNano())
			createTenant(t, node, proto, tenantID, "Seal Store Denied", "prefix", false)
			cert, key := provisionTenantIdentity(t, node, tenantID, "seal-denied", []string{"admin:health"})

			denied := sdkClientForProtocolAs(t, node, proto, cert, key)
			defer denied.Close()
			if err := denied.Connect(ctx); err != nil {
				t.Fatalf("[%s] tenant connect: %v", proto.name, err)
			}
			store := denied.SealStore()

			for name, call := range map[string]func() (bool, error){
				"put": func() (bool, error) {
					return false, store.SealStorePut(ctx, &transport.SealStorePutRequest{Name: refusedPut, Secret: []byte("refused")})
				},
				"get": func() (bool, error) {
					resp, err := store.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: control})
					return resp != nil && (len(resp.Secret) != 0 || resp.Name != ""), err
				},
				"list": func() (bool, error) {
					resp, err := store.SealStoreList(ctx)
					return resp != nil && len(resp.Names) != 0, err
				},
				"delete": func() (bool, error) {
					return false, store.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: control})
				},
				"reseal": func() (bool, error) {
					return false, store.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: control})
				},
				"status": func() (bool, error) {
					resp, err := store.SealStoreStatus(ctx)
					return resp != nil && len(resp.SecretNames) != 0, err
				},
			} {
				gotData, err := call()
				if gotData {
					t.Fatalf("[%s] SealStore %s returned data to a caller without seal:store_manage", proto.name, name)
				}
				var reqErr *transport.RequestError
				if !errors.As(err, &reqErr) {
					t.Fatalf("[%s] SealStore %s err = %v (%T), want the typed permission denial", proto.name, name, err, err)
				}
				if want := permissionDeniedStatus[proto.name]; reqErr.StatusCode != want {
					t.Fatalf("[%s] SealStore %s status = %d (%v), want permission denied (%d)", proto.name, name, reqErr.StatusCode, err, want)
				}
			}

			got, err := adminStore.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: control})
			if err != nil {
				t.Fatalf("[%s] admin SealStoreGet after refusals: %v", proto.name, err)
			}
			if !bytes.Equal(got.Secret, controlSecret) {
				t.Fatalf("[%s] control secret = %q after refused delete/reseal, want %q", proto.name, got.Secret, controlSecret)
			}
			list, err := adminStore.SealStoreList(ctx)
			if err != nil {
				t.Fatalf("[%s] admin SealStoreList: %v", proto.name, err)
			}
			if slices.Contains(list.Names, refusedPut) {
				t.Fatalf("[%s] refused put stored %q: names %v", proto.name, refusedPut, list.Names)
			}
			if err := adminStore.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: control}); err != nil {
				t.Fatalf("[%s] admin SealStoreDelete: %v", proto.name, err)
			}
		})
	}
}
