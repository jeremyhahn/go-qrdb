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
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRESTCollection_CreateCollection_CarriesIndexes covers go-qrdb#414 on the
// REST client. The body is decoded exactly as the server's
// createCollectionRequest decodes it -- "indexes" as objects keyed
// field_name/unique/sparse, pinned server-side in
// pkg/api/rest.TestHandleCreateCollection_CarriesIndexes -- and the state and
// definitions in the answer are decoded.
func TestRESTCollection_CreateCollection_CarriesIndexes(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var body struct {
		StorageCodec string `json:"storage_codec"`
		Indexes      []struct {
			FieldName string `json:"field_name"`
			Unique    bool   `json:"unique"`
			Sparse    bool   `json:"sparse"`
		} `json:"indexes"`
	}
	rs.handle(exactPath(http.MethodPost, "/v1/collections"), func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{
			"name": "users", "database": "db", "state": "active", "state_since": "2026-09-25T12:00:00Z",
			"indexes": []map[string]any{{"field_name": "email", "unique": true}},
		})
	})

	info, err := rt.CreateCollection(context.Background(), transport.CreateCollectionRequest{
		Database: "db", Name: "users", StorageCodec: "json",
		Indexes: []transport.CollectionIndexDefinition{{FieldName: "email", Unique: true}},
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if body.StorageCodec != "json" || len(body.Indexes) != 1 || body.Indexes[0].FieldName != "email" || !body.Indexes[0].Unique {
		t.Fatalf("wire body: %+v", body)
	}
	want := []transport.CollectionIndexDefinition{{FieldName: "email", Unique: true}}
	if !reflect.DeepEqual(info.Indexes, want) || info.State != transport.CollectionStateActive || info.StateSince == "" {
		t.Fatalf("client received %+v", info)
	}
}
