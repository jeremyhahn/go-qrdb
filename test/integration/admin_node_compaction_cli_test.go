// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

//go:build integration

// admin_node_compaction_cli_test.go drives `qrdb admin node-compaction` and
// `qrdb admin update-node-compaction` (cmd/qrdb/admin_node_compaction.go)
// through the real client CLI binary against every transport, sharing one
// multiProtoNode. Both commands shipped after the last CLI-transport audit
// and had zero integration coverage of any kind.
package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestAdminNodeCompaction_CLI_PerProtocol drives the platform node-keyspace
// compaction policy (get/set, distinct from a database's own
// update-compaction) through the CLI binary against every transport
// sharing one multiProtoNode.
func TestAdminNodeCompaction_CLI_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testAdminNodeCompactionOverProtocol(t, node, proto)
		})
	}
}

func testAdminNodeCompactionOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	out, err := runCLIProto(t, node, proto, "--output", "json", "admin", "node-compaction")
	if err != nil {
		t.Fatalf("[%s] admin node-compaction: %v\noutput: %s", proto.name, err, out)
	}
	var policy struct {
		Compaction *transport.DatabaseCompactionConfig `json:"compaction,omitempty"`
	}
	if jerr := json.Unmarshal([]byte(out), &policy); jerr != nil {
		t.Fatalf("[%s] decode admin node-compaction JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if policy.Compaction != nil && policy.Compaction.Mode != "" {
		t.Fatalf("[%s] admin node-compaction: expected off by default, got %+v", proto.name, policy.Compaction)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "admin", "update-node-compaction",
		"--mode", "revision", "--retain-revisions", "9")
	if err != nil {
		t.Fatalf("[%s] admin update-node-compaction --mode revision: %v\noutput: %s", proto.name, err, out)
	}
	if jerr := json.Unmarshal([]byte(out), &policy); jerr != nil {
		t.Fatalf("[%s] decode admin update-node-compaction JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if policy.Compaction == nil || policy.Compaction.Mode != "revision" || policy.Compaction.RetainRevisions != 9 {
		t.Fatalf("[%s] admin update-node-compaction: compaction = %+v, want mode=revision retainRevisions=9", proto.name, policy.Compaction)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "admin", "node-compaction")
	if err != nil {
		t.Fatalf("[%s] admin node-compaction (after set): %v\noutput: %s", proto.name, err, out)
	}
	if jerr := json.Unmarshal([]byte(out), &policy); jerr != nil {
		t.Fatalf("[%s] decode admin node-compaction JSON (after set): %v\noutput: %s", proto.name, jerr, out)
	}
	if policy.Compaction == nil || policy.Compaction.Mode != "revision" || policy.Compaction.RetainRevisions != 9 {
		t.Fatalf("[%s] admin node-compaction (after set): compaction = %+v, want mode=revision retainRevisions=9", proto.name, policy.Compaction)
	}

	tableOut, err := runCLIProto(t, node, proto, "admin", "node-compaction")
	if err != nil {
		t.Fatalf("[%s] admin node-compaction (table): %v\noutput: %s", proto.name, err, tableOut)
	}
	if !strings.Contains(tableOut, "revision") {
		t.Fatalf("[%s] admin node-compaction (table): expected revision in output, got: %s", proto.name, tableOut)
	}

	out, err = runCLIProto(t, node, proto, "admin", "update-node-compaction")
	if err == nil {
		t.Fatalf("[%s] admin update-node-compaction with no --mode succeeded, want error (output: %s)", proto.name, out)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "admin", "update-node-compaction", "--mode", "off")
	if err != nil {
		t.Fatalf("[%s] admin update-node-compaction --mode off: %v\noutput: %s", proto.name, err, out)
	}
	var offPolicy struct {
		Compaction *transport.DatabaseCompactionConfig `json:"compaction,omitempty"`
	}
	if jerr := json.Unmarshal([]byte(out), &offPolicy); jerr != nil {
		t.Fatalf("[%s] decode admin update-node-compaction JSON (off): %v\noutput: %s", proto.name, jerr, out)
	}
	if offPolicy.Compaction != nil && offPolicy.Compaction.Mode != "" && offPolicy.Compaction.Mode != "off" {
		t.Fatalf("[%s] admin update-node-compaction --mode off: compaction = %+v, want off", proto.name, offPolicy.Compaction)
	}
}
