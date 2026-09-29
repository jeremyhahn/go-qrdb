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

package embedded

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestEmbeddedCollection_CreateCollection_CarriesIndexes covers go-qrdb#414
// on the embedded transport.
func TestEmbeddedCollection_CreateCollection_CarriesIndexes(t *testing.T) {
	var got *service.CreateCollectionRequest
	svc := &mockCollectionService{
		createFn: func(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
			got = req
			return &service.CollectionInfo{
				Name: req.Name, Database: req.Database, Indexes: req.Indexes,
				State: service.CollectionStateCreating, StateSince: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC),
			}, nil
		},
	}
	tr := newExtTransport(t, WithCollectionService(svc))

	defs := []transport.CollectionIndexDefinition{{FieldName: "email", Unique: true}}
	info, err := tr.CreateCollection(context.Background(), transport.CreateCollectionRequest{Database: "db", Name: "users", Indexes: defs})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if got == nil || !reflect.DeepEqual(got.Indexes, []service.IndexDefinition{{FieldName: "email", Unique: true}}) {
		t.Fatalf("service received %+v", got)
	}
	if !reflect.DeepEqual(info.Indexes, defs) || info.State != transport.CollectionStateCreating || info.StateSince == "" {
		t.Fatalf("client received %+v", info)
	}
	if toServiceIndexDefinitions(nil) != nil {
		t.Fatal("no definitions must convert to none")
	}
}
