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

package collectionpb

import (
	"reflect"
	"testing"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestCreateRequestFromProto_CarriesIndexes(t *testing.T) {
	got := CreateRequestFromProto(&pb.CreateCollectionRequest{
		Database: "db", Name: "users", IdStrategy: "increment", FieldHash: "f", StorageCodec: "json",
		Indexes: []*pb.CollectionIndexDefinition{{FieldName: "email", Unique: true}, {FieldName: "age", Sparse: true}},
	})
	want := &service.CreateCollectionRequest{
		Database: "db", Name: "users", IDStrategy: "increment", FieldHash: "f", StorageCodec: "json",
		Indexes: []service.IndexDefinition{{FieldName: "email", Unique: true}, {FieldName: "age", Sparse: true}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if CreateRequestFromProto(&pb.CreateCollectionRequest{}).Indexes != nil {
		t.Fatal("no indexes on the wire must mean none requested")
	}
}

func TestInfoToProto_CarriesIndexesAndState(t *testing.T) {
	since := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	got := InfoToProto(&service.CollectionInfo{
		Name: "users", Database: "db", IDStrategy: "unix_nano", StorageCodec: "json",
		Indexes:    []service.IndexDefinition{{FieldName: "email", Unique: true}},
		State:      service.CollectionStateDropping,
		StateSince: since,
	})
	if got.GetState() != "dropping" || got.GetStateSince() != since.Format(time.RFC3339Nano) {
		t.Fatalf("state not carried: %q %q", got.GetState(), got.GetStateSince())
	}
	if len(got.GetIndexes()) != 1 || got.GetIndexes()[0].GetFieldName() != "email" || !got.GetIndexes()[0].GetUnique() {
		t.Fatalf("indexes not carried: %+v", got.GetIndexes())
	}
	if got.GetStorageCodec() != "json" {
		t.Fatalf("storage codec not carried: %q", got.GetStorageCodec())
	}
	if InfoToProto(&service.CollectionInfo{}).GetStateSince() != "" {
		t.Fatal("an unset state time must not be rendered")
	}
}
