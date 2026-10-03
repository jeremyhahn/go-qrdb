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

package quic

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire/dbwiretest"
)

// The Go SDK's Raft request -- every field, discovery blocks and their
// secrets included -- arrives through QUIC's request DTO as the configuration
// it describes. Unknown keys are refused, so a key the SDK misspells fails
// here instead of being dropped.
func TestDatabaseRaftRequest_GoSDKRequestReachesQUIC(t *testing.T) {
	body, err := json.Marshal(dbwiretest.FullRaftRequest())
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var req databaseRaftRequest
	if err := dec.Decode(&req); err != nil {
		t.Fatalf("QUIC cannot decode the Go SDK's Raft request: %v\n%s", err, body)
	}
	dbwiretest.AssertRaftConfig(t, "QUIC", req.toDomain())
}
