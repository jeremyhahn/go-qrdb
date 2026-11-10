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

package grpc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

func TestGRPC_GetCollectionInfo_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithCollectionService(newMockCollectionService()))
	defer cleanup()

	resp, err := client.GetCollectionInfo(context.Background(), &pb.GetCollectionInfoRequest{
		Database: "testdb",
		Name:     "docs",
	})
	if err != nil {
		t.Fatalf("GetCollectionInfo failed: %v", err)
	}
	if resp.GetName() != "docs" {
		t.Fatalf("expected name 'docs', got %q", resp.GetName())
	}
}

func TestGRPC_ListCollections_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithCollectionService(newMockCollectionService()))
	defer cleanup()

	resp, err := client.ListCollections(context.Background(), &pb.ListCollectionsRequest{Database: "testdb"})
	if err != nil {
		t.Fatalf("ListCollections failed: %v", err)
	}
	if len(resp.GetCollections()) != 1 {
		t.Fatalf("expected 1 collection, got %d", len(resp.GetCollections()))
	}
}

func TestGRPC_DropCollection_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithCollectionService(newMockCollectionService()))
	defer cleanup()

	if _, err := client.DropCollection(context.Background(), &pb.DropCollectionRequest{Database: "testdb", Name: "docs"}); err != nil {
		t.Fatalf("DropCollection failed: %v", err)
	}
}

func TestGRPC_InsertDocument_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithCollectionService(newMockCollectionService()))
	defer cleanup()

	resp, err := client.InsertDocument(context.Background(), &pb.InsertDocumentRequest{
		Database:   "testdb",
		Collection: "docs",
		Document:   `{"name":"test","value":42}`,
	})
	if err != nil {
		t.Fatalf("InsertDocument failed: %v", err)
	}
	if resp.GetId() != 1 {
		t.Fatalf("expected id 1, got %d", resp.GetId())
	}
}

func TestGRPC_GetDocument_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithCollectionService(newMockCollectionService()))
	defer cleanup()

	resp, err := client.GetDocument(context.Background(), &pb.GetDocumentRequest{
		Database:   "testdb",
		Collection: "docs",
		Id:         1,
	})
	if err != nil {
		t.Fatalf("GetDocument failed: %v", err)
	}
	if resp.GetId() != 1 {
		t.Fatalf("expected id 1, got %d", resp.GetId())
	}
	if !strings.Contains(resp.GetDocument(), `"key":"value"`) {
		t.Fatalf("expected the stored document JSON, got %q", resp.GetDocument())
	}
}

func TestGRPC_UpdateDocument_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithCollectionService(newMockCollectionService()))
	defer cleanup()

	_, err := client.UpdateDocument(context.Background(), &pb.UpdateDocumentRequest{
		Database:   "testdb",
		Collection: "docs",
		Id:         1,
		Document:   `{"name":"updated"}`,
	})
	if err != nil {
		t.Fatalf("UpdateDocument failed: %v", err)
	}
}

func TestGRPC_DeleteDocument_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithCollectionService(newMockCollectionService()))
	defer cleanup()

	_, err := client.DeleteDocument(context.Background(), &pb.DeleteDocumentRequest{
		Database:   "testdb",
		Collection: "docs",
		Id:         1,
	})
	if err != nil {
		t.Fatalf("DeleteDocument failed: %v", err)
	}
}

func TestGRPC_ListDocuments_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithCollectionService(newMockCollectionService()))
	defer cleanup()

	resp, err := client.ListDocuments(context.Background(), &pb.ListDocumentsRequest{
		Database:   "testdb",
		Collection: "docs",
		PageSize:   10,
	})
	if err != nil {
		t.Fatalf("ListDocuments failed: %v", err)
	}
	if len(resp.GetDocuments()) != 1 {
		t.Fatalf("expected 1 document, got %d", len(resp.GetDocuments()))
	}
	if resp.GetTotalCount() != 1 {
		t.Fatalf("expected total_count=1, got %d", resp.GetTotalCount())
	}
}

// TestGRPC_Document_InvalidJSONIsInvalidArgument checks that each document
// write handler refuses a body that is not JSON before the service sees it.
func TestGRPC_Document_InvalidJSONIsInvalidArgument(t *testing.T) {
	calls := map[string]func(pb.QRDBServiceClient) error{
		"InsertDocument": func(c pb.QRDBServiceClient) error {
			_, err := c.InsertDocument(context.Background(), &pb.InsertDocumentRequest{
				Database: "testdb", Collection: "docs", Document: "not-valid-json",
			})
			return err
		},
		"UpdateDocument": func(c pb.QRDBServiceClient) error {
			_, err := c.UpdateDocument(context.Background(), &pb.UpdateDocumentRequest{
				Database: "testdb", Collection: "docs", Id: 1, Document: "{{invalid}}",
			})
			return err
		},
	}
	client, cleanup := startTestServerFull(t, WithCollectionService(newMockCollectionService()))
	defer cleanup()
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call(client)
			if err == nil {
				t.Fatal("expected error for invalid JSON, got nil")
			}
			if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument, got %s", st.Code())
			}
		})
	}
}

// TestGRPC_Collection_NilService checks that every collection and document
// RPC answers Unimplemented when the server has no collection service.
func TestGRPC_Collection_NilService(t *testing.T) {
	calls := map[string]func(pb.QRDBServiceClient) error{
		"CreateCollection": func(c pb.QRDBServiceClient) error {
			_, err := c.CreateCollection(context.Background(), &pb.CreateCollectionRequest{Database: "db", Name: "c"})
			return err
		},
		"GetCollectionInfo": func(c pb.QRDBServiceClient) error {
			_, err := c.GetCollectionInfo(context.Background(), &pb.GetCollectionInfoRequest{Database: "db", Name: "c"})
			return err
		},
		"ListCollections": func(c pb.QRDBServiceClient) error {
			_, err := c.ListCollections(context.Background(), &pb.ListCollectionsRequest{Database: "db"})
			return err
		},
		"DropCollection": func(c pb.QRDBServiceClient) error {
			_, err := c.DropCollection(context.Background(), &pb.DropCollectionRequest{Database: "db", Name: "c"})
			return err
		},
		"InsertDocument": func(c pb.QRDBServiceClient) error {
			_, err := c.InsertDocument(context.Background(), &pb.InsertDocumentRequest{
				Database: "db", Collection: "c", Document: `{}`,
			})
			return err
		},
		"GetDocument": func(c pb.QRDBServiceClient) error {
			_, err := c.GetDocument(context.Background(), &pb.GetDocumentRequest{Database: "db", Collection: "c", Id: 1})
			return err
		},
		"UpdateDocument": func(c pb.QRDBServiceClient) error {
			_, err := c.UpdateDocument(context.Background(), &pb.UpdateDocumentRequest{
				Database: "db", Collection: "c", Id: 1, Document: `{}`,
			})
			return err
		},
		"DeleteDocument": func(c pb.QRDBServiceClient) error {
			_, err := c.DeleteDocument(context.Background(), &pb.DeleteDocumentRequest{Database: "db", Collection: "c", Id: 1})
			return err
		},
		"ListDocuments": func(c pb.QRDBServiceClient) error {
			_, err := c.ListDocuments(context.Background(), &pb.ListDocumentsRequest{Database: "db", Collection: "c"})
			return err
		},
	}
	client, cleanup := startTestServerFull(t)
	defer cleanup()
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			requireUnimplemented(t, call(client))
		})
	}
}

func TestDocumentUnmarshalError_Error(t *testing.T) {
	msg := (&DocumentUnmarshalError{Err: errors.New("json error")}).Error()
	if !strings.Contains(msg, "unmarshal") || !strings.Contains(msg, "json error") {
		t.Fatalf("expected the operation and the cause in %q", msg)
	}
}

func TestDocumentUnmarshalError_Unwrap(t *testing.T) {
	inner := errors.New("inner")
	if !errors.Is(&DocumentUnmarshalError{Err: inner}, inner) {
		t.Fatal("Unwrap did not return inner error")
	}
}

func TestDocumentMarshalError_Error(t *testing.T) {
	msg := (&DocumentMarshalError{Err: errors.New("encode failed")}).Error()
	if !strings.Contains(msg, "marshal") || !strings.Contains(msg, "encode failed") {
		t.Fatalf("expected the operation and the cause in %q", msg)
	}
}

func TestDocumentMarshalError_Unwrap(t *testing.T) {
	inner := errors.New("inner")
	if !errors.Is(&DocumentMarshalError{Err: inner}, inner) {
		t.Fatal("Unwrap did not return inner error")
	}
}
