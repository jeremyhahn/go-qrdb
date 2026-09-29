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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestHandleCollectionCreate_CarriesIndexes pins the QUIC half of
// go-qrdb#414.
func TestHandleCollectionCreate_CarriesIndexes(t *testing.T) {
	var got *service.CreateCollectionRequest
	collectionSvc := &mockCollectionService{
		createCollectionFn: func(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
			got = req
			return &service.CollectionInfo{Name: req.Name, Database: req.Database, Indexes: req.Indexes, State: service.CollectionStateActive}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))

	payload, _ := proto.Marshal(&pb.CreateCollectionRequest{
		Database: "db1", Name: "col1", StorageCodec: "json",
		Indexes: []*pb.CollectionIndexDefinition{{FieldName: "email", Unique: true}},
	})
	resp, err := srv.handleCollectionCreate(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []service.IndexDefinition{{FieldName: "email", Unique: true}}
	if got == nil || !reflect.DeepEqual(got.Indexes, want) || got.StorageCodec != "json" {
		t.Fatalf("request not carried: %+v", got)
	}
	var info pb.CollectionInfoResponse
	if err := proto.Unmarshal(resp, &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.GetState() != "active" || len(info.GetIndexes()) != 1 {
		t.Fatalf("response not carried: %+v", &info)
	}
}

// TestServiceErrorStatus_CollectionLifecycle pins the wire status of each
// collection lifecycle refusal.
func TestServiceErrorStatus_CollectionLifecycle(t *testing.T) {
	cases := map[string]struct {
		err  error
		want uint16
	}{
		"not active":        {&service.CollectionNotActiveError{State: "dropping"}, wire.StatusFailedPrecondition},
		"state conflict":    {&service.CollectionStateConflictError{Op: "activate"}, wire.StatusAborted},
		"document conflict": {&service.CollectionDocumentConflictError{ID: 1}, wire.StatusAborted},
		"invalid index":     {&service.CollectionInvalidIndexError{Reason: "x"}, wire.StatusInvalidArgument},
		"ownership wrapped": {&service.CollectionMetadataError{Op: "create-index", Err: &index.OwnershipError{Name: "n"}}, wire.StatusFailedPrecondition},
		"retract failed":    {&service.CollectionMetadataError{Op: "retract-write", Err: errors.Join(&service.CollectionNotActiveError{State: "dropping"}, errors.New("disk"))}, wire.StatusError},
		"metadata":          {&service.CollectionMetadataError{Op: "save", Err: errors.New("disk")}, wire.StatusError},
	}
	for name, tc := range cases {
		if got, _ := mapServiceError(tc.err); got != tc.want {
			t.Errorf("%s: status %d, want %d", name, got, tc.want)
		}
	}
}
