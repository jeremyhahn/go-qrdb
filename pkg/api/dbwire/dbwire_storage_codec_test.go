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

package dbwire_test

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// A database's storage codec is reported on every transport, in both wire
// shapes. QUIC and MCP reported it while they sent the internal record; the
// shared converter dropped it everywhere.
func TestInfo_ReportsTheStorageCodec(t *testing.T) {
	in := &database.DatabaseInfo{Name: "coded", StorageCodec: "msgpack"}

	encoded, err := json.Marshal(dbwire.Info(in))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"storageCodec":"msgpack"`) {
		t.Fatalf("JSON shape = %s, want storageCodec msgpack", encoded)
	}

	encoded, err = protojson.Marshal(dbwire.InfoProto(in))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ReplaceAll(string(encoded), " ", ""), `"storageCodec":"msgpack"`) {
		t.Fatalf("protobuf shape = %s, want storageCodec msgpack", encoded)
	}

	encoded, err = json.Marshal(dbwire.Info(&database.DatabaseInfo{Name: "plain"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "storageCodec") {
		t.Fatalf("a database with the default codec reports one: %s", encoded)
	}
}
