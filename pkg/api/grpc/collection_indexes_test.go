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

package grpc

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// recordingCollectionService records the create request it is handed and
// answers with the definitions and an active state. Only the verbs the tests
// below reach are implemented.
type recordingCollectionService struct {
	service.CollectionServicer
	got *service.CreateCollectionRequest
}

func (r *recordingCollectionService) CreateCollection(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	r.got = req
	return &service.CollectionInfo{
		Name: req.Name, Database: req.Database, Indexes: req.Indexes, StorageCodec: req.StorageCodec,
		State: service.CollectionStateActive,
	}, nil
}

// TestGRPC_CreateCollection_CarriesIndexes pins the gRPC (and, through the
// same service implementation, the Unix socket) half of go-qrdb#414.
func TestGRPC_CreateCollection_CarriesIndexes(t *testing.T) {
	rec := &recordingCollectionService{}
	client, cleanup := startTestServerFull(t, WithCollectionService(rec))
	defer cleanup()

	resp, err := client.CreateCollection(context.Background(), &pb.CreateCollectionRequest{
		Database: "db", Name: "users", StorageCodec: "json",
		Indexes: []*pb.CollectionIndexDefinition{{FieldName: "email", Unique: true}},
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	want := []service.IndexDefinition{{FieldName: "email", Unique: true}}
	if rec.got == nil || !reflect.DeepEqual(rec.got.Indexes, want) || rec.got.StorageCodec != "json" {
		t.Fatalf("request not carried: %+v", rec.got)
	}
	if resp.GetState() != "active" || len(resp.GetIndexes()) != 1 || resp.GetStorageCodec() != "json" {
		t.Fatalf("response not carried: %+v", resp)
	}
}

// TestMapServiceError_CollectionLifecycle pins the code of each collection
// lifecycle refusal, including an ownership refusal wrapped in the
// CollectionMetadataError a failed create reports.
func TestMapServiceError_CollectionLifecycle(t *testing.T) {
	cases := map[string]struct {
		err  error
		want codes.Code
	}{
		"not active":        {&service.CollectionNotActiveError{State: "dropping"}, codes.FailedPrecondition},
		"state conflict":    {&service.CollectionStateConflictError{Op: "activate"}, codes.Aborted},
		"document conflict": {&service.CollectionDocumentConflictError{ID: 1}, codes.Aborted},
		"invalid index":     {&service.CollectionInvalidIndexError{Reason: "x"}, codes.InvalidArgument},
		"ownership wrapped": {&service.CollectionMetadataError{Op: "create-index", Err: &index.OwnershipError{Name: "n"}}, codes.FailedPrecondition},
		"retract failed":    {&service.CollectionMetadataError{Op: "retract-write", Err: errors.Join(&service.CollectionNotActiveError{State: "dropping"}, errors.New("disk"))}, codes.Internal},
		"metadata":          {&service.CollectionMetadataError{Op: "save", Err: errors.New("disk")}, codes.Internal},
	}
	for name, tc := range cases {
		if got := status.Code(mapServiceError(tc.err)); got != tc.want {
			t.Errorf("%s: code %v, want %v", name, got, tc.want)
		}
	}
}

// ownedIndexRefuser records the owner an owner-checked drop claims and
// refuses it, as the apply path does for an index a collection owns.
type ownedIndexRefuser struct {
	index.IndexServicer
	claimed *string
}

func (o *ownedIndexRefuser) DropOwnedIndex(_ context.Context, name, owner string) error {
	*o.claimed = owner
	return &index.OwnershipError{Name: name, Owner: "collection:db/c#1", Requested: owner}
}

// TestGRPC_IndexDrop_RefusesACollectionOwnedIndex pins that the explicit
// index API drops as the explicit owner, so it cannot remove an index a
// collection owns, and that the refusal is FAILED_PRECONDITION.
func TestGRPC_IndexDrop_RefusesACollectionOwnedIndex(t *testing.T) {
	claimed := "unset"
	client, cleanup := startTestServerFull(t, WithIndex(&ownedIndexRefuser{claimed: &claimed}))
	defer cleanup()

	_, err := client.IndexDrop(context.Background(), &pb.IndexDropRequest{Name: "db.c.email"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v, want FailedPrecondition (%v)", status.Code(err), err)
	}
	if claimed != index.ExplicitOwner {
		t.Fatalf("the explicit drop claimed owner %q", claimed)
	}
}
