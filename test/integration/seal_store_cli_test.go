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
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// sealStoreGetDisplay mirrors transport.SealStoreGetResponse's wire JSON
// shape, decoded from `seal-store get --output json`.
type sealStoreGetDisplay struct {
	Name   string `json:"name"`
	Secret []byte `json:"secret"`
}

// sealStoreStatusDisplay mirrors transport.SealStoreStatusResponse's wire
// JSON shape, decoded from `seal-store status --output json`.
type sealStoreStatusDisplay struct {
	Available   bool     `json:"available"`
	SecretCount int      `json:"secret_count"`
	SecretNames []string `json:"secret_names"`
}

// TestSealStore_CLI_PerProtocol drives `qrdb seal-store put/get/list/status/
// reseal/delete` through the CLI binary over every network transport sharing
// one multiProtoNode, as the bootstrap admin (system-tenant Security Officer,
// holder of seal:store_manage).
// sealStoreSecretNotFoundText is the text of seal.ErrSealStoreSecretNotFound
// the CLI prints for a missing secret on every protocol.
const sealStoreSecretNotFoundText = "sealstore secret not found"

func TestSealStore_CLI_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			name := "cli-e2e-" + proto.name
			secret := "seal-store-cli-secret-" + proto.name

			out, err := runCLIProtoStdin(t, node, proto, secret, "seal-store", "put", name)
			if err != nil {
				t.Fatalf("[%s] seal-store put: %v\noutput: %s", proto.name, err, out)
			}

			out, err = runCLIProto(t, node, proto, "--output", "json", "seal-store", "get", name)
			if err != nil {
				t.Fatalf("[%s] seal-store get: %v\noutput: %s", proto.name, err, out)
			}
			var got sealStoreGetDisplay
			if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
				t.Fatalf("[%s] decode `seal-store get` JSON: %v\noutput: %s", proto.name, jerr, out)
			}
			if got.Name != name || string(got.Secret) != secret {
				t.Fatalf("[%s] seal-store get = {%q, %q}, want {%q, %q}", proto.name, got.Name, got.Secret, name, secret)
			}

			out, err = runCLIProto(t, node, proto, "seal-store", "list")
			if err != nil {
				t.Fatalf("[%s] seal-store list: %v\noutput: %s", proto.name, err, out)
			}
			if !slices.Contains(strings.Fields(out), name) {
				t.Fatalf("[%s] seal-store list lacks %q\noutput: %s", proto.name, name, out)
			}

			out, err = runCLIProto(t, node, proto, "--output", "json", "seal-store", "status")
			if err != nil {
				t.Fatalf("[%s] seal-store status: %v\noutput: %s", proto.name, err, out)
			}
			var status sealStoreStatusDisplay
			if jerr := json.Unmarshal([]byte(out), &status); jerr != nil {
				t.Fatalf("[%s] decode `seal-store status` JSON: %v\noutput: %s", proto.name, jerr, out)
			}
			if !status.Available || !slices.Contains(status.SecretNames, name) {
				t.Fatalf("[%s] seal-store status = %+v, want available with %q", proto.name, status, name)
			}

			if out, err = runCLIProto(t, node, proto, "seal-store", "reseal", name); err != nil {
				t.Fatalf("[%s] seal-store reseal: %v\noutput: %s", proto.name, err, out)
			}
			if out, err = runCLIProto(t, node, proto, "seal-store", "delete", name); err != nil {
				t.Fatalf("[%s] seal-store delete: %v\noutput: %s", proto.name, err, out)
			}
			// A missing secret is reported as the seal store's own
			// not-found refusal, with the same text on every protocol
			// (#1647).
			if out, err = runCLIProto(t, node, proto, "seal-store", "get", name); err == nil {
				t.Fatalf("[%s] seal-store get after delete succeeded, output: %s", proto.name, out)
			}
			if !strings.Contains(out, sealStoreSecretNotFoundText) || strings.Contains(out, "key not found") {
				t.Fatalf("[%s] seal-store get after delete output does not name the missing secret as %q\noutput: %s", proto.name, sealStoreSecretNotFoundText, out)
			}
		})
	}
}

// TestSealStore_CLI_DeniedWithoutStoreManage_PerProtocol drives `qrdb
// seal-store` as a tenant identity, which cannot hold seal:store_manage
// (ScopeCross), on every network transport: get, list, status, reseal and
// delete each fail as a permission denial that leaks no secret, and the
// admin's control secret is unchanged afterwards. runQRDB has no stdin form,
// so the refused put is covered by the SDK refusal test, not driven here.
func TestSealStore_CLI_DeniedWithoutStoreManage_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			control := "cli-deny-" + proto.name
			controlSecret := "seal-store-cli-control-" + proto.name

			if out, err := runCLIProtoStdin(t, node, proto, controlSecret, "seal-store", "put", control); err != nil {
				t.Fatalf("[%s] admin seal-store put: %v\noutput: %s", proto.name, err, out)
			}

			tenantID := fmt.Sprintf("sealdeny-%s-%d", proto.name, time.Now().UnixNano())
			createTenant(t, node, proto, tenantID, "Seal Store Denied", "prefix", false)
			cert, key := provisionTenantIdentity(t, node, tenantID, "seal-denied", []string{"admin:health"})
			conn := tenantCLIArgsForProto(node, proto, cert, key)

			for _, args := range [][]string{
				{"seal-store", "get", control},
				{"seal-store", "list"},
				{"seal-store", "status"},
				{"seal-store", "reseal", control},
				{"seal-store", "delete", control},
			} {
				verb := strings.Join(args, " ")
				out, err := runQRDB(t, node.cli, conn, args...)
				if err == nil {
					t.Fatalf("[%s] CLI %s succeeded for a caller without seal:store_manage: %s", proto.name, verb, out)
				}
				assertKVReadDeniedOutput(t, proto, verb, out, controlSecret)
			}

			out, err := runCLIProto(t, node, proto, "--output", "json", "seal-store", "get", control)
			if err != nil {
				t.Fatalf("[%s] admin seal-store get after refusals: %v\noutput: %s", proto.name, err, out)
			}
			var got sealStoreGetDisplay
			if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
				t.Fatalf("[%s] decode `seal-store get` JSON: %v\noutput: %s", proto.name, jerr, out)
			}
			if string(got.Secret) != controlSecret {
				t.Fatalf("[%s] control secret = %q after refused delete/reseal, want %q", proto.name, got.Secret, controlSecret)
			}
			if out, err := runCLIProto(t, node, proto, "seal-store", "delete", control); err != nil {
				t.Fatalf("[%s] admin seal-store delete: %v\noutput: %s", proto.name, err, out)
			}
		})
	}
}
