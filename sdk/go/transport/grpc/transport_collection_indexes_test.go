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
	"net"
	"reflect"
	"testing"

	googlegrpc "google.golang.org/grpc"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// indexEchoCollectionServicer records the create request and answers with
// the definitions it was given and a lifecycle state.
type indexEchoCollectionServicer struct {
	service.CollectionServicer
	got *service.CreateCollectionRequest
}

func (e *indexEchoCollectionServicer) CreateCollection(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	e.got = req
	return &service.CollectionInfo{
		Name: req.Name, Database: req.Database, Indexes: req.Indexes, StorageCodec: req.StorageCodec,
		State: service.CollectionStateCreating,
	}, nil
}

// TestGRPCCollection_IndexesSurviveTheRoundTrip covers go-qrdb#414 end to end
// on the gRPC plane: the REAL pkg/api/grpc service over a loopback listener,
// so the client, the wire message and the server mapping are covered in one
// pass.
func TestGRPCCollection_IndexesSurviveTheRoundTrip(t *testing.T) {
	rec := &indexEchoCollectionServicer{}
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil, grpcpkg.WithRegisterCollectionService(rec))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	tr, cleanup := connectLLITransport(t, lis.Addr().String())
	t.Cleanup(cleanup)

	defs := []transport.CollectionIndexDefinition{{FieldName: "email", Unique: true}, {FieldName: "age", Sparse: true}}
	info, err := tr.CreateCollection(context.Background(), transport.CreateCollectionRequest{
		Database: "db", Name: "users", StorageCodec: "json", Indexes: defs,
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	want := []service.IndexDefinition{{FieldName: "email", Unique: true}, {FieldName: "age", Sparse: true}}
	if rec.got == nil || !reflect.DeepEqual(rec.got.Indexes, want) || rec.got.StorageCodec != "json" {
		t.Fatalf("server received %+v", rec.got)
	}
	if !reflect.DeepEqual(info.Indexes, defs) || info.State != transport.CollectionStateCreating || info.StorageCodec != "json" {
		t.Fatalf("client received %+v", info)
	}
}
