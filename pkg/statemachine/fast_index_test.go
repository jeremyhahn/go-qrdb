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

package statemachine

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// ---------------------------------------------------------------------------
// mockIndexManager: records calls and returns configured responses
// ---------------------------------------------------------------------------

type mockIndexManager struct {
	mu sync.Mutex

	createCalls []mockCreateCall
	createErr   error

	dropCalls []string
	dropErr   error

	queryResult [][]byte
	queryErr    error

	scanResult map[string][][]byte
	scanErr    error

	listResult []interface{}
	listErr    error
}

type mockCreateCall struct {
	name      string
	fieldName string
	opts      protocol.IndexCreateOptions
}

func (m *mockIndexManager) CreateIndex(name, fieldName string, opts protocol.IndexCreateOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createCalls = append(m.createCalls, mockCreateCall{
		name:      name,
		fieldName: fieldName,
		opts:      opts,
	})
	return m.createErr
}

func (m *mockIndexManager) DropIndex(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dropCalls = append(m.dropCalls, name)
	return m.dropErr
}

func (m *mockIndexManager) Query(_ string, _ []byte) ([][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.queryResult, m.queryErr
}

func (m *mockIndexManager) Scan(_ string, _, _ []byte) (map[string][][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.scanResult, m.scanErr
}

func (m *mockIndexManager) List() ([]interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listResult, m.listErr
}

func (m *mockIndexManager) getCreateCalls() []mockCreateCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]mockCreateCall, len(m.createCalls))
	copy(cp, m.createCalls)
	return cp
}

func (m *mockIndexManager) getDropCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(m.dropCalls))
	copy(cp, m.dropCalls)
	return cp
}

// ---------------------------------------------------------------------------
// Tests: handleIndexCreate
// ---------------------------------------------------------------------------

func TestIndexCreate_NilIndexManager(t *testing.T) {
	fsm, _ := newTestFSM() // no IndexManager configured

	cmd := mustEncode(protocol.EncodeIndexCreate("user_email", "email", true, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("index")) {
		t.Fatalf("expected Data to mention %q, got %q", "index", results[0].Data)
	}
}

func TestIndexCreate_Success(t *testing.T) {
	im := &mockIndexManager{}
	fsm, _ := newTestFSM(WithIndexManager(im))

	cmd := mustEncode(protocol.EncodeIndexCreate("user_email", "email", true, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := fsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision")
	}

	calls := im.getCreateCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 CreateIndex call, got %d", len(calls))
	}
	if calls[0].name != "user_email" {
		t.Fatalf("expected index name 'user_email', got %q", calls[0].name)
	}
	if calls[0].fieldName != "email" {
		t.Fatalf("expected field name 'email', got %q", calls[0].fieldName)
	}
	if !calls[0].opts.Unique {
		t.Fatal("expected unique=true")
	}
	if calls[0].opts.Sparse {
		t.Fatal("expected sparse=false")
	}
}

func TestIndexCreate_WithSparse(t *testing.T) {
	im := &mockIndexManager{}
	fsm, _ := newTestFSM(WithIndexManager(im))

	cmd := mustEncode(protocol.EncodeIndexCreate("user_phone", "phone", false, true, false))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := fsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	calls := im.getCreateCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 CreateIndex call, got %d", len(calls))
	}
	if calls[0].opts.Unique {
		t.Fatal("expected unique=false")
	}
	if !calls[0].opts.Sparse {
		t.Fatal("expected sparse=true")
	}
}

func TestIndexCreate_ManagerError(t *testing.T) {
	managerErr := errors.New("duplicate index name")
	im := &mockIndexManager{createErr: managerErr}
	fsm, _ := newTestFSM(WithIndexManager(im))

	cmd := mustEncode(protocol.EncodeIndexCreate("dup_idx", "field", false, false, false))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error from index manager")
	}

	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("expected StorageError wrapping manager error, got %T: %v", err, err)
	}
	if !errors.Is(storageErr.Unwrap(), managerErr) {
		t.Fatalf("expected inner error to be manager error, got %v", storageErr.Unwrap())
	}
}

func TestIndexCreate_InvalidPayload(t *testing.T) {
	im := &mockIndexManager{}
	fsm, _ := newTestFSM(WithIndexManager(im))

	// Craft a truncated IndexCreate: op byte + 1 byte (too short).
	cmd := []byte{protocol.OpIndexCreate, 0x01}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for truncated index create payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Tests: handleIndexDrop
// ---------------------------------------------------------------------------

func TestIndexDrop_NilIndexManager(t *testing.T) {
	fsm, _ := newTestFSM() // no IndexManager configured

	cmd := mustEncode(protocol.EncodeIndexDrop("user_email"))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err != nil {
		t.Fatalf("expected nil error (SNC encoded in result.Data), got %v", err)
	}
	if len(results[0].Data) == 0 {
		t.Fatal("expected ServiceNotConfiguredError encoded in result.Data")
	}
	if !bytes.Contains(results[0].Data, []byte("index")) {
		t.Fatalf("expected Data to mention %q, got %q", "index", results[0].Data)
	}
}

func TestIndexDrop_Success(t *testing.T) {
	im := &mockIndexManager{}
	fsm, _ := newTestFSM(WithIndexManager(im))

	cmd := mustEncode(protocol.EncodeIndexDrop("user_email"))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := fsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision")
	}

	calls := im.getDropCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 DropIndex call, got %d", len(calls))
	}
	if calls[0] != "user_email" {
		t.Fatalf("expected index name 'user_email', got %q", calls[0])
	}
}

func TestIndexDrop_ManagerError(t *testing.T) {
	managerErr := errors.New("index not found")
	im := &mockIndexManager{dropErr: managerErr}
	fsm, _ := newTestFSM(WithIndexManager(im))

	cmd := mustEncode(protocol.EncodeIndexDrop("nonexistent"))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error from index manager")
	}

	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("expected StorageError wrapping manager error, got %T: %v", err, err)
	}
	if !errors.Is(storageErr.Unwrap(), managerErr) {
		t.Fatalf("expected inner error to be manager error, got %v", storageErr.Unwrap())
	}
}

func TestIndexDrop_InvalidPayload(t *testing.T) {
	im := &mockIndexManager{}
	fsm, _ := newTestFSM(WithIndexManager(im))

	// Craft a truncated IndexDrop: op byte + 1 byte (too short).
	cmd := []byte{protocol.OpIndexDrop, 0x01}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	err := fsm.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for truncated index drop payload")
	}

	var incErr *IncompleteDataError
	if !errors.As(err, &incErr) {
		t.Fatalf("expected IncompleteDataError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Tests: lookupIndex - IndexQueryRequest
// ---------------------------------------------------------------------------

func TestIndexLookup_Query_NilIndexManager(t *testing.T) {
	fsm, _ := newTestFSM() // no IndexManager

	_, err := fsm.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "user_email",
		Value:     []byte("alice@example.com"),
	})
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}

	var svcErr *ServiceNotConfiguredError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestIndexLookup_Query_Success(t *testing.T) {
	im := &mockIndexManager{
		queryResult: [][]byte{[]byte("100"), []byte("200"), []byte("300")},
	}
	fsm, _ := newTestFSM(WithIndexManager(im))

	result, err := fsm.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "user_email",
		Value:     []byte("alice@example.com"),
	})
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}

	ids, ok := result.([][]byte)
	if !ok {
		t.Fatalf("expected [][]byte, got %T", result)
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 results, got %d", len(ids))
	}
	if string(ids[0]) != "100" || string(ids[1]) != "200" || string(ids[2]) != "300" {
		t.Fatalf("unexpected results: %v", ids)
	}
}

func TestIndexLookup_Query_Error(t *testing.T) {
	queryErr := errors.New("index corrupted")
	im := &mockIndexManager{queryErr: queryErr}
	fsm, _ := newTestFSM(WithIndexManager(im))

	_, err := fsm.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "broken_idx",
		Value:     []byte("test"),
	})
	if err == nil {
		t.Fatal("expected error from index query")
	}
	if !errors.Is(err, queryErr) {
		t.Fatalf("expected query error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: lookupIndex - IndexScanRequest
// ---------------------------------------------------------------------------

func TestIndexLookup_Scan_NilIndexManager(t *testing.T) {
	fsm, _ := newTestFSM() // no IndexManager

	_, err := fsm.Lookup(context.Background(), index.IndexScanRequest{
		IndexName: "price_idx",
		Start:     []byte("10"),
		End:       []byte("99"),
	})
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}

	var svcErr *ServiceNotConfiguredError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestIndexLookup_Scan_Success(t *testing.T) {
	scanResult := map[string][][]byte{
		"10": {[]byte("1"), []byte("2")},
		"20": {[]byte("3")},
	}
	im := &mockIndexManager{scanResult: scanResult}
	fsm, _ := newTestFSM(WithIndexManager(im))

	result, err := fsm.Lookup(context.Background(), index.IndexScanRequest{
		IndexName: "price_idx",
		Start:     []byte("10"),
		End:       []byte("99"),
	})
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}

	m, ok := result.(map[string][][]byte)
	if !ok {
		t.Fatalf("expected map[string][][]byte, got %T", result)
	}
	if len(m) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(m))
	}
	if len(m["10"]) != 2 || len(m["20"]) != 1 {
		t.Fatalf("unexpected scan results: %v", m)
	}
}

func TestIndexLookup_Scan_Error(t *testing.T) {
	scanErr := errors.New("scan failed")
	im := &mockIndexManager{scanErr: scanErr}
	fsm, _ := newTestFSM(WithIndexManager(im))

	_, err := fsm.Lookup(context.Background(), index.IndexScanRequest{
		IndexName: "broken_idx",
		Start:     []byte("a"),
		End:       []byte("z"),
	})
	if err == nil {
		t.Fatal("expected error from index scan")
	}
	if !errors.Is(err, scanErr) {
		t.Fatalf("expected scan error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: lookupIndex - IndexListRequest
// ---------------------------------------------------------------------------

func TestIndexLookup_List_NilIndexManager(t *testing.T) {
	fsm, _ := newTestFSM() // no IndexManager

	_, err := fsm.Lookup(context.Background(), index.IndexListRequest{})
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}

	var svcErr *ServiceNotConfiguredError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestIndexLookup_List_Success(t *testing.T) {
	im := &mockIndexManager{
		listResult: []interface{}{"user_email", "user_phone"},
	}
	fsm, _ := newTestFSM(WithIndexManager(im))

	result, err := fsm.Lookup(context.Background(), index.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}

	list, ok := result.([]interface{})
	if !ok {
		t.Fatalf("expected []interface{}, got %T", result)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 indexes, got %d", len(list))
	}
}

func TestIndexLookup_List_Error(t *testing.T) {
	listErr := errors.New("list failed")
	im := &mockIndexManager{listErr: listErr}
	fsm, _ := newTestFSM(WithIndexManager(im))

	_, err := fsm.Lookup(context.Background(), index.IndexListRequest{})
	if err == nil {
		t.Fatal("expected error from index list")
	}
	if !errors.Is(err, listErr) {
		t.Fatalf("expected list error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: WithIndexManager option
// ---------------------------------------------------------------------------

func TestWithIndexManager_Option(t *testing.T) {
	im := &mockIndexManager{}
	fsm, _ := newTestFSM(WithIndexManager(im))

	if fsm.indexManager == nil {
		t.Fatal("expected indexManager to be set")
	}
}

// ---------------------------------------------------------------------------
// Tests: index create and drop round-trip
// ---------------------------------------------------------------------------

func TestIndexCreateDrop_RoundTrip(t *testing.T) {
	im := &mockIndexManager{}
	fsm, _ := newTestFSM(WithIndexManager(im))

	// Create an index.
	createCmd := mustEncode(protocol.EncodeIndexCreate("product_sku", "sku", true, true, false))
	entries := []sm.Entry{{Index: 1, Cmd: createCmd}}
	results := make([]sm.Result, 1)

	if err := fsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("IndexCreate failed: %v", err)
	}
	createRevision := results[0].Value

	// Drop the same index.
	dropCmd := mustEncode(protocol.EncodeIndexDrop("product_sku"))
	entries = []sm.Entry{{Index: 2, Cmd: dropCmd}}
	results = make([]sm.Result, 1)

	if err := fsm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("IndexDrop failed: %v", err)
	}
	dropRevision := results[0].Value

	// Revisions should be monotonically increasing.
	if dropRevision <= createRevision {
		t.Fatalf("expected drop revision %d > create revision %d", dropRevision, createRevision)
	}

	// Verify both calls were made.
	creates := im.getCreateCalls()
	if len(creates) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(creates))
	}
	if creates[0].name != "product_sku" || creates[0].fieldName != "sku" {
		t.Fatalf("unexpected create params: %+v", creates[0])
	}
	if !creates[0].opts.Unique || !creates[0].opts.Sparse {
		t.Fatal("expected unique=true, sparse=true")
	}

	drops := im.getDropCalls()
	if len(drops) != 1 {
		t.Fatalf("expected 1 drop call, got %d", len(drops))
	}
	if drops[0] != "product_sku" {
		t.Fatalf("expected drop name 'product_sku', got %q", drops[0])
	}
}
