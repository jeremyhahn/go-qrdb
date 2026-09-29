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

package unix

import (
	"context"
	"reflect"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestUnixRT_CollectionIndexesSurviveTheRoundTrip covers go-qrdb#414 on the
// Unix socket: definitions reach the service and the lifecycle state comes
// back, through the real server.
func TestUnixRT_CollectionIndexesSurviveTheRoundTrip(t *testing.T) {
	var got *service.CreateCollectionRequest
	svc := &mockCollectionSvc{
		createFn: func(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
			got = req
			return &service.CollectionInfo{
				Name: req.Name, Database: req.Database, Indexes: req.Indexes, StorageCodec: req.StorageCodec,
				State: service.CollectionStateActive,
			}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCollectionService(svc))
	defer cleanup()

	defs := []transport.CollectionIndexDefinition{{FieldName: "email", Unique: true}}
	info, err := tr.CreateCollection(context.Background(), transport.CreateCollectionRequest{
		Database: "main", Name: "users", StorageCodec: "json", Indexes: defs,
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if got == nil || !reflect.DeepEqual(got.Indexes, []service.IndexDefinition{{FieldName: "email", Unique: true}}) || got.StorageCodec != "json" {
		t.Fatalf("server received %+v", got)
	}
	if !reflect.DeepEqual(info.Indexes, defs) || info.State != transport.CollectionStateActive {
		t.Fatalf("client received %+v", info)
	}
}
