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
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestE2E_GetCollectionInfo_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("collection.GetInfo", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CollectionInfoResponse{
			Name: "users", Database: "db", DocumentCount: 5,
		})
	})
	info, err := tr.GetCollectionInfo(context.Background(), "db", "users")
	if err != nil {
		t.Fatalf("GetCollectionInfo: %v", err)
	}
	if info.DocumentCount != 5 {
		t.Fatalf("info = %+v", info)
	}
}

func TestE2E_ListCollections_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("collection.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.ListCollectionsResponse{
			Collections: []*pb.CollectionInfoResponse{
				{Name: "a", Database: "db"},
				{Name: "b", Database: "db"},
			},
		})
	})
	resp, err := tr.ListCollections(context.Background(), "db")
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(resp.Collections) != 2 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_DropCollection_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("collection.Drop", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.DropCollection(context.Background(), "db", "users"); err != nil {
		t.Fatalf("DropCollection: %v", err)
	}
}

func TestE2E_InsertDocument_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("collection.InsertDocument", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.InsertDocumentResponse{Id: 7})
	})
	resp, err := tr.InsertDocument(context.Background(), transport.InsertDocumentRequest{
		Database: "db", Collection: "users", Document: map[string]interface{}{"name": "alice"},
	})
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}
	if resp.ID != 7 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_GetDocument_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("collection.GetDocument", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.DocumentResponse{
			Id: 7, Document: `{"name":"alice"}`,
		})
	})
	resp, err := tr.GetDocument(context.Background(), "db", "users", 7)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if resp.ID != 7 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestE2E_UpdateDocument_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("collection.UpdateDocument", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.UpdateDocument(context.Background(), transport.UpdateDocumentRequest{
		Database: "db", Collection: "users", ID: 7, Document: map[string]interface{}{"name": "bob"},
	}); err != nil {
		t.Fatalf("UpdateDocument: %v", err)
	}
}

func TestE2E_DeleteDocument_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("collection.DeleteDocument", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.DeleteDocument(context.Background(), "db", "users", 7); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
}

func TestE2E_ListDocuments_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("collection.ListDocuments", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.ListDocumentsResponse{
			Documents: []*pb.DocumentResponse{
				{Id: 1, Document: `{"name":"alice"}`},
			},
			NextPageToken: "next",
			TotalCount:    1,
		})
	})
	resp, err := tr.ListDocuments(context.Background(), transport.ListDocumentsRequest{
		Database: "db", Collection: "users", PageSize: 10,
	})
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(resp.Documents) != 1 || resp.NextPageToken != "next" {
		t.Fatalf("resp = %+v", resp)
	}
}
