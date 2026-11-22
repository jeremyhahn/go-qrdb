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
	"slices"
	"strings"
	"testing"
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
			if out, err = runCLIProto(t, node, proto, "seal-store", "get", name); err == nil {
				t.Fatalf("[%s] seal-store get after delete succeeded, output: %s", proto.name, out)
			}
		})
	}
}
