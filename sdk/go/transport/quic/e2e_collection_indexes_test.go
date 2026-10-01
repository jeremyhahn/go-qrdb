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
	"context"
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestE2E_CreateCollection_CarriesIndexes covers go-qrdb#414 on the QUIC
// client: the definitions and the codec are encoded on the wire, and the
// state and definitions the server answers with are decoded.
func TestE2E_CreateCollection_CarriesIndexes(t *testing.T) {
	srv, tr := startE2EServer(t)
	var sent pb.CreateCollectionRequest
	srv.SetResponder("collection.Create", func(payload []byte) (uint16, []byte) {
		if err := proto.Unmarshal(payload, &sent); err != nil {
			return wire.StatusInvalidArgument, nil
		}
		return wire.StatusOK, mustMarshal(&pb.CollectionInfoResponse{
			Name: sent.GetName(), Database: sent.GetDatabase(), StorageCodec: sent.GetStorageCodec(),
			Indexes: sent.GetIndexes(), State: "dropping", StateSince: "2026-09-25T12:00:00Z",
		})
	})

	defs := []transport.CollectionIndexDefinition{{FieldName: "email", Unique: true}}
	info, err := tr.CreateCollection(context.Background(), transport.CreateCollectionRequest{
		Database: "db", Name: "users", StorageCodec: "json", Indexes: defs,
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if len(sent.GetIndexes()) != 1 || sent.GetIndexes()[0].GetFieldName() != "email" || sent.GetStorageCodec() != "json" {
		t.Fatalf("wire request: %+v", &sent)
	}
	if !reflect.DeepEqual(info.Indexes, defs) || info.State != transport.CollectionStateDropping || info.StateSince == "" {
		t.Fatalf("client received %+v", info)
	}
}
