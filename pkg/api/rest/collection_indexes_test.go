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

package rest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestHandleCreateCollection_CarriesIndexes pins the REST half of
// go-qrdb#414: index definitions and the storage codec reach the service, and
// the lifecycle state comes back.
func TestHandleCreateCollection_CarriesIndexes(t *testing.T) {
	var got *service.CreateCollectionRequest
	since := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	collSvc := newMockCollectionService()
	collSvc.createCollectionFn = func(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
		got = req
		return &service.CollectionInfo{
			Name: req.Name, Database: req.Database, Indexes: req.Indexes, StorageCodec: req.StorageCodec,
			State: service.CollectionStateActive, StateSince: since,
		}, nil
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithCollectionService(collSvc))

	body := `{"name":"users","database":"mydb","storage_codec":"json","indexes":[{"field_name":"email","unique":true}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/collections", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusCreated)

	want := []service.IndexDefinition{{FieldName: "email", Unique: true}}
	if got == nil || !reflect.DeepEqual(got.Indexes, want) || got.StorageCodec != "json" {
		t.Fatalf("request not carried: %+v", got)
	}
	var resp collectionInfoResponse
	decodeJSON(t, rec, &resp)
	if !reflect.DeepEqual(resp.Indexes, want) || resp.State != "active" || resp.StateSince == "" || resp.StorageCodec != "json" {
		t.Fatalf("response not carried: %+v", resp)
	}
}

// TestMapHTTPError_CollectionLifecycle pins the status of each collection
// lifecycle refusal, including when it arrives wrapped in the
// CollectionMetadataError a failed create reports.
func TestMapHTTPError_CollectionLifecycle(t *testing.T) {
	cases := map[string]struct {
		err  error
		want int
	}{
		"not active":        {&service.CollectionNotActiveError{State: "dropping"}, http.StatusConflict},
		"state conflict":    {&service.CollectionStateConflictError{Op: "activate"}, http.StatusConflict},
		"document conflict": {&service.CollectionDocumentConflictError{ID: 1}, http.StatusConflict},
		"invalid index":     {&service.CollectionInvalidIndexError{Reason: "x"}, http.StatusBadRequest},
		"ownership wrapped": {&service.CollectionMetadataError{Op: "create-index", Err: &index.OwnershipError{Name: "n"}}, http.StatusConflict},
		"retract failed":    {&service.CollectionMetadataError{Op: "retract-write", Err: errors.Join(&service.CollectionNotActiveError{State: "dropping"}, errors.New("disk"))}, http.StatusInternalServerError},
		"metadata":          {&service.CollectionMetadataError{Op: "save", Err: errors.New("disk")}, http.StatusInternalServerError},
	}
	for name, tc := range cases {
		if got := mapHTTPError(tc.err); got != tc.want {
			t.Errorf("%s: status %d, want %d", name, got, tc.want)
		}
	}
}

// explicitDropRecorder records the owner an owner-checked drop claims and
// refuses it, as the apply path does for an index a collection owns.
type explicitDropRecorder struct {
	service.IndexServicer
	claimed string
}

func (e *explicitDropRecorder) DropOwnedIndex(_ context.Context, name, owner string) error {
	e.claimed = owner
	return &index.OwnershipError{Name: name, Owner: "collection:db/c#1", Requested: owner}
}

// TestHandleIndexDrop_RefusesACollectionOwnedIndex pins that the explicit
// index API drops as the explicit owner, and a refusal is 409.
func TestHandleIndexDrop_RefusesACollectionOwnedIndex(t *testing.T) {
	rec := &explicitDropRecorder{claimed: "unset"}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithIndex(rec))
	req := httptest.NewRequest(http.MethodPost, "/index/drop", strings.NewReader(`{"name":"db.c.email"}`))
	w := httptest.NewRecorder()
	srv.dispatch(w, req)
	assertStatus(t, w, http.StatusConflict)
	if rec.claimed != index.ExplicitOwner {
		t.Fatalf("the explicit drop claimed owner %q", rec.claimed)
	}
}
