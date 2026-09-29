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
	"reflect"
	"testing"
	"time"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// indexEchoCollectionSvc records the create request and answers with the
// definitions it was given and a lifecycle state.
type indexEchoCollectionSvc struct {
	service.CollectionServicer
	got *service.CreateCollectionRequest
}

func (e *indexEchoCollectionSvc) CreateCollection(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	e.got = req
	return &service.CollectionInfo{
		Name: req.Name, Database: req.Database, Indexes: req.Indexes, StorageCodec: req.StorageCodec,
		State: service.CollectionStateDropping, StateSince: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC),
	}, nil
}

// TestCollectionCreate_IndexesSurviveTheRoundTrip covers go-qrdb#414 on MCP,
// through the REAL MCP server in process.
func TestCollectionCreate_IndexesSurviveTheRoundTrip(t *testing.T) {
	rec := &indexEchoCollectionSvc{}
	srv := mcpapi.NewServer(newMockKVService(), newMockAdminService(), ":0", mcpapi.WithCollectionService(rec))
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = tr.Close() }()

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
	if !reflect.DeepEqual(info.Indexes, defs) || info.State != transport.CollectionStateDropping || info.StateSince == "" {
		t.Fatalf("client received %+v", info)
	}
}
