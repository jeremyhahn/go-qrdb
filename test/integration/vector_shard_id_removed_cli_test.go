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

package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestVector_CLI_NoShardIDFlag_PerProtocol asserts over every transport that
// the vector CLI has no --shard-id flag: the database's binding decides the
// vector shard (OD-14 A). The same commands without the flag succeed.
func TestVector_CLI_NoShardIDFlag_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			ns := fmt.Sprintf("vecnoshard-%s-%d", proto.name, time.Now().UnixNano())
			create := []string{"vector", "namespace", "create",
				"--namespace", ns, "--dimension", "4", "--distance-metric", "cosine", "--index-type", "hnsw"}

			out, err := runCLIProto(t, node, proto, append(create, "--shard-id", "1")...)
			if err == nil {
				t.Fatalf("[%s] namespace create --shard-id 1 succeeded, output: %s", proto.name, out)
			}
			if !strings.Contains(out, "unknown flag: --shard-id") {
				t.Fatalf("[%s] namespace create --shard-id 1: want unknown flag, got: %s", proto.name, out)
			}

			if out, err = runCLIProto(t, node, proto, create...); err != nil {
				t.Fatalf("[%s] namespace create: %v\noutput: %s", proto.name, err, out)
			}
			if out, err = runCLIProto(t, node, proto, "vector", "upsert",
				"--namespace", ns, "--id", "1", "--vector", "1,0,0,0"); err != nil {
				t.Fatalf("[%s] upsert: %v\noutput: %s", proto.name, err, out)
			}
			out, err = runCLIProto(t, node, proto, "--output", "json", "vector", "search",
				"--namespace", ns, "--vector", "1,0,0,0", "--k", "1")
			if err != nil {
				t.Fatalf("[%s] search: %v\noutput: %s", proto.name, err, out)
			}
			var resp vectorSearchResponseJSON
			if jerr := json.Unmarshal([]byte(out), &resp); jerr != nil {
				t.Fatalf("[%s] decode search: %v\noutput: %s", proto.name, jerr, out)
			}
			if len(resp.Results) != 1 || resp.Results[0].ID != 1 {
				t.Fatalf("[%s] search results = %+v, want id=1", proto.name, resp.Results)
			}
			if out, err = runCLIProto(t, node, proto, "vector", "namespace", "delete", "--namespace", ns); err != nil {
				t.Fatalf("[%s] namespace delete: %v\noutput: %s", proto.name, err, out)
			}
		})
	}
}
