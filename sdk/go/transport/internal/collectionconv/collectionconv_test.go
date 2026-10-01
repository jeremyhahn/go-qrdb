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

package collectionconv

import (
	"reflect"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestToCreateRequest_CarriesIndexes(t *testing.T) {
	got := ToCreateRequest(transport.CreateCollectionRequest{
		Database: "db", Name: "users", IDStrategy: "increment", StorageCodec: "json",
		Indexes: []transport.CollectionIndexDefinition{{FieldName: "email", Unique: true}, {FieldName: "age", Sparse: true}},
	})
	if got.GetStorageCodec() != "json" || len(got.GetIndexes()) != 2 {
		t.Fatalf("request not carried: %+v", got)
	}
	if got.GetIndexes()[0].GetFieldName() != "email" || !got.GetIndexes()[0].GetUnique() || !got.GetIndexes()[1].GetSparse() {
		t.Fatalf("index definitions not carried: %+v", got.GetIndexes())
	}
	if len(ToCreateRequest(transport.CreateCollectionRequest{}).GetIndexes()) != 0 {
		t.Fatal("no definitions must encode none")
	}
}

func TestToCollectionInfo_CarriesIndexesAndState(t *testing.T) {
	got := ToCollectionInfo(&pb.CollectionInfoResponse{
		Name: "users", Database: "db", IdStrategy: "unix_nano", StorageCodec: "json",
		Indexes:    []*pb.CollectionIndexDefinition{{FieldName: "email", Unique: true}},
		State:      transport.CollectionStateCreating,
		StateSince: "2026-09-25T12:00:00Z",
	})
	want := &transport.CollectionInfo{
		Name: "users", Database: "db", IDStrategy: "unix_nano", StorageCodec: "json",
		Indexes:    []transport.CollectionIndexDefinition{{FieldName: "email", Unique: true}},
		State:      transport.CollectionStateCreating,
		StateSince: "2026-09-25T12:00:00Z",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
