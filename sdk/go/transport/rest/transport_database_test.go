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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Database test server helper
// ---------------------------------------------------------------------------

// newDatabaseTestServer creates an httptest.Server that simulates the QRDB
// database management REST API endpoints. The server maintains an in-memory
// map of databases for the lifecycle of the test.
func newDatabaseTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	databases := map[string]transport.DatabaseInfo{
		"default": {
			Name:    "default",
			Backend: "pebble",
			Status:  "active",
			DataDir: "/data/default",
			ShardID: 1,
		},
	}

	mux := http.NewServeMux()

	// Health endpoint required for Connect.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(transport.HealthResponse{
			Status:   "ok",
			IsLeader: true,
		})
	})

	// Database create endpoint: POST /v1/databases
	mux.HandleFunc("/v1/databases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req transport.CreateDatabaseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, `{"error":"name is required"}`, http.StatusBadRequest)
			return
		}
		if _, exists := databases[req.Name]; exists {
			http.Error(w, `{"error":"database already exists"}`, http.StatusConflict)
			return
		}
		info := transport.DatabaseInfo{
			Name:    req.Name,
			Backend: req.Backend,
			Status:  "active",
			DataDir: "/data/" + req.Name,
			ShardID: 2,
		}
		databases[req.Name] = info
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(info)
	})

	// Database list endpoint: GET /v1/databases/list
	mux.HandleFunc("/v1/databases/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		dbs := make([]transport.DatabaseInfo, 0, len(databases))
		for _, db := range databases {
			dbs = append(dbs, db)
		}
		_ = json.NewEncoder(w).Encode(transport.ListDatabasesResponse{
			Databases: dbs,
		})
	})

	// Database endpoints: /v1/databases/{name}
	mux.HandleFunc("/v1/databases/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Extract the database name from the path.
		name := strings.TrimPrefix(r.URL.Path, "/v1/databases/")
		if name == "" {
			http.Error(w, `{"error":"name is required"}`, http.StatusBadRequest)
			return
		}

		switch r.Method {
		case http.MethodGet:
			// Get database.
			info, exists := databases[name]
			if !exists {
				http.Error(w, `{"error":"database not found"}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(info)

		case http.MethodDelete:
			// Drop database.
			if _, exists := databases[name]; !exists {
				http.Error(w, `{"error":"database not found"}`, http.StatusNotFound)
				return
			}
			delete(databases, name)
			w.WriteHeader(http.StatusNoContent)

		default:
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
	})

	return httptest.NewServer(mux)
}

// ---------------------------------------------------------------------------
// CreateDatabase tests
// ---------------------------------------------------------------------------

func TestCreateDatabase_Success(t *testing.T) {
	srv := newDatabaseTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	info, err := rt.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{
		Name:    "analytics",
		Backend: "pebble",
	})
	if err != nil {
		t.Fatalf("CreateDatabase failed: %v", err)
	}

	if info.Name != "analytics" {
		t.Errorf("expected name 'analytics', got %q", info.Name)
	}
	if info.Backend != "pebble" {
		t.Errorf("expected backend 'pebble', got %q", info.Backend)
	}
	if info.Status != "active" {
		t.Errorf("expected status 'active', got %q", info.Status)
	}
}

func TestCreateDatabase_Conflict(t *testing.T) {
	srv := newDatabaseTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{
		Name:    "default",
		Backend: "pebble",
	})
	if err == nil {
		t.Fatal("expected error for duplicate database name")
	}

	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestCreateDatabase_ClosedTransport(t *testing.T) {
	srv := newDatabaseTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	_, err := rt.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{
		Name:    "test",
		Backend: "pebble",
	})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}

	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// GetDatabase tests
// ---------------------------------------------------------------------------

func TestGetDatabase_Success(t *testing.T) {
	srv := newDatabaseTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	info, err := rt.GetDatabase(context.Background(), "default")
	if err != nil {
		t.Fatalf("GetDatabase failed: %v", err)
	}

	if info.Name != "default" {
		t.Errorf("expected name 'default', got %q", info.Name)
	}
	if info.Backend != "pebble" {
		t.Errorf("expected backend 'pebble', got %q", info.Backend)
	}
	if info.Status != "active" {
		t.Errorf("expected status 'active', got %q", info.Status)
	}
	if info.ShardID != 1 {
		t.Errorf("expected shard_id 1, got %d", info.ShardID)
	}
}

func TestGetDatabase_NotFound(t *testing.T) {
	srv := newDatabaseTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.GetDatabase(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent database")
	}

	var notFoundErr *transport.DatabaseNotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Errorf("expected DatabaseNotFoundError, got %T: %v", err, err)
	}
	if notFoundErr.Name != "nonexistent" {
		t.Errorf("expected name 'nonexistent', got %q", notFoundErr.Name)
	}
}

func TestGetDatabase_ClosedTransport(t *testing.T) {
	srv := newDatabaseTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	_, err := rt.GetDatabase(context.Background(), "default")
	if err == nil {
		t.Fatal("expected error on closed transport")
	}

	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ListDatabases tests
// ---------------------------------------------------------------------------

func TestListDatabases_Success(t *testing.T) {
	srv := newDatabaseTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	dbs, err := rt.ListDatabases(context.Background())
	if err != nil {
		t.Fatalf("ListDatabases failed: %v", err)
	}

	if len(dbs) != 1 {
		t.Fatalf("expected 1 database, got %d", len(dbs))
	}
	if dbs[0].Name != "default" {
		t.Errorf("expected database name 'default', got %q", dbs[0].Name)
	}
}

func TestListDatabases_ClosedTransport(t *testing.T) {
	srv := newDatabaseTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	_, err := rt.ListDatabases(context.Background())
	if err == nil {
		t.Fatal("expected error on closed transport")
	}

	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DropDatabase tests
// ---------------------------------------------------------------------------

func TestDropDatabase_Success(t *testing.T) {
	srv := newDatabaseTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	ctx := context.Background()

	// Create a database to drop.
	_, err := rt.CreateDatabase(ctx, transport.CreateDatabaseRequest{
		Name:    "temporary",
		Backend: "pebble",
	})
	if err != nil {
		t.Fatalf("CreateDatabase failed: %v", err)
	}

	// Verify it exists.
	_, err = rt.GetDatabase(ctx, "temporary")
	if err != nil {
		t.Fatalf("GetDatabase failed: %v", err)
	}

	// Drop it.
	if err := rt.DropDatabase(ctx, "temporary"); err != nil {
		t.Fatalf("DropDatabase failed: %v", err)
	}

	// Verify it no longer exists.
	_, err = rt.GetDatabase(ctx, "temporary")
	if err == nil {
		t.Fatal("expected error for dropped database")
	}

	var notFoundErr *transport.DatabaseNotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Errorf("expected DatabaseNotFoundError, got %T: %v", err, err)
	}
}

func TestDropDatabase_NotFound(t *testing.T) {
	srv := newDatabaseTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.DropDatabase(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent database")
	}

	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestDropDatabase_ClosedTransport(t *testing.T) {
	srv := newDatabaseTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	err := rt.DropDatabase(context.Background(), "default")
	if err == nil {
		t.Fatal("expected error on closed transport")
	}

	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Full CRUD lifecycle test
// ---------------------------------------------------------------------------

func TestDatabase_FullLifecycle(t *testing.T) {
	srv := newDatabaseTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	ctx := context.Background()

	// 1. List initial databases.
	dbs, err := rt.ListDatabases(ctx)
	if err != nil {
		t.Fatalf("initial ListDatabases failed: %v", err)
	}
	initialCount := len(dbs)

	// 2. Create a new database.
	created, err := rt.CreateDatabase(ctx, transport.CreateDatabaseRequest{
		Name:    "test_db",
		Backend: "pebble",
	})
	if err != nil {
		t.Fatalf("CreateDatabase failed: %v", err)
	}
	if created.Name != "test_db" {
		t.Errorf("expected name 'test_db', got %q", created.Name)
	}

	// 3. Get the database.
	fetched, err := rt.GetDatabase(ctx, "test_db")
	if err != nil {
		t.Fatalf("GetDatabase failed: %v", err)
	}
	if fetched.Name != created.Name {
		t.Errorf("expected name %q, got %q", created.Name, fetched.Name)
	}

	// 4. List should show one more database.
	dbs, err = rt.ListDatabases(ctx)
	if err != nil {
		t.Fatalf("ListDatabases after create failed: %v", err)
	}
	if len(dbs) != initialCount+1 {
		t.Errorf("expected %d databases, got %d", initialCount+1, len(dbs))
	}

	// 5. Drop the database.
	if err := rt.DropDatabase(ctx, "test_db"); err != nil {
		t.Fatalf("DropDatabase failed: %v", err)
	}

	// 6. Get should return not found.
	_, err = rt.GetDatabase(ctx, "test_db")
	var notFoundErr *transport.DatabaseNotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Errorf("expected DatabaseNotFoundError after drop, got %T: %v", err, err)
	}

	// 7. List should be back to initial count.
	dbs, err = rt.ListDatabases(ctx)
	if err != nil {
		t.Fatalf("ListDatabases after drop failed: %v", err)
	}
	if len(dbs) != initialCount {
		t.Errorf("expected %d databases after drop, got %d", initialCount, len(dbs))
	}
}

// ---------------------------------------------------------------------------
// Server error response tests
// ---------------------------------------------------------------------------

func TestCreateDatabase_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.CreateDatabase(context.Background(), transport.CreateDatabaseRequest{
		Name:    "test",
		Backend: "pebble",
	})
	if err == nil {
		t.Fatal("expected error for server error response")
	}

	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestGetDatabase_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.GetDatabase(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for server error response")
	}

	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestListDatabases_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.ListDatabases(context.Background())
	if err == nil {
		t.Fatal("expected error for server error response")
	}

	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestListDatabases_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{invalid-json`))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.ListDatabases(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}

	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestGetDatabase_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{invalid-json`))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.GetDatabase(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}

	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Compile-time interface assertion test
// ---------------------------------------------------------------------------
