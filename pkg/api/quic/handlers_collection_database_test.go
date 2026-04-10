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
	"encoding/json"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// handleCollectionCreate
// ---------------------------------------------------------------------------

func TestHandleCollectionCreate_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.CreateCollectionRequest{Database: "db1", Name: "col1"})
	_, err := srv.handleCollectionCreate(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError, got nil")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleCollectionCreate_Success(t *testing.T) {
	fixedTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	collectionSvc := &mockCollectionService{
		createCollectionFn: func(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
			return &service.CollectionInfo{
				Name:      req.Name,
				Database:  req.Database,
				CreatedAt: fixedTime,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))

	payload, _ := proto.Marshal(&pb.CreateCollectionRequest{Database: "db1", Name: "col1"})
	resp, err := srv.handleCollectionCreate(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var infoResp pb.CollectionInfoResponse
	if err := proto.Unmarshal(resp, &infoResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if infoResp.GetName() != "col1" {
		t.Errorf("expected name 'col1', got %q", infoResp.GetName())
	}
	if infoResp.GetDatabase() != "db1" {
		t.Errorf("expected database 'db1', got %q", infoResp.GetDatabase())
	}
}

func TestHandleCollectionCreate_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "create error"}
	collectionSvc := &mockCollectionService{
		createCollectionFn: func(_ context.Context, _ *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))
	payload, _ := proto.Marshal(&pb.CreateCollectionRequest{Database: "db", Name: "col"})
	_, err := srv.handleCollectionCreate(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleCollectionCreate_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(&mockCollectionService{}))
	_, err := srv.handleCollectionCreate(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCollectionGetInfo
// ---------------------------------------------------------------------------

func TestHandleCollectionGetInfo_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.GetCollectionInfoRequest{Database: "db", Name: "col"})
	_, err := srv.handleCollectionGetInfo(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleCollectionGetInfo_Success(t *testing.T) {
	collectionSvc := &mockCollectionService{
		getCollectionInfoFn: func(_ context.Context, db, name string) (*service.CollectionInfo, error) {
			return &service.CollectionInfo{Name: name, Database: db, DocumentCount: 42}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))

	payload, _ := proto.Marshal(&pb.GetCollectionInfoRequest{Database: "db1", Name: "col1"})
	resp, err := srv.handleCollectionGetInfo(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var infoResp pb.CollectionInfoResponse
	if err := proto.Unmarshal(resp, &infoResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if infoResp.GetDocumentCount() != 42 {
		t.Errorf("expected document_count 42, got %d", infoResp.GetDocumentCount())
	}
}

func TestHandleCollectionGetInfo_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "get info error"}
	collectionSvc := &mockCollectionService{
		getCollectionInfoFn: func(_ context.Context, _, _ string) (*service.CollectionInfo, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))
	payload, _ := proto.Marshal(&pb.GetCollectionInfoRequest{Database: "db", Name: "col"})
	_, err := srv.handleCollectionGetInfo(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleCollectionList
// ---------------------------------------------------------------------------

func TestHandleCollectionList_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.ListCollectionsRequest{Database: "db"})
	_, err := srv.handleCollectionList(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleCollectionList_Success(t *testing.T) {
	fixedTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	collectionSvc := &mockCollectionService{
		listCollectionsFn: func(_ context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
			return &service.ListCollectionsResponse{
				Collections: []service.CollectionInfo{
					{Name: "col1", Database: req.Database, CreatedAt: fixedTime},
					{Name: "col2", Database: req.Database, CreatedAt: fixedTime},
				},
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))

	payload, _ := proto.Marshal(&pb.ListCollectionsRequest{Database: "mydb"})
	resp, err := srv.handleCollectionList(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var listResp pb.ListCollectionsResponse
	if err := proto.Unmarshal(resp, &listResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(listResp.GetCollections()) != 2 {
		t.Errorf("expected 2 collections, got %d", len(listResp.GetCollections()))
	}
}

func TestHandleCollectionList_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "list error"}
	collectionSvc := &mockCollectionService{
		listCollectionsFn: func(_ context.Context, _ *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))
	payload, _ := proto.Marshal(&pb.ListCollectionsRequest{Database: "db"})
	_, err := srv.handleCollectionList(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleCollectionDrop
// ---------------------------------------------------------------------------

func TestHandleCollectionDrop_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.DropCollectionRequest{Database: "db", Name: "col"})
	_, err := srv.handleCollectionDrop(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleCollectionDrop_Success(t *testing.T) {
	called := false
	collectionSvc := &mockCollectionService{
		dropCollectionFn: func(_ context.Context, req *service.DropCollectionRequest) error {
			called = true
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))
	payload, _ := proto.Marshal(&pb.DropCollectionRequest{Database: "db", Name: "col"})
	resp, err := srv.handleCollectionDrop(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response")
	}
	if !called {
		t.Fatal("expected drop to be called")
	}
}

func TestHandleCollectionDrop_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "drop error"}
	collectionSvc := &mockCollectionService{
		dropCollectionFn: func(_ context.Context, _ *service.DropCollectionRequest) error { return svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))
	payload, _ := proto.Marshal(&pb.DropCollectionRequest{Database: "db", Name: "col"})
	_, err := srv.handleCollectionDrop(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleCollectionInsertDocument
// ---------------------------------------------------------------------------

func TestHandleCollectionInsertDocument_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.InsertDocumentRequest{Database: "db", Collection: "col", Document: `{"key":"val"}`})
	_, err := srv.handleCollectionInsertDocument(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleCollectionInsertDocument_Success(t *testing.T) {
	collectionSvc := &mockCollectionService{
		insertDocumentFn: func(_ context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
			return &service.InsertDocumentResponse{ID: 999}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))

	payload, _ := proto.Marshal(&pb.InsertDocumentRequest{
		Database: "db", Collection: "col",
		Document: `{"name":"alice","age":30}`,
	})
	resp, err := srv.handleCollectionInsertDocument(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var insertResp pb.InsertDocumentResponse
	if err := proto.Unmarshal(resp, &insertResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if insertResp.GetId() != 999 {
		t.Errorf("expected id 999, got %d", insertResp.GetId())
	}
}

func TestHandleCollectionInsertDocument_InvalidJSON(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(&mockCollectionService{}))
	payload, _ := proto.Marshal(&pb.InsertDocumentRequest{
		Database: "db", Collection: "col",
		Document: `{not-valid-json}`,
	})
	_, err := srv.handleCollectionInsertDocument(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for invalid JSON document")
	}
	var argErr *InvalidArgumentError
	if !errors.As(err, &argErr) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

func TestHandleCollectionInsertDocument_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "insert error"}
	collectionSvc := &mockCollectionService{
		insertDocumentFn: func(_ context.Context, _ *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))
	payload, _ := proto.Marshal(&pb.InsertDocumentRequest{Database: "db", Collection: "col", Document: `{"k":"v"}`})
	_, err := srv.handleCollectionInsertDocument(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleCollectionGetDocument
// ---------------------------------------------------------------------------

func TestHandleCollectionGetDocument_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.GetDocumentRequest{Database: "db", Collection: "col", Id: 1})
	_, err := srv.handleCollectionGetDocument(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleCollectionGetDocument_Success(t *testing.T) {
	collectionSvc := &mockCollectionService{
		getDocumentFn: func(_ context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
			return &service.GetDocumentResponse{
				ID:       req.ID,
				Document: map[string]interface{}{"name": "alice"},
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))

	payload, _ := proto.Marshal(&pb.GetDocumentRequest{Database: "db", Collection: "col", Id: 42})
	resp, err := srv.handleCollectionGetDocument(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var docResp pb.DocumentResponse
	if err := proto.Unmarshal(resp, &docResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if docResp.GetId() != 42 {
		t.Errorf("expected id 42, got %d", docResp.GetId())
	}
	if docResp.GetDocument() == "" {
		t.Error("expected non-empty document JSON")
	}
}

func TestHandleCollectionGetDocument_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "get error"}
	collectionSvc := &mockCollectionService{
		getDocumentFn: func(_ context.Context, _ *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))
	payload, _ := proto.Marshal(&pb.GetDocumentRequest{Database: "db", Collection: "col", Id: 1})
	_, err := srv.handleCollectionGetDocument(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleCollectionUpdateDocument
// ---------------------------------------------------------------------------

func TestHandleCollectionUpdateDocument_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.UpdateDocumentRequest{Database: "db", Collection: "col", Id: 1, Document: `{"k":"v"}`})
	_, err := srv.handleCollectionUpdateDocument(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleCollectionUpdateDocument_Success(t *testing.T) {
	called := false
	collectionSvc := &mockCollectionService{
		updateDocumentFn: func(_ context.Context, _ *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
			called = true
			return &service.UpdateDocumentResponse{}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))
	payload, _ := proto.Marshal(&pb.UpdateDocumentRequest{
		Database: "db", Collection: "col", Id: 1,
		Document: `{"name":"bob"}`,
	})
	resp, err := srv.handleCollectionUpdateDocument(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response")
	}
	if !called {
		t.Fatal("expected update to be called")
	}
}

func TestHandleCollectionUpdateDocument_InvalidJSON(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(&mockCollectionService{}))
	payload, _ := proto.Marshal(&pb.UpdateDocumentRequest{
		Database: "db", Collection: "col", Id: 1,
		Document: `not-json`,
	})
	_, err := srv.handleCollectionUpdateDocument(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	var argErr *InvalidArgumentError
	if !errors.As(err, &argErr) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

func TestHandleCollectionUpdateDocument_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "update error"}
	collectionSvc := &mockCollectionService{
		updateDocumentFn: func(_ context.Context, _ *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))
	payload, _ := proto.Marshal(&pb.UpdateDocumentRequest{Database: "db", Collection: "col", Id: 1, Document: `{"k":"v"}`})
	_, err := srv.handleCollectionUpdateDocument(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleCollectionDeleteDocument
// ---------------------------------------------------------------------------

func TestHandleCollectionDeleteDocument_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.DeleteDocumentRequest{Database: "db", Collection: "col", Id: 1})
	_, err := srv.handleCollectionDeleteDocument(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleCollectionDeleteDocument_Success(t *testing.T) {
	called := false
	collectionSvc := &mockCollectionService{
		deleteDocumentFn: func(_ context.Context, _ *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
			called = true
			return &service.DeleteDocumentResponse{Deleted: true}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))
	payload, _ := proto.Marshal(&pb.DeleteDocumentRequest{Database: "db", Collection: "col", Id: 7})
	resp, err := srv.handleCollectionDeleteDocument(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response")
	}
	if !called {
		t.Fatal("expected delete to be called")
	}
}

func TestHandleCollectionDeleteDocument_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "delete error"}
	collectionSvc := &mockCollectionService{
		deleteDocumentFn: func(_ context.Context, _ *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))
	payload, _ := proto.Marshal(&pb.DeleteDocumentRequest{Database: "db", Collection: "col", Id: 1})
	_, err := srv.handleCollectionDeleteDocument(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleCollectionListDocuments
// ---------------------------------------------------------------------------

func TestHandleCollectionListDocuments_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.ListDocumentsRequest{Database: "db", Collection: "col"})
	_, err := srv.handleCollectionListDocuments(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleCollectionListDocuments_Success(t *testing.T) {
	collectionSvc := &mockCollectionService{
		listDocumentsFn: func(_ context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
			return &service.ListDocumentsResponse{
				Documents: []service.GetDocumentResponse{
					{ID: 1, Document: map[string]interface{}{"key": "val1"}},
					{ID: 2, Document: map[string]interface{}{"key": "val2"}},
				},
				NextPageToken: "next-token",
				TotalCount:    2,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))

	payload, _ := proto.Marshal(&pb.ListDocumentsRequest{Database: "db", Collection: "col", PageSize: 10})
	resp, err := srv.handleCollectionListDocuments(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var listResp pb.ListDocumentsResponse
	if err := proto.Unmarshal(resp, &listResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(listResp.GetDocuments()) != 2 {
		t.Errorf("expected 2 documents, got %d", len(listResp.GetDocuments()))
	}
	if listResp.GetNextPageToken() != "next-token" {
		t.Errorf("expected next_page_token 'next-token', got %q", listResp.GetNextPageToken())
	}
	if listResp.GetTotalCount() != 2 {
		t.Errorf("expected total_count 2, got %d", listResp.GetTotalCount())
	}
}

func TestHandleCollectionListDocuments_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "list error"}
	collectionSvc := &mockCollectionService{
		listDocumentsFn: func(_ context.Context, _ *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(collectionSvc))
	payload, _ := proto.Marshal(&pb.ListDocumentsRequest{Database: "db", Collection: "col"})
	_, err := srv.handleCollectionListDocuments(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// collectionInfoToProto
// ---------------------------------------------------------------------------

func TestCollectionInfoToProto_FieldMapping(t *testing.T) {
	fixedTime := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	info := &service.CollectionInfo{
		Name:          "mycol",
		Database:      "mydb",
		IDStrategy:    "increment",
		FieldHash:     "id_field",
		DocumentCount: 100,
		CreatedAt:     fixedTime,
	}
	got := collectionInfoToProto(info)
	if got.GetName() != "mycol" {
		t.Errorf("expected name 'mycol', got %q", got.GetName())
	}
	if got.GetDatabase() != "mydb" {
		t.Errorf("expected database 'mydb', got %q", got.GetDatabase())
	}
	if got.GetIdStrategy() != "increment" {
		t.Errorf("expected id_strategy 'increment', got %q", got.GetIdStrategy())
	}
	if got.GetFieldHash() != "id_field" {
		t.Errorf("expected field_hash 'id_field', got %q", got.GetFieldHash())
	}
	if got.GetDocumentCount() != 100 {
		t.Errorf("expected document_count 100, got %d", got.GetDocumentCount())
	}
	if got.GetCreatedAt() == "" {
		t.Error("expected non-empty created_at")
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseCreate
// ---------------------------------------------------------------------------

func TestHandleDatabaseCreate_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(map[string]interface{}{"name": "mydb", "engine": "mvcc", "backend": "pebble"})
	_, err := srv.handleDatabaseCreate(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError, got nil")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleDatabaseCreate_Success(t *testing.T) {
	dbSvc := &mockDatabaseService{
		createFn: func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{Name: cfg.Name}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))

	payload, _ := json.Marshal(map[string]interface{}{
		"name": "testdb", "engine": "mvcc", "backend": "pebble",
	})
	resp, err := srv.handleDatabaseCreate(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var info database.DatabaseInfo
	if err := json.Unmarshal(resp, &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.Name != "testdb" {
		t.Errorf("expected name 'testdb', got %q", info.Name)
	}
}

func TestHandleDatabaseCreate_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "create error"}
	dbSvc := &mockDatabaseService{
		createFn: func(_ database.DatabaseConfig) (*database.DatabaseInfo, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))
	payload, _ := json.Marshal(map[string]interface{}{"name": "db", "engine": "mvcc", "backend": "pebble"})
	_, err := srv.handleDatabaseCreate(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleDatabaseCreate_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(&mockDatabaseService{}))
	_, err := srv.handleDatabaseCreate(context.Background(), []byte(`{not-valid`))
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseGet
// ---------------------------------------------------------------------------

func TestHandleDatabaseGet_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(map[string]interface{}{"name": "db"})
	_, err := srv.handleDatabaseGet(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleDatabaseGet_Success(t *testing.T) {
	dbSvc := &mockDatabaseService{
		getFn: func(name string) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{Name: name}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))
	payload, _ := json.Marshal(map[string]interface{}{"name": "testdb"})
	resp, err := srv.handleDatabaseGet(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var info database.DatabaseInfo
	if err := json.Unmarshal(resp, &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.Name != "testdb" {
		t.Errorf("expected name 'testdb', got %q", info.Name)
	}
}

func TestHandleDatabaseGet_EmptyName(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(&mockDatabaseService{}))
	payload, _ := json.Marshal(map[string]interface{}{"name": ""})
	_, err := srv.handleDatabaseGet(context.Background(), payload)
	if err == nil {
		t.Fatal("expected InvalidArgumentError for empty name")
	}
	var argErr *InvalidArgumentError
	if !errors.As(err, &argErr) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

func TestHandleDatabaseGet_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "get error"}
	dbSvc := &mockDatabaseService{
		getFn: func(_ string) (*database.DatabaseInfo, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))
	payload, _ := json.Marshal(map[string]interface{}{"name": "db"})
	_, err := srv.handleDatabaseGet(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseList
// ---------------------------------------------------------------------------

func TestHandleDatabaseList_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleDatabaseList(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleDatabaseList_Success(t *testing.T) {
	dbSvc := &mockDatabaseService{
		listFn: func() ([]database.DatabaseInfo, error) {
			return []database.DatabaseInfo{{Name: "db1"}, {Name: "db2"}}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))
	resp, err := srv.handleDatabaseList(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var infos []database.DatabaseInfo
	if err := json.Unmarshal(resp, &infos); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(infos) != 2 {
		t.Errorf("expected 2 databases, got %d", len(infos))
	}
}

func TestHandleDatabaseList_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "list error"}
	dbSvc := &mockDatabaseService{
		listFn: func() ([]database.DatabaseInfo, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))
	_, err := srv.handleDatabaseList(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseDrop
// ---------------------------------------------------------------------------

func TestHandleDatabaseDrop_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(map[string]interface{}{"name": "db"})
	_, err := srv.handleDatabaseDrop(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleDatabaseDrop_Success(t *testing.T) {
	called := false
	dbSvc := &mockDatabaseService{
		dropFn: func(name string) error { called = true; return nil },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))
	payload, _ := json.Marshal(map[string]interface{}{"name": "mydb"})
	resp, err := srv.handleDatabaseDrop(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response")
	}
	if !called {
		t.Fatal("expected drop to be called")
	}
}

func TestHandleDatabaseDrop_EmptyName(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(&mockDatabaseService{}))
	payload, _ := json.Marshal(map[string]interface{}{"name": ""})
	_, err := srv.handleDatabaseDrop(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	var argErr *InvalidArgumentError
	if !errors.As(err, &argErr) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

func TestHandleDatabaseDrop_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "drop error"}
	dbSvc := &mockDatabaseService{
		dropFn: func(_ string) error { return svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))
	payload, _ := json.Marshal(map[string]interface{}{"name": "db"})
	_, err := srv.handleDatabaseDrop(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseUpdateProtocol
// ---------------------------------------------------------------------------

func TestHandleDatabaseUpdateProtocol_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(map[string]interface{}{"name": "db", "mysql": true})
	_, err := srv.handleDatabaseUpdateProtocol(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleDatabaseUpdateProtocol_Success(t *testing.T) {
	dbSvc := &mockDatabaseService{
		updateProtocolFn: func(name string, protocol database.ProtocolConfig) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{Name: name}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))
	payload, _ := json.Marshal(map[string]interface{}{"name": "mydb", "mysql": true, "postgresql": false})
	resp, err := srv.handleDatabaseUpdateProtocol(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var info database.DatabaseInfo
	if err := json.Unmarshal(resp, &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.Name != "mydb" {
		t.Errorf("expected name 'mydb', got %q", info.Name)
	}
}

func TestHandleDatabaseUpdateProtocol_EmptyName(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(&mockDatabaseService{}))
	payload, _ := json.Marshal(map[string]interface{}{"name": "", "mysql": true})
	_, err := srv.handleDatabaseUpdateProtocol(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestHandleDatabaseUpdateProtocol_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "update error"}
	dbSvc := &mockDatabaseService{
		updateProtocolFn: func(_ string, _ database.ProtocolConfig) (*database.DatabaseInfo, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))
	payload, _ := json.Marshal(map[string]interface{}{"name": "db", "mysql": true})
	_, err := srv.handleDatabaseUpdateProtocol(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseUpdateEtcd
// ---------------------------------------------------------------------------

func TestHandleDatabaseUpdateEtcd_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(map[string]interface{}{"name": "db", "enabled": true, "listenAddr": ":2379"})
	_, err := srv.handleDatabaseUpdateEtcd(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleDatabaseUpdateEtcd_Success(t *testing.T) {
	dbSvc := &mockDatabaseService{
		updateEtcdFn: func(name string, etcd *database.EtcdConfig) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{Name: name}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))
	payload, _ := json.Marshal(map[string]interface{}{
		"name": "mydb", "enabled": true, "listenAddr": ":2379",
	})
	resp, err := srv.handleDatabaseUpdateEtcd(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var info database.DatabaseInfo
	if err := json.Unmarshal(resp, &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.Name != "mydb" {
		t.Errorf("expected name 'mydb', got %q", info.Name)
	}
}

func TestHandleDatabaseUpdateEtcd_EmptyName(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(&mockDatabaseService{}))
	payload, _ := json.Marshal(map[string]interface{}{"name": "", "enabled": true})
	_, err := srv.handleDatabaseUpdateEtcd(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseUpdateRaft
// ---------------------------------------------------------------------------

func TestHandleDatabaseUpdateRaft_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	raftCfg := database.DatabaseRaftConfig{ShardID: 1}
	payload, _ := json.Marshal(map[string]interface{}{"name": "db", "raft": raftCfg})
	_, err := srv.handleDatabaseUpdateRaft(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleDatabaseUpdateRaft_Success(t *testing.T) {
	dbSvc := &mockDatabaseService{
		updateRaftFn: func(name string, raft *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{Name: name}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))
	raftCfg := database.DatabaseRaftConfig{ShardID: 1}
	payload, _ := json.Marshal(map[string]interface{}{"name": "mydb", "raft": raftCfg})
	resp, err := srv.handleDatabaseUpdateRaft(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var info database.DatabaseInfo
	if err := json.Unmarshal(resp, &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.Name != "mydb" {
		t.Errorf("expected name 'mydb', got %q", info.Name)
	}
}

func TestHandleDatabaseUpdateRaft_EmptyName(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(&mockDatabaseService{}))
	raftCfg := database.DatabaseRaftConfig{ShardID: 1}
	payload, _ := json.Marshal(map[string]interface{}{"name": "", "raft": raftCfg})
	_, err := srv.handleDatabaseUpdateRaft(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestHandleDatabaseUpdateRaft_NilRaft(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(&mockDatabaseService{}))
	payload, _ := json.Marshal(map[string]interface{}{"name": "db"})
	_, err := srv.handleDatabaseUpdateRaft(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for nil raft config")
	}
	var argErr *InvalidArgumentError
	if !errors.As(err, &argErr) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseUpdateBarrier
// ---------------------------------------------------------------------------

func TestHandleDatabaseUpdateBarrier_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	barrierCfg := database.DatabaseBarrierConfig{Mode: "shamir"}
	payload, _ := json.Marshal(map[string]interface{}{"name": "db", "barrier": barrierCfg})
	_, err := srv.handleDatabaseUpdateBarrier(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleDatabaseUpdateBarrier_Success(t *testing.T) {
	dbSvc := &mockDatabaseService{
		updateBarrierFn: func(name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{Name: name}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))
	barrierCfg := database.DatabaseBarrierConfig{Mode: "shamir"}
	payload, _ := json.Marshal(map[string]interface{}{"name": "mydb", "barrier": barrierCfg})
	resp, err := srv.handleDatabaseUpdateBarrier(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var info database.DatabaseInfo
	if err := json.Unmarshal(resp, &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.Name != "mydb" {
		t.Errorf("expected name 'mydb', got %q", info.Name)
	}
}

func TestHandleDatabaseUpdateBarrier_NilBarrier(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(&mockDatabaseService{}))
	payload, _ := json.Marshal(map[string]interface{}{"name": "db"})
	_, err := srv.handleDatabaseUpdateBarrier(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for nil barrier config")
	}
	var argErr *InvalidArgumentError
	if !errors.As(err, &argErr) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseUpdateConnection
// ---------------------------------------------------------------------------

func TestHandleDatabaseUpdateConnection_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	connCfg := database.DatabaseConnectionConfig{Transport: "grpc", Address: "localhost:9000"}
	payload, _ := json.Marshal(map[string]interface{}{"name": "db", "connection": connCfg})
	_, err := srv.handleDatabaseUpdateConnection(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleDatabaseUpdateConnection_Success(t *testing.T) {
	dbSvc := &mockDatabaseService{
		updateConnectionFn: func(name string, conn *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{Name: name}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))
	connCfg := database.DatabaseConnectionConfig{Transport: "grpc", Address: "remotehost:9000"}
	payload, _ := json.Marshal(map[string]interface{}{"name": "mydb", "connection": connCfg})
	resp, err := srv.handleDatabaseUpdateConnection(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var info database.DatabaseInfo
	if err := json.Unmarshal(resp, &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.Name != "mydb" {
		t.Errorf("expected name 'mydb', got %q", info.Name)
	}
}

func TestHandleDatabaseUpdateConnection_NilConnection(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(&mockDatabaseService{}))
	payload, _ := json.Marshal(map[string]interface{}{"name": "db"})
	_, err := srv.handleDatabaseUpdateConnection(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for nil connection config")
	}
	var argErr *InvalidArgumentError
	if !errors.As(err, &argErr) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

func TestHandleDatabaseUpdateConnection_EmptyName(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(&mockDatabaseService{}))
	connCfg := database.DatabaseConnectionConfig{Transport: "grpc", Address: "h:9000"}
	payload, _ := json.Marshal(map[string]interface{}{"name": "", "connection": connCfg})
	_, err := srv.handleDatabaseUpdateConnection(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

// ---------------------------------------------------------------------------
// toDomain (via handleDatabaseCreate with etcd config)
// ---------------------------------------------------------------------------

func TestHandleDatabaseCreate_WithEtcdProtocol(t *testing.T) {
	var capturedCfg database.DatabaseConfig
	dbSvc := &mockDatabaseService{
		createFn: func(cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
			capturedCfg = cfg
			return &database.DatabaseInfo{Name: cfg.Name}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))

	payload, _ := json.Marshal(map[string]interface{}{
		"name":    "mydb",
		"engine":  "mvcc",
		"backend": "pebble",
		"protocol": map[string]interface{}{
			"mysql":      false,
			"postgresql": false,
			"etcd": map[string]interface{}{
				"enabled":    true,
				"listenAddr": ":2379",
			},
		},
	})
	_, err := srv.handleDatabaseCreate(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedCfg.Protocol.Etcd == nil {
		t.Fatal("expected etcd config to be set")
	}
	if !capturedCfg.Protocol.Etcd.Enabled {
		t.Error("expected etcd enabled=true")
	}
	if capturedCfg.Protocol.Etcd.ListenAddr != ":2379" {
		t.Errorf("expected listen_addr ':2379', got %q", capturedCfg.Protocol.Etcd.ListenAddr)
	}
}
