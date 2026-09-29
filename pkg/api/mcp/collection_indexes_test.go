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

package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestHandleCollectionCreate_CarriesIndexes pins the MCP half of go-qrdb#414:
// a collection's index definitions and storage codec reach the service, and
// its lifecycle state comes back.
func TestHandleCollectionCreate_CarriesIndexes(t *testing.T) {
	svc := newMockCollectionService()
	var got *service.CreateCollectionRequest
	svc.createCollectionFn = func(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
		got = req
		return &service.CollectionInfo{
			Name: req.Name, Database: req.Database, Indexes: req.Indexes, State: service.CollectionStateActive,
			IndexOwner: "collection:mydb/users#1", Incarnation: "1",
		}, nil
	}

	result, err := handleCollectionCreate(svc)(context.Background(), buildCallToolRequest(t, "qrdb_collection_create", map[string]any{
		"database":      "mydb",
		"name":          "users",
		"storage_codec": "json",
		"indexes":       `[{"field_name":"email","unique":true},{"field_name":"age","sparse":true}]`,
	}))
	if err != nil {
		t.Fatalf("handleCollectionCreate: %v", err)
	}
	want := []service.IndexDefinition{{FieldName: "email", Unique: true}, {FieldName: "age", Sparse: true}}
	if got == nil || !reflect.DeepEqual(got.Indexes, want) || got.StorageCodec != "json" {
		t.Fatalf("request not carried: %+v", got)
	}
	text := resultText(t, result)
	var info service.CollectionInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	for _, internal := range []string{"index_owner", "incarnation"} {
		if strings.Contains(text, internal) {
			t.Errorf("the tool result publishes the server's %s: %s", internal, text)
		}
	}
	if info.State != service.CollectionStateActive || len(info.Indexes) != 2 {
		t.Fatalf("response lost the state or the indexes: %+v", info)
	}
}

// TestHandleCollectionCreate_MalformedIndexesRefused pins that a malformed
// declaration is refused rather than read as "no indexes": creating the
// collection without the unique constraint the caller asked for would be
// silent.
func TestHandleCollectionCreate_MalformedIndexesRefused(t *testing.T) {
	svc := newMockCollectionService()
	called := false
	svc.createCollectionFn = func(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
		called = true
		return &service.CollectionInfo{Name: req.Name}, nil
	}
	result, err := handleCollectionCreate(svc)(context.Background(), buildCallToolRequest(t, "qrdb_collection_create", map[string]any{
		"database": "mydb",
		"name":     "users",
		"indexes":  `{"field_name":"email"}`,
	}))
	if err != nil {
		t.Fatalf("handleCollectionCreate: %v", err)
	}
	if !result.IsError {
		t.Fatal("a malformed indexes argument must be an error result")
	}
	if called {
		t.Fatal("nothing may be created from a malformed declaration")
	}
}
