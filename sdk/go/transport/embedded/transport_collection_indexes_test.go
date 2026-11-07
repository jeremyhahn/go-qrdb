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

package embedded

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestEmbeddedCollection_CreateCollection_CarriesIndexes covers go-qrdb#414
// on the embedded transport.
func TestEmbeddedCollection_CreateCollection_CarriesIndexes(t *testing.T) {
	var got *service.CreateCollectionRequest
	svc := &mockCollectionService{
		createFn: func(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
			got = req
			return &service.CollectionInfo{
				Name: req.Name, Database: req.Database, Indexes: req.Indexes,
				State: service.CollectionStateCreating, StateSince: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC),
			}, nil
		},
	}
	tr := newExtTransport(t, WithCollectionService(svc))

	defs := []transport.CollectionIndexDefinition{{FieldName: "email", Unique: true}}
	info, err := tr.CreateCollection(context.Background(), transport.CreateCollectionRequest{Database: "db", Name: "users", Indexes: defs})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if got == nil || !reflect.DeepEqual(got.Indexes, []service.IndexDefinition{{FieldName: "email", Unique: true}}) {
		t.Fatalf("service received %+v", got)
	}
	if !reflect.DeepEqual(info.Indexes, defs) || info.State != transport.CollectionStateCreating || info.StateSince == "" {
		t.Fatalf("client received %+v", info)
	}
	if toServiceIndexDefinitions(nil) != nil {
		t.Fatal("no definitions must convert to none")
	}
}

// TestEmbeddedCollection_Delegates drives every collection and document
// method through a service that answers and through one that fails: the
// transport converts the answer and passes the failure through unchanged.
func TestEmbeddedCollection_Delegates(t *testing.T) {
	ctx := t.Context()
	failing := &mockCollectionService{
		createFn: func(context.Context, *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
			return nil, errSentinel
		},
		dropFn: func(context.Context, *service.DropCollectionRequest) error { return errSentinel },
		listFn: func(context.Context, *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
			return nil, errSentinel
		},
		getInfoFn: func(context.Context, string, string) (*service.CollectionInfo, error) { return nil, errSentinel },
		insertFn: func(context.Context, *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
			return nil, errSentinel
		},
		getDocFn: func(context.Context, *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
			return nil, errSentinel
		},
		updateDocFn: func(context.Context, *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
			return nil, errSentinel
		},
		deleteDocFn: func(context.Context, *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
			return nil, errSentinel
		},
		listDocsFn: func(context.Context, *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
			return nil, errSentinel
		},
	}

	tests := []struct {
		name string
		call func(t *testing.T, tr *Transport) error
	}{
		{"CreateCollection", func(t *testing.T, tr *Transport) error {
			info, err := tr.CreateCollection(ctx, transport.CreateCollectionRequest{Database: "db", Name: "users"})
			if err == nil && (info.Name != "users" || info.Database != "db") {
				t.Errorf("info = %+v", info)
			}
			return err
		}},
		{"GetCollectionInfo", func(t *testing.T, tr *Transport) error {
			info, err := tr.GetCollectionInfo(ctx, "db", "users")
			if err == nil && info.Name != "users" {
				t.Errorf("name = %q", info.Name)
			}
			return err
		}},
		{"ListCollections", func(t *testing.T, tr *Transport) error {
			resp, err := tr.ListCollections(ctx, "db")
			if err == nil && (len(resp.Collections) != 1 || resp.Collections[0].Name != "users") {
				t.Errorf("resp = %+v", resp)
			}
			return err
		}},
		{"DropCollection", func(_ *testing.T, tr *Transport) error { return tr.DropCollection(ctx, "db", "users") }},
		{"InsertDocument", func(t *testing.T, tr *Transport) error {
			resp, err := tr.InsertDocument(ctx, transport.InsertDocumentRequest{Database: "db", Collection: "users", Document: map[string]interface{}{"x": 1}})
			if err == nil && resp.ID != 42 {
				t.Errorf("ID = %d", resp.ID)
			}
			return err
		}},
		{"GetDocument", func(t *testing.T, tr *Transport) error {
			doc, err := tr.GetDocument(ctx, "db", "users", 7)
			if err == nil && doc.ID != 7 {
				t.Errorf("ID = %d", doc.ID)
			}
			return err
		}},
		{"UpdateDocument", func(_ *testing.T, tr *Transport) error {
			return tr.UpdateDocument(ctx, transport.UpdateDocumentRequest{Database: "db", Collection: "users", ID: 1})
		}},
		{"DeleteDocument", func(_ *testing.T, tr *Transport) error { return tr.DeleteDocument(ctx, "db", "users", 1) }},
		{"ListDocuments", func(t *testing.T, tr *Transport) error {
			resp, err := tr.ListDocuments(ctx, transport.ListDocumentsRequest{Database: "db", Collection: "users"})
			if err == nil && (resp.TotalCount != 1 || len(resp.Documents) != 1) {
				t.Errorf("resp = %+v", resp)
			}
			return err
		}},
	}
	ok := newExtTransport(t, WithCollectionService(&mockCollectionService{}))
	bad := newExtTransport(t, WithCollectionService(failing))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(t, ok); err != nil {
				t.Fatalf("answering service: %v", err)
			}
			if err := tt.call(t, bad); !errors.Is(err, errSentinel) {
				t.Fatalf("failing service: err = %v, want the service's error", err)
			}
		})
	}
}

// mockCollectionService answers every collection call with a fixed result;
// a non-nil Fn field replaces the answer.
type mockCollectionService struct {
	createFn    func(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error)
	dropFn      func(ctx context.Context, req *service.DropCollectionRequest) error
	listFn      func(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error)
	getInfoFn   func(ctx context.Context, database, name string) (*service.CollectionInfo, error)
	insertFn    func(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error)
	getDocFn    func(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error)
	updateDocFn func(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error)
	deleteDocFn func(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error)
	listDocsFn  func(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error)
}

var _ service.CollectionServicer = (*mockCollectionService)(nil)

func (m *mockCollectionService) CreateCollection(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	if m.createFn != nil {
		return m.createFn(ctx, req)
	}
	return &service.CollectionInfo{Name: req.Name, Database: req.Database, CreatedAt: time.Unix(1700000000, 0).UTC()}, nil
}

func (m *mockCollectionService) DropCollection(ctx context.Context, req *service.DropCollectionRequest) error {
	if m.dropFn != nil {
		return m.dropFn(ctx, req)
	}
	return nil
}

func (m *mockCollectionService) ListCollections(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
	if m.listFn != nil {
		return m.listFn(ctx, req)
	}
	return &service.ListCollectionsResponse{
		Collections: []service.CollectionInfo{
			{Name: "users", Database: req.Database, CreatedAt: time.Unix(1700000000, 0).UTC()},
		},
	}, nil
}

func (m *mockCollectionService) GetCollectionInfo(ctx context.Context, database, name string) (*service.CollectionInfo, error) {
	if m.getInfoFn != nil {
		return m.getInfoFn(ctx, database, name)
	}
	return &service.CollectionInfo{Name: name, Database: database, CreatedAt: time.Unix(1700000000, 0).UTC()}, nil
}

func (m *mockCollectionService) InsertDocument(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
	if m.insertFn != nil {
		return m.insertFn(ctx, req)
	}
	return &service.InsertDocumentResponse{ID: 42}, nil
}

func (m *mockCollectionService) GetDocument(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	if m.getDocFn != nil {
		return m.getDocFn(ctx, req)
	}
	return &service.GetDocumentResponse{ID: req.ID, Document: map[string]interface{}{"name": "alice"}}, nil
}

func (m *mockCollectionService) UpdateDocument(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
	if m.updateDocFn != nil {
		return m.updateDocFn(ctx, req)
	}
	return &service.UpdateDocumentResponse{ID: req.ID}, nil
}

// UpsertDocument reports an insert: no test that uses this double drives
// the guarded upsert, which is exercised against the real service.
func (m *mockCollectionService) UpsertDocument(_ context.Context, req *service.UpsertDocumentRequest) (*service.UpsertDocumentResponse, error) {
	return &service.UpsertDocumentResponse{ID: req.ID}, nil
}

func (m *mockCollectionService) DeleteDocument(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
	if m.deleteDocFn != nil {
		return m.deleteDocFn(ctx, req)
	}
	return &service.DeleteDocumentResponse{Deleted: true}, nil
}

func (m *mockCollectionService) ListDocuments(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
	if m.listDocsFn != nil {
		return m.listDocsFn(ctx, req)
	}
	return &service.ListDocumentsResponse{
		Documents:  []service.GetDocumentResponse{{ID: 1, Document: map[string]interface{}{"k": "v"}}},
		TotalCount: 1,
	}, nil
}
