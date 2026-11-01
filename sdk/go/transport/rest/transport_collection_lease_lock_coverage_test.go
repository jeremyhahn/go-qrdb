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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Collection
// ===========================================================================

func TestRESTCollection_CreateCollection_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/collections"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.CollectionInfo{Name: "users", Database: "db"})
	})
	info, err := rt.CreateCollection(context.Background(), transport.CreateCollectionRequest{Database: "db", Name: "users"})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if info.Name != "users" {
		t.Fatalf("Name = %q", info.Name)
	}
}

func TestRESTCollection_CreateCollection_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.CreateCollection(context.Background(), transport.CreateCollectionRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCollection_GetCollectionInfo_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/collections/db/users"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.CollectionInfo{Name: "users", Database: "db"})
	})
	info, err := rt.GetCollectionInfo(context.Background(), "db", "users")
	if err != nil {
		t.Fatalf("GetCollectionInfo: %v", err)
	}
	if info.Name != "users" {
		t.Fatalf("Name = %q", info.Name)
	}
}

func TestRESTCollection_GetCollectionInfo_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/collections/db/users"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.GetCollectionInfo(context.Background(), "db", "users")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTCollection_GetCollectionInfo_BadJSON(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/collections/db/users"), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	})
	_, err := rt.GetCollectionInfo(context.Background(), "db", "users")
	if err == nil {
		t.Fatal("expected JSON decode error")
	}
}

func TestRESTCollection_GetCollectionInfo_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.GetCollectionInfo(context.Background(), "db", "users")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCollection_ListCollections_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/v1/collections/list"), func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("database") != "db" {
			t.Errorf("expected database=db, got %q", r.URL.RawQuery)
		}
		writeJSON(w, &transport.ListCollectionsResponse{Collections: []transport.CollectionInfo{{Name: "users"}}})
	})
	resp, err := rt.ListCollections(context.Background(), "db")
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(resp.Collections) != 1 {
		t.Fatalf("got %d collections", len(resp.Collections))
	}
}

func TestRESTCollection_ListCollections_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/v1/collections/list"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.ListCollections(context.Background(), "db")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTCollection_ListCollections_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.ListCollections(context.Background(), "db")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCollection_DropCollection_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodDelete, "/v1/collections/db/users"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := rt.DropCollection(context.Background(), "db", "users"); err != nil {
		t.Fatalf("DropCollection: %v", err)
	}
}

func TestRESTCollection_DropCollection_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodDelete, "/v1/collections/db/users"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	err := rt.DropCollection(context.Background(), "db", "users")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTCollection_DropCollection_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.DropCollection(context.Background(), "db", "users")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCollection_InsertDocument_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/collections/db/users/documents"), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.Copy(io.Discard, bytes.NewReader(nil))
		writeJSON(w, &transport.InsertDocumentResponse{ID: 42})
	})
	resp, err := rt.InsertDocument(context.Background(), transport.InsertDocumentRequest{Database: "db", Collection: "users", Document: map[string]interface{}{"x": 1}})
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}
	if resp.ID != 42 {
		t.Fatalf("ID = %d", resp.ID)
	}
}

func TestRESTCollection_InsertDocument_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/collections/db/users/documents"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	})
	_, err := rt.InsertDocument(context.Background(), transport.InsertDocumentRequest{Database: "db", Collection: "users"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTCollection_InsertDocument_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.InsertDocument(context.Background(), transport.InsertDocumentRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCollection_GetDocument_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/collections/db/users/documents/1"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.DocumentResponse{ID: 1, Document: map[string]interface{}{"name": "alice"}})
	})
	doc, err := rt.GetDocument(context.Background(), "db", "users", 1)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if doc.ID != 1 {
		t.Fatalf("ID = %d", doc.ID)
	}
}

func TestRESTCollection_GetDocument_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/collections/db/users/documents/1"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.GetDocument(context.Background(), "db", "users", 1)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTCollection_GetDocument_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.GetDocument(context.Background(), "db", "users", 1)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCollection_UpdateDocument_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPut, "/v1/collections/db/users/documents/1"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := rt.UpdateDocument(context.Background(), transport.UpdateDocumentRequest{Database: "db", Collection: "users", ID: 1}); err != nil {
		t.Fatalf("UpdateDocument: %v", err)
	}
}

func TestRESTCollection_UpdateDocument_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPut, "/v1/collections/db/users/documents/1"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	})
	err := rt.UpdateDocument(context.Background(), transport.UpdateDocumentRequest{Database: "db", Collection: "users", ID: 1})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTCollection_UpdateDocument_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.UpdateDocument(context.Background(), transport.UpdateDocumentRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCollection_DeleteDocument_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodDelete, "/v1/collections/db/users/documents/1"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := rt.DeleteDocument(context.Background(), "db", "users", 1); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
}

func TestRESTCollection_DeleteDocument_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodDelete, "/v1/collections/db/users/documents/1"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	err := rt.DeleteDocument(context.Background(), "db", "users", 1)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTCollection_DeleteDocument_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.DeleteDocument(context.Background(), "db", "users", 1)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCollection_ListDocuments_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/v1/collections/db/users/documents"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.ListDocumentsResponse{TotalCount: 1, Documents: []transport.DocumentResponse{{ID: 1}}})
	})
	resp, err := rt.ListDocuments(context.Background(), transport.ListDocumentsRequest{Database: "db", Collection: "users", PageSize: 10})
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if resp.TotalCount != 1 {
		t.Fatalf("TotalCount = %d", resp.TotalCount)
	}
}

func TestRESTCollection_ListDocuments_NoPagination(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/v1/collections/db/users/documents"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.ListDocumentsResponse{})
	})
	if _, err := rt.ListDocuments(context.Background(), transport.ListDocumentsRequest{Database: "db", Collection: "users"}); err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
}

func TestRESTCollection_ListDocuments_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/v1/collections/db/users/documents"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.ListDocuments(context.Background(), transport.ListDocumentsRequest{Database: "db", Collection: "users"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTCollection_ListDocuments_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.ListDocuments(context.Background(), transport.ListDocumentsRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// ===========================================================================
// Lease
// ===========================================================================

func TestRESTLease_Grant_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/lease/grant"), func(w http.ResponseWriter, r *http.Request) {
		var req map[string]int64
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["ttl"] != 60 {
			t.Errorf("expected ttl=60, got %d", req["ttl"])
		}
		writeJSON(w, map[string]interface{}{"id": 42, "ttl": 60, "granted_ttl": 60, "remaining_ttl": 60, "keys": []string{"k1"}})
	})
	info, err := rt.LeaseGrant(context.Background(), 60)
	if err != nil {
		t.Fatalf("LeaseGrant: %v", err)
	}
	if info.ID != 42 || info.TTL != 60 || len(info.Keys) != 1 {
		t.Fatalf("unexpected: %+v", info)
	}
}

func TestRESTLease_Grant_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/lease/grant"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.LeaseGrant(context.Background(), 60)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTLease_Grant_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.LeaseGrant(context.Background(), 60)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTLease_Revoke_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/lease/revoke"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := rt.LeaseRevoke(context.Background(), 42); err != nil {
		t.Fatalf("LeaseRevoke: %v", err)
	}
}

func TestRESTLease_Revoke_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.LeaseRevoke(context.Background(), 42)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTLease_KeepAlive_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/lease/keepalive"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{"id": 42, "ttl": 60, "remaining_ttl": 30})
	})
	info, err := rt.LeaseKeepAlive(context.Background(), 42)
	if err != nil {
		t.Fatalf("LeaseKeepAlive: %v", err)
	}
	if info.ID != 42 {
		t.Fatalf("ID = %d", info.ID)
	}
}

func TestRESTLease_KeepAlive_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.LeaseKeepAlive(context.Background(), 42)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTLease_TimeToLive_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/lease/ttl"), func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "lease_id=42") {
			t.Errorf("expected lease_id=42, got %q", r.URL.RawQuery)
		}
		writeJSON(w, map[string]interface{}{"id": 42, "ttl": 60})
	})
	info, err := rt.LeaseTimeToLive(context.Background(), 42)
	if err != nil {
		t.Fatalf("LeaseTimeToLive: %v", err)
	}
	if info.ID != 42 {
		t.Fatalf("ID = %d", info.ID)
	}
}

func TestRESTLease_TimeToLive_NotFound(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/lease/ttl"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := rt.LeaseTimeToLive(context.Background(), 42)
	var lne *transport.LeaseNotFoundError
	if !errors.As(err, &lne) {
		t.Fatalf("expected LeaseNotFoundError, got %T", err)
	}
}

func TestRESTLease_TimeToLive_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/lease/ttl"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.LeaseTimeToLive(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTLease_TimeToLive_BadJSON(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/lease/ttl"), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	})
	_, err := rt.LeaseTimeToLive(context.Background(), 42)
	if err == nil {
		t.Fatal("expected JSON decode error")
	}
}

func TestRESTLease_TimeToLive_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.LeaseTimeToLive(context.Background(), 42)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTLease_List_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/lease/list"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{"leases": []map[string]interface{}{
			{"id": 1, "ttl": 60},
			{"id": 2, "ttl": 120},
		}})
	})
	leases, err := rt.LeaseList(context.Background())
	if err != nil {
		t.Fatalf("LeaseList: %v", err)
	}
	if len(leases) != 2 {
		t.Fatalf("got %d leases", len(leases))
	}
}

func TestRESTLease_List_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/lease/list"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.LeaseList(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTLease_List_BadJSON(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/lease/list"), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	})
	_, err := rt.LeaseList(context.Background())
	if err == nil {
		t.Fatal("expected JSON decode error")
	}
}

func TestRESTLease_List_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.LeaseList(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// ===========================================================================
// Lock
// ===========================================================================

func TestRESTLock_Acquire_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/lock/acquire"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{"key": "k1", "fencing_token": 1, "holder_id": "h1"})
	})
	info, err := rt.LockAcquire(context.Background(), &transport.LockAcquireRequest{Key: "k1", HolderID: "h1", TTL: 60})
	if err != nil {
		t.Fatalf("LockAcquire: %v", err)
	}
	if info.Key != "k1" {
		t.Fatalf("Key = %q", info.Key)
	}
}

func TestRESTLock_Acquire_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.LockAcquire(context.Background(), &transport.LockAcquireRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTLock_TryLock_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/lock/trylock"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{"key": "k1", "fencing_token": 2})
	})
	info, err := rt.LockTryLock(context.Background(), &transport.LockAcquireRequest{Key: "k1"})
	if err != nil {
		t.Fatalf("LockTryLock: %v", err)
	}
	if info.FencingToken != 2 {
		t.Fatalf("FencingToken = %d", info.FencingToken)
	}
}

func TestRESTLock_TryLock_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.LockTryLock(context.Background(), &transport.LockAcquireRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTLock_Release_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/lock/release"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := rt.LockRelease(context.Background(), &transport.LockReleaseRequest{Key: "k1", FencingToken: 1}); err != nil {
		t.Fatalf("LockRelease: %v", err)
	}
}

func TestRESTLock_Release_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.LockRelease(context.Background(), &transport.LockReleaseRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTLock_Extend_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/lock/extend"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{"key": "k1", "fencing_token": 1})
	})
	info, err := rt.LockExtend(context.Background(), &transport.LockExtendRequest{Key: "k1", FencingToken: 1})
	if err != nil {
		t.Fatalf("LockExtend: %v", err)
	}
	if info.Key != "k1" {
		t.Fatalf("Key = %q", info.Key)
	}
}

func TestRESTLock_Extend_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.LockExtend(context.Background(), &transport.LockExtendRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTLock_Status_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/lock/status"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{"key": "k1", "fencing_token": 1})
	})
	info, err := rt.LockStatus(context.Background(), "k1")
	if err != nil {
		t.Fatalf("LockStatus: %v", err)
	}
	if info.Key != "k1" {
		t.Fatalf("Key = %q", info.Key)
	}
}

func TestRESTLock_Status_NotFound(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/lock/status"), func(w http.ResponseWriter, _ *http.Request) {
		notFound := &lock.LockNotFoundError{Key: "k1"}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(transport.ErrorResponse{
			Error: notFound.Error(), Code: lock.ReasonLockNotFound, Metadata: notFound.Metadata(),
		})
	})
	info, err := rt.LockStatus(context.Background(), "k1")
	if err != nil {
		t.Fatalf("LockStatus: %v", err)
	}
	if info != nil {
		t.Fatalf("expected nil info, got %+v", info)
	}
}

// TestRESTLock_Status_NotFoundWithoutLockReasonIsAnError pins that a 404
// which is not the server's LOCK_NOT_FOUND refusal -- a route or database
// the server does not have -- is a failed read, not "not locked".
func TestRESTLock_Status_NotFoundWithoutLockReasonIsAnError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/lock/status"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "404 page not found", http.StatusNotFound)
	})
	info, err := rt.LockStatus(context.Background(), "k1")
	if err == nil {
		t.Fatalf("LockStatus = %+v, nil; want an error", info)
	}
	if reqErr, ok := errors.AsType[*transport.RequestError](err); !ok || reqErr.StatusCode != http.StatusNotFound {
		t.Fatalf("err = %T, want a RequestError carrying 404", err)
	}
}

func TestRESTLock_Status_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/lock/status"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.LockStatus(context.Background(), "k1")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTLock_Status_BadJSON(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/lock/status"), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	})
	_, err := rt.LockStatus(context.Background(), "k1")
	if err == nil {
		t.Fatal("expected JSON decode error")
	}
}

func TestRESTLock_Status_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.LockStatus(context.Background(), "k1")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTLock_List_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/lock/list"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{"locks": []map[string]interface{}{
			{"key": "k1"},
		}})
	})
	resp, err := rt.LockList(context.Background(), "prefix")
	if err != nil {
		t.Fatalf("LockList: %v", err)
	}
	if len(resp.Locks) != 1 {
		t.Fatalf("got %d locks", len(resp.Locks))
	}
}

func TestRESTLock_List_NoPrefix(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/lock/list"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{"locks": []map[string]interface{}{}})
	})
	if _, err := rt.LockList(context.Background(), ""); err != nil {
		t.Fatalf("LockList: %v", err)
	}
}

func TestRESTLock_List_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/lock/list"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.LockList(context.Background(), "prefix")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTLock_List_BadJSON(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/lock/list"), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	})
	_, err := rt.LockList(context.Background(), "prefix")
	if err == nil {
		t.Fatal("expected JSON decode error")
	}
}

func TestRESTLock_List_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.LockList(context.Background(), "prefix")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// ===========================================================================
// Batch
// ===========================================================================

func TestRESTBatch_ExecuteBatch_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/batch"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.BatchResponse{Responses: []transport.OpResponse{{Succeeded: true}}})
	})
	resp, err := rt.ExecuteBatch(context.Background(), []transport.Op{{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")}})
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}
	if len(resp.Responses) != 1 {
		t.Fatalf("got %d responses", len(resp.Responses))
	}
}

func TestRESTBatch_ExecuteBatch_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.ExecuteBatch(context.Background(), nil)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTBatch_ImportFile_ChunkedRoundTrip(t *testing.T) {
	const fileBody = "key,val\nk1,v1\nk2,v2\n"

	rt, rs, _ := newExtTransport(t)

	var gotFormat, gotKeyField, gotValueMode string
	var staged bytes.Buffer
	rs.handle(exactPath(http.MethodPost, "/batch/import/begin"), func(w http.ResponseWriter, r *http.Request) {
		var req importBeginWireRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode ImportBegin request: %v", err)
		}
		gotFormat, gotKeyField, gotValueMode = req.Format, req.KeyField, req.ValueMode
		writeJSON(w, importBeginWireResponse{SessionID: "sess-1"})
	})
	rs.handle(pathPrefix(http.MethodPost, "/batch/import/sess-1/"), func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/append"):
			raw, _ := io.ReadAll(r.Body)
			staged.Write(raw)
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/commit"):
			writeJSON(w, batchImportWireResponse{Total: 2, Succeeded: 2, Failed: 0})
		default:
			http.NotFound(w, r)
		}
	})

	res, err := rt.ImportFile(context.Background(), "data.csv",
		strings.NewReader(fileBody),
		transport.ImportOptions{
			Format:    "csv",
			KeyField:  "key",
			ValueMode: "field",
		})
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if res.Total != 2 || res.Succeeded != 2 || res.Failed != 0 {
		t.Fatalf("result = %+v, want total=2 succeeded=2 failed=0", res)
	}
	if staged.String() != fileBody {
		t.Errorf("staged chunks = %q, want %q", staged.String(), fileBody)
	}
	if gotFormat != "csv" || gotKeyField != "key" || gotValueMode != "field" {
		t.Errorf("ImportBegin fields format=%q keyField=%q valueMode=%q, want csv/key/field",
			gotFormat, gotKeyField, gotValueMode)
	}
}

func TestRESTBatch_ImportFile_EmptyFilenameStillSent(t *testing.T) {
	// An empty filename (stdin imports) must still be forwarded as an
	// explicit empty "filename" field on ImportBegin, not omitted -- the
	// wire struct carries no `omitempty` tag specifically so the server
	// can distinguish "no filename" from "field missing" the same way for
	// every caller.
	rt, rs, _ := newExtTransport(t)

	var sawFilenameField bool
	rs.handle(exactPath(http.MethodPost, "/batch/import/begin"), func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		sawFilenameField = strings.Contains(string(raw), `"filename":""`)
		writeJSON(w, importBeginWireResponse{SessionID: "sess-2"})
	})
	rs.handle(pathPrefix(http.MethodPost, "/batch/import/sess-2/"), func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/append"):
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/commit"):
			writeJSON(w, batchImportWireResponse{Total: 1, Succeeded: 1})
		default:
			http.NotFound(w, r)
		}
	})

	if _, err := rt.ImportFile(context.Background(), "", strings.NewReader("{\"id\":\"x\"}\n"),
		transport.ImportOptions{Format: "ndjson", KeyField: "id", ValueMode: "row"}); err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if !sawFilenameField {
		t.Fatal("empty filename was not sent as an explicit \"filename\":\"\" field on ImportBegin")
	}
}

func TestRESTBatch_ImportFile_ClosedTransport(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.ImportFile(context.Background(), "data.csv", strings.NewReader("k,v"), transport.ImportOptions{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}
