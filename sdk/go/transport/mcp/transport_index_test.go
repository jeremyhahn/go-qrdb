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
	"errors"
	"sync"
	"testing"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockIndexSvc is an in-memory service.IndexServicer for transport tests.
// ---------------------------------------------------------------------------

type mockIndexSvc struct {
	mu sync.Mutex

	createErr error
	dropErr   error
	queryErr  error
	scanErr   error
	listErr   error

	lastCreateName  string
	lastCreateField string
	lastCreateCfg   index.IndexConfig
	lastDropName    string
	lastQueryName   string
	lastQueryValue  []byte
	lastScanName    string
	lastScanStart   []byte
	lastScanEnd     []byte

	queryResp [][]byte
	scanResp  map[string][][]byte
	listResp  []string
}

var _ service.IndexServicer = (*mockIndexSvc)(nil)

func newMockIndexSvc() *mockIndexSvc {
	return &mockIndexSvc{
		queryResp: [][]byte{[]byte("k1"), []byte("k2")},
		scanResp: map[string][][]byte{
			"alpha": {[]byte("k1")},
			"beta":  {[]byte("k2"), []byte("k3")},
		},
		listResp: []string{"idx_name", "idx_age"},
	}
}

func (m *mockIndexSvc) CreateIndex(_ context.Context, name, fieldName string, cfg index.IndexConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastCreateName = name
	m.lastCreateField = fieldName
	m.lastCreateCfg = cfg
	return m.createErr
}

func (m *mockIndexSvc) DropIndex(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastDropName = name
	return m.dropErr
}

func (m *mockIndexSvc) Query(_ context.Context, indexName string, value []byte) ([][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastQueryName = indexName
	m.lastQueryValue = append([]byte(nil), value...)
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	return m.queryResp, nil
}

func (m *mockIndexSvc) Scan(_ context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastScanName = indexName
	m.lastScanStart = append([]byte(nil), start...)
	m.lastScanEnd = append([]byte(nil), end...)
	if m.scanErr != nil {
		return nil, m.scanErr
	}
	return m.scanResp, nil
}

func (m *mockIndexSvc) ListIndexes(_ context.Context) ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResp, nil
}

func connectIndexInProcess(t *testing.T, svc service.IndexServicer) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithIndex(svc),
	)
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}
	return tr, func() { _ = tr.Close() }
}

// ---------------------------------------------------------------------------
// IndexCreate
// ---------------------------------------------------------------------------

func TestIndexCreate_Success(t *testing.T) {
	svc := newMockIndexSvc()
	tr, cleanup := connectIndexInProcess(t, svc)
	defer cleanup()

	err := tr.IndexCreate(context.Background(), "idx_name", "name", transport.IndexConfig{
		Unique: true,
		Sparse: false,
	})
	if err != nil {
		t.Fatalf("IndexCreate: %v", err)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastCreateName != "idx_name" {
		t.Errorf("name: %q", svc.lastCreateName)
	}
	if svc.lastCreateField != "name" {
		t.Errorf("field: %q", svc.lastCreateField)
	}
	if !svc.lastCreateCfg.Unique {
		t.Error("expected unique=true")
	}
}

func TestIndexCreate_ServiceError(t *testing.T) {
	svc := newMockIndexSvc()
	svc.createErr = errors.New("already exists")
	tr, cleanup := connectIndexInProcess(t, svc)
	defer cleanup()

	if err := tr.IndexCreate(context.Background(), "x", "f", transport.IndexConfig{}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// IndexDrop
// ---------------------------------------------------------------------------

func TestIndexDrop_Success(t *testing.T) {
	svc := newMockIndexSvc()
	tr, cleanup := connectIndexInProcess(t, svc)
	defer cleanup()

	if err := tr.IndexDrop(context.Background(), "idx_name"); err != nil {
		t.Fatalf("IndexDrop: %v", err)
	}
}

func TestIndexDrop_ServiceError(t *testing.T) {
	svc := newMockIndexSvc()
	svc.dropErr = errors.New("not found")
	tr, cleanup := connectIndexInProcess(t, svc)
	defer cleanup()

	if err := tr.IndexDrop(context.Background(), "x"); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// IndexQuery
// ---------------------------------------------------------------------------

func TestIndexQuery_Success(t *testing.T) {
	svc := newMockIndexSvc()
	tr, cleanup := connectIndexInProcess(t, svc)
	defer cleanup()

	resp, err := tr.IndexQuery(context.Background(), "idx_name", "alice")
	if err != nil {
		t.Fatalf("IndexQuery: %v", err)
	}
	if len(resp.Keys) != 2 {
		t.Errorf("keys: %d", len(resp.Keys))
	}
}

func TestIndexQuery_ServiceError(t *testing.T) {
	svc := newMockIndexSvc()
	svc.queryErr = errors.New("index not found")
	tr, cleanup := connectIndexInProcess(t, svc)
	defer cleanup()

	if _, err := tr.IndexQuery(context.Background(), "missing", "x"); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// IndexScan
// ---------------------------------------------------------------------------

func TestIndexScan_Success(t *testing.T) {
	svc := newMockIndexSvc()
	tr, cleanup := connectIndexInProcess(t, svc)
	defer cleanup()

	resp, err := tr.IndexScan(context.Background(), "idx_name", spIdx("a"), spIdx("z"))
	if err != nil {
		t.Fatalf("IndexScan: %v", err)
	}
	if len(resp.Entries) != 2 {
		t.Errorf("entries: %d", len(resp.Entries))
	}
	// Verify entries are sorted by value
	if resp.Entries[0].Value != "alpha" || resp.Entries[1].Value != "beta" {
		t.Errorf("ordering: %+v", resp.Entries)
	}
}

func TestIndexScan_ServiceError(t *testing.T) {
	svc := newMockIndexSvc()
	svc.scanErr = errors.New("scan failed")
	tr, cleanup := connectIndexInProcess(t, svc)
	defer cleanup()

	if _, err := tr.IndexScan(context.Background(), "x", spIdx("a"), spIdx("z")); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// IndexList
// ---------------------------------------------------------------------------

func TestIndexList_Success(t *testing.T) {
	svc := newMockIndexSvc()
	tr, cleanup := connectIndexInProcess(t, svc)
	defer cleanup()

	resp, err := tr.IndexList(context.Background())
	if err != nil {
		t.Fatalf("IndexList: %v", err)
	}
	if len(resp.Names) != 2 {
		t.Errorf("names: %d", len(resp.Names))
	}
}

func TestIndexList_ServiceError(t *testing.T) {
	svc := newMockIndexSvc()
	svc.listErr = errors.New("list failed")
	tr, cleanup := connectIndexInProcess(t, svc)
	defer cleanup()

	if _, err := tr.IndexList(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}
