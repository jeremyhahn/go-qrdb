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

package rest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire/dbwiretest"
)

// raftRequestFixture is the Raft request the TypeScript SDK's contract test
// holds its DatabaseRaftConfig to. TestDatabaseRaftRequestWire_Fixture holds
// it to the Go SDK's encoding of dbwiretest.FullRaftRequest, which the REST
// handler decodes field for field.
var raftRequestFixture = filepath.Join("..", "..", "..", "sdk", "typescript", "test", "fixtures", "rest-database-raft-request.json")

// decodeRaftRequest decodes a Raft request body as the REST handlers do, but
// refusing unknown keys, so a key the SDK misspells fails here instead of
// being dropped.
func decodeRaftRequest(t *testing.T, body []byte) *raftConfigRequest {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var req raftConfigRequest
	if err := dec.Decode(&req); err != nil {
		t.Fatalf("REST cannot decode the Go SDK's Raft request: %v\n%s", err, body)
	}
	return &req
}

// The Go SDK's Raft request -- every field, discovery blocks and their
// secrets included -- arrives through REST as the configuration it describes.
func TestDatabaseRaftRequestWire_GoSDKRequestReachesREST(t *testing.T) {
	req := dbwiretest.FullRaftRequest()
	dbwiretest.AssertEveryFieldSet(t, req)
	dbwiretest.AssertEveryFieldSet(t, dbwiretest.FullRaftConfig(), "ShardID")

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	dbwiretest.AssertRaftConfig(t, "REST", raftConfigFromRequest(decodeRaftRequest(t, body)))
}

// The fixture the TypeScript SDK is held to is what the Go SDK sends.
func TestDatabaseRaftRequestWire_Fixture(t *testing.T) {
	body, err := json.MarshalIndent(dbwiretest.FullRaftRequest(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(body, &want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(raftRequestFixture)
	if err != nil {
		t.Fatalf("read %s: %v", raftRequestFixture, err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode %s: %v", raftRequestFixture, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s is not the Go SDK's encoding of dbwiretest.FullRaftRequest; replace it with\n%s", raftRequestFixture, body)
	}
	dbwiretest.AssertRaftConfig(t, "REST (fixture)", raftConfigFromRequest(decodeRaftRequest(t, data)))
}
