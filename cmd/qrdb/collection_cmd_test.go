//go:build !integration

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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// mockCollectionService — configurable service.CollectionServicer mock
// ---------------------------------------------------------------------------

type mockCollectionService struct {
	createCollectionFn  func(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error)
	dropCollectionFn    func(ctx context.Context, req *service.DropCollectionRequest) error
	listCollectionsFn   func(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error)
	getCollectionInfoFn func(ctx context.Context, database, name string) (*service.CollectionInfo, error)
	insertDocumentFn    func(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error)
	getDocumentFn       func(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error)
	updateDocumentFn    func(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error)
	deleteDocumentFn    func(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error)
	listDocumentsFn     func(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error)
}

var _ service.CollectionServicer = (*mockCollectionService)(nil)

func (m *mockCollectionService) CreateCollection(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	if m.createCollectionFn != nil {
		return m.createCollectionFn(ctx, req)
	}
	return &service.CollectionInfo{Name: req.Name, Database: req.Database, IDStrategy: req.IDStrategy}, nil
}

func (m *mockCollectionService) DropCollection(ctx context.Context, req *service.DropCollectionRequest) error {
	if m.dropCollectionFn != nil {
		return m.dropCollectionFn(ctx, req)
	}
	return nil
}

func (m *mockCollectionService) ListCollections(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
	if m.listCollectionsFn != nil {
		return m.listCollectionsFn(ctx, req)
	}
	return &service.ListCollectionsResponse{}, nil
}

func (m *mockCollectionService) GetCollectionInfo(ctx context.Context, database, name string) (*service.CollectionInfo, error) {
	if m.getCollectionInfoFn != nil {
		return m.getCollectionInfoFn(ctx, database, name)
	}
	return &service.CollectionInfo{Name: name, Database: database}, nil
}

func (m *mockCollectionService) InsertDocument(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
	if m.insertDocumentFn != nil {
		return m.insertDocumentFn(ctx, req)
	}
	return &service.InsertDocumentResponse{ID: 1}, nil
}

func (m *mockCollectionService) GetDocument(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	if m.getDocumentFn != nil {
		return m.getDocumentFn(ctx, req)
	}
	return &service.GetDocumentResponse{ID: req.ID}, nil
}

func (m *mockCollectionService) UpdateDocument(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
	if m.updateDocumentFn != nil {
		return m.updateDocumentFn(ctx, req)
	}
	return &service.UpdateDocumentResponse{ID: req.ID}, nil
}

func (m *mockCollectionService) DeleteDocument(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
	if m.deleteDocumentFn != nil {
		return m.deleteDocumentFn(ctx, req)
	}
	return &service.DeleteDocumentResponse{Deleted: true}, nil
}

func (m *mockCollectionService) ListDocuments(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
	if m.listDocumentsFn != nil {
		return m.listDocumentsFn(ctx, req)
	}
	return &service.ListDocumentsResponse{}, nil
}

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

// executeCollectionCmd builds an embedded SDK client with the given
// collection service, sets the package-level client, builds the
// "collection" command tree, and executes it with the given args.
func executeCollectionCmd(t *testing.T, svc service.CollectionServicer, args ...string) (string, error) {
	t.Helper()

	tr, err := embedded.New(&noopKVService{}, newMockAdminService(),
		embedded.WithCollectionService(svc),
	)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient := client
	oldWriter := outputWriter
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
	})

	client = sdkClient
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newCollectionCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs(args)

	execErr := cmd.ExecuteContext(context.Background())
	return buf.String(), execErr
}

// ---------------------------------------------------------------------------
// effectiveDatabaseScope (L7) — the persistent root --database flag vs each
// collection subcommand's own local --database flag.
//
// executeCollectionCmd above runs the standalone "collection" command
// subtree (as newCollectionCmd() returns it, with no parent), which never
// registers the persistent root --database flag at all — exactly the
// pre-fix shape where the two flags cannot interact. These tests instead
// run through the FULL root command tree, as `qrdb --database <x>
// collection ...` does in production, wiring the mock collection service
// through a PersistentPreRunE override mirroring newTestRootCmd
// (cmd_test.go) plus collection.go's WithCollectionService option.
// ---------------------------------------------------------------------------

// executeCollectionCmdViaRoot runs args through the full root command tree
// with svc wired in as the collection service, so the persistent root
// --database flag registered by newRootCmd is present and can be asserted
// against collection.go's own local --database flag.
func executeCollectionCmdViaRoot(t *testing.T, svc service.CollectionServicer, args ...string) (string, error) {
	t.Helper()

	root := newRootCmd()
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Name() == "version" || cmd.Name() == "help" {
			return nil
		}
		format, _ := cmd.Root().PersistentFlags().GetString("output")
		if err := validateOutputFormat(format); err != nil {
			return err
		}
		outputFormat = format

		tr, err := embedded.New(&noopKVService{}, newMockAdminService(), embedded.WithCollectionService(svc))
		if err != nil {
			return err
		}
		c, err := sdk.NewClient(tr)
		if err != nil {
			return err
		}
		client = c
		return nil
	}

	oldClient := client
	oldWriter := outputWriter
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
	})

	var buf bytes.Buffer
	outputWriter = &buf
	root.SetOut(io.Discard)
	root.SetErr(os.Stderr)
	root.SetArgs(args)

	execErr := root.ExecuteContext(context.Background())
	return buf.String(), execErr
}

// TestEffectiveDatabaseScope exercises effectiveDatabaseScope directly
// against synthetic cobra command trees, covering the cases the
// integration-style tests below cannot cheaply isolate: no root persistent
// --database flag registered at all (the standalone command-subtree shape
// executeCollectionCmd/executeSQLCmd use in every other test in this
// package), and a root flag that is registered but left unset.
func TestEffectiveDatabaseScope(t *testing.T) {
	newLeaf := func(withRootDatabaseFlag bool, rootArgs, localArgs []string) *cobra.Command {
		var local string
		leaf := &cobra.Command{Use: "leaf", RunE: func(*cobra.Command, []string) error { return nil }}
		leaf.Flags().StringVar(&local, "database", transport.DefaultDatabaseScope, "")

		root := &cobra.Command{Use: "root"}
		if withRootDatabaseFlag {
			var global string
			root.PersistentFlags().StringVar(&global, "database", "", "")
		}
		root.AddCommand(leaf)

		args := append(append([]string{}, rootArgs...), append([]string{"leaf"}, localArgs...)...)
		root.SetArgs(args)
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		if err := root.Execute(); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		return leaf
	}

	t.Run("no root flag registered falls back to local default", func(t *testing.T) {
		leaf := newLeaf(false, nil, nil)
		local, _ := leaf.Flags().GetString("database")
		if got := effectiveDatabaseScope(leaf, local); got != transport.DefaultDatabaseScope {
			t.Errorf("effectiveDatabaseScope = %q, want default %q", got, transport.DefaultDatabaseScope)
		}
	})

	t.Run("root flag set, local unset: honors root flag", func(t *testing.T) {
		leaf := newLeaf(true, []string{"--database", "globaldb"}, nil)
		local, _ := leaf.Flags().GetString("database")
		if got := effectiveDatabaseScope(leaf, local); got != "globaldb" {
			t.Errorf("effectiveDatabaseScope = %q, want globaldb", got)
		}
	})

	t.Run("root flag set, local also set: local wins", func(t *testing.T) {
		leaf := newLeaf(true, []string{"--database", "globaldb"}, []string{"--database", "localdb"})
		local, _ := leaf.Flags().GetString("database")
		if got := effectiveDatabaseScope(leaf, local); got != "localdb" {
			t.Errorf("effectiveDatabaseScope = %q, want localdb", got)
		}
	})

	t.Run("root flag registered but unset, local unset: falls back to local default", func(t *testing.T) {
		leaf := newLeaf(true, nil, nil)
		local, _ := leaf.Flags().GetString("database")
		if got := effectiveDatabaseScope(leaf, local); got != transport.DefaultDatabaseScope {
			t.Errorf("effectiveDatabaseScope = %q, want default %q", got, transport.DefaultDatabaseScope)
		}
	})
}

// TestCollectionList_HonorsGlobalDatabaseFlag proves the persistent root
// --database flag is NOT silently ignored when 'collection list' declares
// its own local --database flag: without the fix,
// `qrdb --database globaldb collection list` would always target "system"
// (transport.DefaultDatabaseScope) instead.
func TestCollectionList_HonorsGlobalDatabaseFlag(t *testing.T) {
	var captured *service.ListCollectionsRequest
	svc := &mockCollectionService{
		listCollectionsFn: func(_ context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
			captured = req
			return &service.ListCollectionsResponse{}, nil
		},
	}

	_, err := executeCollectionCmdViaRoot(t, svc, "--database", "globaldb", "collection", "list")
	if err != nil {
		t.Fatalf("collection list via root: %v", err)
	}
	if captured == nil || captured.Database != "globaldb" {
		t.Fatalf("captured request = %+v, want Database=globaldb (root --database must not be silently ignored)", captured)
	}
}

// TestCollectionList_LocalDatabaseFlagOverridesGlobal proves the more
// specific local --database flag wins when both are supplied.
func TestCollectionList_LocalDatabaseFlagOverridesGlobal(t *testing.T) {
	var captured *service.ListCollectionsRequest
	svc := &mockCollectionService{
		listCollectionsFn: func(_ context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
			captured = req
			return &service.ListCollectionsResponse{}, nil
		},
	}

	_, err := executeCollectionCmdViaRoot(t, svc, "--database", "globaldb", "collection", "list", "--database", "localdb")
	if err != nil {
		t.Fatalf("collection list via root: %v", err)
	}
	if captured == nil || captured.Database != "localdb" {
		t.Fatalf("captured request = %+v, want Database=localdb (explicit local flag is the more specific request)", captured)
	}
}

// TestCollectionCreate_HonorsGlobalDatabaseFlag mirrors the List case for a
// command whose Database field is set through a request struct literal
// (transport.CreateCollectionRequest) rather than a positional SDK arg.
func TestCollectionCreate_HonorsGlobalDatabaseFlag(t *testing.T) {
	var captured *service.CreateCollectionRequest
	svc := &mockCollectionService{
		createCollectionFn: func(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
			captured = req
			return &service.CollectionInfo{Name: req.Name, Database: req.Database}, nil
		},
	}

	_, err := executeCollectionCmdViaRoot(t, svc, "--database", "globaldb", "collection", "create", "widgets")
	if err != nil {
		t.Fatalf("collection create via root: %v", err)
	}
	if captured == nil || captured.Database != "globaldb" {
		t.Fatalf("captured request = %+v, want Database=globaldb", captured)
	}
}

// TestCollectionList_NoGlobalFlag_DefaultsAsBefore proves the fix is purely
// additive: with no root --database flag set, behavior is unchanged from
// before the fix (falls back to transport.DefaultDatabaseScope).
func TestCollectionList_NoGlobalFlag_DefaultsAsBefore(t *testing.T) {
	var captured *service.ListCollectionsRequest
	svc := &mockCollectionService{
		listCollectionsFn: func(_ context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
			captured = req
			return &service.ListCollectionsResponse{}, nil
		},
	}

	_, err := executeCollectionCmdViaRoot(t, svc, "collection", "list")
	if err != nil {
		t.Fatalf("collection list via root: %v", err)
	}
	if captured == nil || captured.Database != transport.DefaultDatabaseScope {
		t.Fatalf("captured request = %+v, want Database=%q (unchanged default)", captured, transport.DefaultDatabaseScope)
	}
}

// ---------------------------------------------------------------------------
// collection create
// ---------------------------------------------------------------------------

func TestCollectionCreate_Success(t *testing.T) {
	var captured *service.CreateCollectionRequest
	svc := &mockCollectionService{
		createCollectionFn: func(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
			captured = req
			return &service.CollectionInfo{
				Name:       req.Name,
				Database:   req.Database,
				IDStrategy: "unix_nano",
				CreatedAt:  time.Now(),
			}, nil
		},
	}

	output, err := executeCollectionCmd(t, svc, "create", "widgets", "--database", "mydb", "--id-strategy", "unix_nano")
	if err != nil {
		t.Fatalf("collection create error: %v", err)
	}
	if captured == nil {
		t.Fatal("CreateCollection was not called")
	}
	if captured.Database != "mydb" || captured.Name != "widgets" {
		t.Errorf("captured request = %+v, want database=mydb name=widgets", captured)
	}
	if !strings.Contains(output, "widgets") {
		t.Errorf("output missing collection name, got: %q", output)
	}
}

func TestCollectionCreate_DefaultDatabase(t *testing.T) {
	var captured *service.CreateCollectionRequest
	svc := &mockCollectionService{
		createCollectionFn: func(_ context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
			captured = req
			return &service.CollectionInfo{Name: req.Name, Database: req.Database}, nil
		},
	}

	_, err := executeCollectionCmd(t, svc, "create", "widgets")
	if err != nil {
		t.Fatalf("collection create error: %v", err)
	}
	if captured.Database != transport.DefaultDatabaseScope {
		t.Errorf("Database = %q, want default scope %q", captured.Database, transport.DefaultDatabaseScope)
	}
}

func TestCollectionCreate_MissingName(t *testing.T) {
	svc := &mockCollectionService{}
	_, err := executeCollectionCmd(t, svc, "create")
	if err == nil {
		t.Fatal("expected error for missing collection name argument")
	}
}

func TestCollectionCreate_ServiceError(t *testing.T) {
	svc := &mockCollectionService{
		createCollectionFn: func(_ context.Context, _ *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
			return nil, errors.New("create failed")
		},
	}
	_, err := executeCollectionCmd(t, svc, "create", "widgets")
	if err == nil {
		t.Fatal("expected error from create command")
	}
}

// ---------------------------------------------------------------------------
// collection info
// ---------------------------------------------------------------------------

func TestCollectionInfo_JSON(t *testing.T) {
	svc := &mockCollectionService{
		getCollectionInfoFn: func(_ context.Context, database, name string) (*service.CollectionInfo, error) {
			return &service.CollectionInfo{Name: name, Database: database, DocumentCount: 5}, nil
		},
	}

	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	output, err := executeCollectionCmd(t, svc, "info", "widgets", "--database", "mydb")
	if err != nil {
		t.Fatalf("collection info error: %v", err)
	}

	var info transport.CollectionInfo
	if jsonErr := json.Unmarshal([]byte(output), &info); jsonErr != nil {
		t.Fatalf("JSON unmarshal error: %v, output: %q", jsonErr, output)
	}
	if info.DocumentCount != 5 {
		t.Errorf("DocumentCount = %d, want 5", info.DocumentCount)
	}
}

func TestCollectionInfo_ServiceError(t *testing.T) {
	svc := &mockCollectionService{
		getCollectionInfoFn: func(_ context.Context, _, _ string) (*service.CollectionInfo, error) {
			return nil, errors.New("not found")
		},
	}
	_, err := executeCollectionCmd(t, svc, "info", "widgets")
	if err == nil {
		t.Fatal("expected error from info command")
	}
}

// ---------------------------------------------------------------------------
// collection list
// ---------------------------------------------------------------------------

func TestCollectionList_Table(t *testing.T) {
	svc := &mockCollectionService{
		listCollectionsFn: func(_ context.Context, _ *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
			return &service.ListCollectionsResponse{
				Collections: []service.CollectionInfo{
					{Name: "widgets", Database: "mydb", IDStrategy: "unix_nano", DocumentCount: 3},
				},
			}, nil
		},
	}

	output, err := executeCollectionCmd(t, svc, "list", "--database", "mydb")
	if err != nil {
		t.Fatalf("collection list error: %v", err)
	}
	for _, want := range []string{"NAME", "widgets", "mydb"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestCollectionList_ServiceError(t *testing.T) {
	svc := &mockCollectionService{
		listCollectionsFn: func(_ context.Context, _ *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
			return nil, errors.New("list failed")
		},
	}
	_, err := executeCollectionCmd(t, svc, "list")
	if err == nil {
		t.Fatal("expected error from list command")
	}
}

// ---------------------------------------------------------------------------
// collection drop
// ---------------------------------------------------------------------------

func TestCollectionDrop_Success(t *testing.T) {
	var captured *service.DropCollectionRequest
	svc := &mockCollectionService{
		dropCollectionFn: func(_ context.Context, req *service.DropCollectionRequest) error {
			captured = req
			return nil
		},
	}

	output, err := executeCollectionCmd(t, svc, "drop", "widgets", "--database", "mydb")
	if err != nil {
		t.Fatalf("collection drop error: %v", err)
	}
	if captured.Name != "widgets" || captured.Database != "mydb" {
		t.Errorf("captured request = %+v, want name=widgets database=mydb", captured)
	}
	if !strings.Contains(output, "OK") {
		t.Errorf("output missing OK, got: %q", output)
	}
}

func TestCollectionDrop_ServiceError(t *testing.T) {
	svc := &mockCollectionService{
		dropCollectionFn: func(_ context.Context, _ *service.DropCollectionRequest) error {
			return errors.New("drop failed")
		},
	}
	_, err := executeCollectionCmd(t, svc, "drop", "widgets")
	if err == nil {
		t.Fatal("expected error from drop command")
	}
}

// ---------------------------------------------------------------------------
// collection doc insert
// ---------------------------------------------------------------------------

func TestCollectionDocInsert_InlineData(t *testing.T) {
	var captured *service.InsertDocumentRequest
	svc := &mockCollectionService{
		insertDocumentFn: func(_ context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
			captured = req
			return &service.InsertDocumentResponse{ID: 42}, nil
		},
	}

	output, err := executeCollectionCmd(t, svc, "doc", "insert", "widgets",
		"--database", "mydb", "--data", `{"name":"alice"}`)
	if err != nil {
		t.Fatalf("doc insert error: %v", err)
	}
	if captured.Collection != "widgets" || captured.Database != "mydb" {
		t.Errorf("captured request = %+v", captured)
	}
	if captured.Document["name"] != "alice" {
		t.Errorf("Document[name] = %v, want alice", captured.Document["name"])
	}
	if !strings.Contains(output, "42") {
		t.Errorf("output missing assigned ID, got: %q", output)
	}
}

func TestCollectionDocInsert_FileData(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/doc.json"
	if err := os.WriteFile(path, []byte(`{"name":"bob"}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var captured *service.InsertDocumentRequest
	svc := &mockCollectionService{
		insertDocumentFn: func(_ context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
			captured = req
			return &service.InsertDocumentResponse{ID: 7}, nil
		},
	}

	_, err := executeCollectionCmd(t, svc, "doc", "insert", "widgets", "--file", path)
	if err != nil {
		t.Fatalf("doc insert --file error: %v", err)
	}
	if captured.Document["name"] != "bob" {
		t.Errorf("Document[name] = %v, want bob", captured.Document["name"])
	}
}

func TestCollectionDocInsert_MissingDataAndFile(t *testing.T) {
	svc := &mockCollectionService{}
	_, err := executeCollectionCmd(t, svc, "doc", "insert", "widgets")
	if err == nil {
		t.Fatal("expected error when neither --data nor --file is supplied")
	}
	var missing *MissingArgError
	if !errors.As(err, &missing) {
		t.Errorf("expected *MissingArgError, got %T: %v", err, err)
	}
}

func TestCollectionDocInsert_InvalidJSON(t *testing.T) {
	svc := &mockCollectionService{}
	_, err := executeCollectionCmd(t, svc, "doc", "insert", "widgets", "--data", "not-json")
	if err == nil {
		t.Fatal("expected error for invalid JSON document")
	}
	var jsonErr *DocumentJSONError
	if !errors.As(err, &jsonErr) {
		t.Errorf("expected *DocumentJSONError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// collection doc get
// ---------------------------------------------------------------------------

func TestCollectionDocGet_Success(t *testing.T) {
	svc := &mockCollectionService{
		getDocumentFn: func(_ context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
			return &service.GetDocumentResponse{ID: req.ID, Document: map[string]interface{}{"name": "carol"}}, nil
		},
	}

	output, err := executeCollectionCmd(t, svc, "doc", "get", "widgets", "5")
	if err != nil {
		t.Fatalf("doc get error: %v", err)
	}
	if !strings.Contains(output, "carol") {
		t.Errorf("output missing document body, got: %q", output)
	}
}

func TestCollectionDocGet_InvalidID(t *testing.T) {
	svc := &mockCollectionService{}
	_, err := executeCollectionCmd(t, svc, "doc", "get", "widgets", "not-a-number")
	if err == nil {
		t.Fatal("expected error for invalid document ID")
	}
	var idErr *InvalidDocumentIDError
	if !errors.As(err, &idErr) {
		t.Errorf("expected *InvalidDocumentIDError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// collection doc update
// ---------------------------------------------------------------------------

func TestCollectionDocUpdate_Success(t *testing.T) {
	var captured *service.UpdateDocumentRequest
	svc := &mockCollectionService{
		updateDocumentFn: func(_ context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
			captured = req
			return &service.UpdateDocumentResponse{ID: req.ID}, nil
		},
	}

	output, err := executeCollectionCmd(t, svc, "doc", "update", "widgets", "9", "--data", `{"name":"dave"}`)
	if err != nil {
		t.Fatalf("doc update error: %v", err)
	}
	if captured.ID != 9 {
		t.Errorf("ID = %d, want 9", captured.ID)
	}
	if !strings.Contains(output, "OK") {
		t.Errorf("output missing OK, got: %q", output)
	}
}

func TestCollectionDocUpdate_InvalidID(t *testing.T) {
	svc := &mockCollectionService{}
	_, err := executeCollectionCmd(t, svc, "doc", "update", "widgets", "bad-id", "--data", "{}")
	if err == nil {
		t.Fatal("expected error for invalid document ID")
	}
}

func TestCollectionDocUpdate_ServiceError(t *testing.T) {
	svc := &mockCollectionService{
		updateDocumentFn: func(_ context.Context, _ *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
			return nil, errors.New("update failed")
		},
	}
	_, err := executeCollectionCmd(t, svc, "doc", "update", "widgets", "1", "--data", "{}")
	if err == nil {
		t.Fatal("expected error from update command")
	}
}

// ---------------------------------------------------------------------------
// collection doc delete
// ---------------------------------------------------------------------------

func TestCollectionDocDelete_Success(t *testing.T) {
	var captured *service.DeleteDocumentRequest
	svc := &mockCollectionService{
		deleteDocumentFn: func(_ context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
			captured = req
			return &service.DeleteDocumentResponse{Deleted: true}, nil
		},
	}

	output, err := executeCollectionCmd(t, svc, "doc", "delete", "widgets", "3")
	if err != nil {
		t.Fatalf("doc delete error: %v", err)
	}
	if captured.ID != 3 {
		t.Errorf("ID = %d, want 3", captured.ID)
	}
	if !strings.Contains(output, "OK") {
		t.Errorf("output missing OK, got: %q", output)
	}
}

func TestCollectionDocDelete_ServiceError(t *testing.T) {
	svc := &mockCollectionService{
		deleteDocumentFn: func(_ context.Context, _ *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
			return nil, errors.New("delete failed")
		},
	}
	_, err := executeCollectionCmd(t, svc, "doc", "delete", "widgets", "1")
	if err == nil {
		t.Fatal("expected error from delete command")
	}
}

// ---------------------------------------------------------------------------
// collection doc list
// ---------------------------------------------------------------------------

func TestCollectionDocList_Table(t *testing.T) {
	svc := &mockCollectionService{
		listDocumentsFn: func(_ context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
			return &service.ListDocumentsResponse{
				Documents: []service.GetDocumentResponse{
					{ID: 1, Document: map[string]interface{}{"name": "erin"}},
				},
				NextPageToken: "tok-2",
				TotalCount:    1,
			}, nil
		},
	}

	output, err := executeCollectionCmd(t, svc, "doc", "list", "widgets", "--page-size", "10")
	if err != nil {
		t.Fatalf("doc list error: %v", err)
	}
	for _, want := range []string{"ID", "DOCUMENT", "erin", "tok-2"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q, got: %q", want, output)
		}
	}
}

func TestCollectionDocList_JSON(t *testing.T) {
	svc := &mockCollectionService{
		listDocumentsFn: func(_ context.Context, _ *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
			return &service.ListDocumentsResponse{
				Documents:  []service.GetDocumentResponse{{ID: 1, Document: map[string]interface{}{"a": 1.0}}},
				TotalCount: 1,
			}, nil
		},
	}

	oldFormat := outputFormat
	outputFormat = "json"
	t.Cleanup(func() { outputFormat = oldFormat })

	output, err := executeCollectionCmd(t, svc, "doc", "list", "widgets")
	if err != nil {
		t.Fatalf("doc list json error: %v", err)
	}

	var resp transport.ListDocumentsResponse
	if jsonErr := json.Unmarshal([]byte(output), &resp); jsonErr != nil {
		t.Fatalf("JSON unmarshal error: %v, output: %q", jsonErr, output)
	}
	if resp.TotalCount != 1 {
		t.Errorf("TotalCount = %d, want 1", resp.TotalCount)
	}
}

func TestCollectionDocList_ServiceError(t *testing.T) {
	svc := &mockCollectionService{
		listDocumentsFn: func(_ context.Context, _ *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
			return nil, errors.New("list failed")
		},
	}
	_, err := executeCollectionCmd(t, svc, "doc", "list", "widgets")
	if err == nil {
		t.Fatal("expected error from doc list command")
	}
}

// ---------------------------------------------------------------------------
// readDocumentJSON — direct unit tests
// ---------------------------------------------------------------------------

func TestReadDocumentJSON_InlineData(t *testing.T) {
	doc, err := readDocumentJSON(`{"a":1}`, "")
	if err != nil {
		t.Fatalf("readDocumentJSON: %v", err)
	}
	if doc["a"] != 1.0 {
		t.Errorf("doc[a] = %v, want 1", doc["a"])
	}
}

func TestReadDocumentJSON_Stdin(t *testing.T) {
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })

	go func() {
		_, _ = w.Write([]byte(`{"b":2}`))
		_ = w.Close()
	}()

	doc, err := readDocumentJSON("", "-")
	if err != nil {
		t.Fatalf("readDocumentJSON(stdin): %v", err)
	}
	if doc["b"] != 2.0 {
		t.Errorf("doc[b] = %v, want 2", doc["b"])
	}
}

func TestReadDocumentJSON_MissingBoth(t *testing.T) {
	_, err := readDocumentJSON("", "")
	if err == nil {
		t.Fatal("expected error when neither data nor file supplied")
	}
}

func TestReadDocumentJSON_FileNotFound(t *testing.T) {
	_, err := readDocumentJSON("", "/nonexistent/path/doc.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	var fileErr *FileReadError
	if !errors.As(err, &fileErr) {
		t.Errorf("expected *FileReadError, got %T: %v", err, err)
	}
}

// parseDocumentID direct tests
func TestParseDocumentID_Valid(t *testing.T) {
	id, err := parseDocumentID("123")
	if err != nil {
		t.Fatalf("parseDocumentID: %v", err)
	}
	if id != 123 {
		t.Errorf("id = %d, want 123", id)
	}
}

func TestParseDocumentID_Invalid(t *testing.T) {
	_, err := parseDocumentID("abc")
	if err == nil {
		t.Fatal("expected error for non-numeric document ID")
	}
}
