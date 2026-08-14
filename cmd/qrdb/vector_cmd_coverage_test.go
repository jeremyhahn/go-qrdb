// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build qrdb_vector && !integration

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// executeVectorCmd wires up an embedded transport backed by noopVectorService,
// sets the global client, then runs the vector command with the given args.
// It returns the captured stdout output and any execution error.
func executeVectorCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()

	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	vecSvc := &noopVectorService{}

	tr, err := embedded.New(kvSvc, adminSvc, embedded.WithVectorService(vecSvc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := cl.Connect(context.Background()); err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer func() { _ = cl.Close() }()

	oldClient := client
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	client = cl
	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "table"

	cmd := newVectorCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs(args)

	execErr := cmd.ExecuteContext(context.Background())
	return buf.String(), execErr
}

// ---------------------------------------------------------------------------
// namespace create
// ---------------------------------------------------------------------------

func TestVectorCmd_NamespaceCreate_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "namespace", "create",
		"--namespace", "myns",
		"--dimension", "128",
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_NamespaceCreate_MissingNamespace(t *testing.T) {
	_, err := executeVectorCmd(t, "namespace", "create", "--dimension", "128")
	if err == nil {
		t.Fatal("expected error for missing --namespace flag")
	}
}

func TestVectorCmd_NamespaceCreate_MissingDimension(t *testing.T) {
	_, err := executeVectorCmd(t, "namespace", "create", "--namespace", "ns1")
	if err == nil {
		t.Fatal("expected error for missing --dimension flag")
	}
}

func TestVectorCmd_NamespaceCreate_InvalidDistanceMetric(t *testing.T) {
	_, err := executeVectorCmd(t, "namespace", "create",
		"--namespace", "ns1",
		"--dimension", "128",
		"--distance-metric", "hamming",
	)
	if err == nil {
		t.Fatal("expected error for invalid distance metric")
	}
}

func TestVectorCmd_NamespaceCreate_InvalidIndexType(t *testing.T) {
	_, err := executeVectorCmd(t, "namespace", "create",
		"--namespace", "ns1",
		"--dimension", "128",
		"--index-type", "annoy",
	)
	if err == nil {
		t.Fatal("expected error for invalid index type")
	}
}

// ---------------------------------------------------------------------------
// namespace delete
// ---------------------------------------------------------------------------

func TestVectorCmd_NamespaceDelete_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "namespace", "delete", "--namespace", "myns")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_NamespaceDelete_MissingNamespace(t *testing.T) {
	_, err := executeVectorCmd(t, "namespace", "delete")
	if err == nil {
		t.Fatal("expected error for missing --namespace flag")
	}
}

// ---------------------------------------------------------------------------
// namespace list
// ---------------------------------------------------------------------------

func TestVectorCmd_NamespaceList_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "namespace", "list")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_NamespaceList_JSONOutput(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	tr, err := embedded.New(kvSvc, adminSvc, embedded.WithVectorService(&noopVectorService{}))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := cl.Connect(context.Background()); err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer func() { _ = cl.Close() }()

	oldClient := client
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
		outputFormat = oldFormat
	})
	client = cl
	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "json"

	cmd := newVectorCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs([]string{"namespace", "list"})
	if runErr := cmd.ExecuteContext(context.Background()); runErr != nil {
		t.Fatalf("expected no error, got: %v", runErr)
	}
}

// ---------------------------------------------------------------------------
// namespace info
// ---------------------------------------------------------------------------

func TestVectorCmd_NamespaceInfo_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "namespace", "info", "--namespace", "myns")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_NamespaceInfo_MissingNamespace(t *testing.T) {
	_, err := executeVectorCmd(t, "namespace", "info")
	if err == nil {
		t.Fatal("expected error for missing --namespace flag")
	}
}

// ---------------------------------------------------------------------------
// upsert
// ---------------------------------------------------------------------------

func TestVectorCmd_Upsert_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "upsert",
		"--namespace", "myns",
		"--id", "1",
		"--vector", "1.0,2.0,3.0",
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_Upsert_WithMetadata(t *testing.T) {
	_, err := executeVectorCmd(t, "upsert",
		"--namespace", "myns",
		"--id", "2",
		"--vector", "1.0,2.0,3.0",
		"--metadata", `{"tag":"test"}`,
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_Upsert_MissingNamespace(t *testing.T) {
	_, err := executeVectorCmd(t, "upsert", "--id", "1", "--vector", "1.0,2.0")
	if err == nil {
		t.Fatal("expected error for missing --namespace flag")
	}
}

func TestVectorCmd_Upsert_InvalidVector(t *testing.T) {
	_, err := executeVectorCmd(t, "upsert",
		"--namespace", "ns",
		"--id", "1",
		"--vector", "not,a,vector",
	)
	if err == nil {
		t.Fatal("expected error for invalid vector values")
	}
}

func TestVectorCmd_Upsert_InvalidID(t *testing.T) {
	_, err := executeVectorCmd(t, "upsert",
		"--namespace", "ns",
		"--id", "notanumber",
		"--vector", "1.0,2.0",
	)
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
}

// ---------------------------------------------------------------------------
// get
// ---------------------------------------------------------------------------

func TestVectorCmd_Get_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "get",
		"--namespace", "myns",
		"--id", "42",
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_Get_MissingID(t *testing.T) {
	_, err := executeVectorCmd(t, "get", "--namespace", "myns")
	if err == nil {
		t.Fatal("expected error for missing --id flag")
	}
}

func TestVectorCmd_Get_InvalidConsistency(t *testing.T) {
	_, err := executeVectorCmd(t, "get",
		"--namespace", "ns",
		"--id", "1",
		"--consistency", "strong",
	)
	if err == nil {
		t.Fatal("expected error for invalid consistency level")
	}
}

// ---------------------------------------------------------------------------
// delete
// ---------------------------------------------------------------------------

func TestVectorCmd_Delete_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "delete",
		"--namespace", "myns",
		"--id", "10",
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_Delete_MissingNamespace(t *testing.T) {
	_, err := executeVectorCmd(t, "delete", "--id", "1")
	if err == nil {
		t.Fatal("expected error for missing --namespace flag")
	}
}

func TestVectorCmd_Delete_InvalidID(t *testing.T) {
	_, err := executeVectorCmd(t, "delete",
		"--namespace", "ns",
		"--id", "notanumber",
	)
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
}

// ---------------------------------------------------------------------------
// batch-upsert
// ---------------------------------------------------------------------------

func TestVectorCmd_BatchUpsert_Success(t *testing.T) {
	// Write a temp JSON file with vector data.
	tmpFile, err := os.CreateTemp(t.TempDir(), "vectors-*.json")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	_, _ = tmpFile.WriteString(`[{"id":1,"vector":[1.0,2.0,3.0]}]`)
	_ = tmpFile.Close()

	_, execErr := executeVectorCmd(t, "batch-upsert",
		"--namespace", "ns",
		"--file", tmpFile.Name(),
	)
	if execErr != nil {
		t.Fatalf("expected no error, got: %v", execErr)
	}
}

func TestVectorCmd_BatchUpsert_MissingFile(t *testing.T) {
	_, err := executeVectorCmd(t, "batch-upsert", "--namespace", "ns")
	if err == nil {
		t.Fatal("expected error for missing --file flag")
	}
}

func TestVectorCmd_BatchUpsert_InvalidJSONFile(t *testing.T) {
	tmpFile, err := os.CreateTemp(t.TempDir(), "bad-*.json")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	_, _ = tmpFile.WriteString("not json at all")
	_ = tmpFile.Close()

	_, execErr := executeVectorCmd(t, "batch-upsert",
		"--namespace", "ns",
		"--file", tmpFile.Name(),
	)
	if execErr == nil {
		t.Fatal("expected error for invalid JSON file")
	}
}

func TestVectorCmd_BatchUpsert_FileNotFound(t *testing.T) {
	_, err := executeVectorCmd(t, "batch-upsert",
		"--namespace", "ns",
		"--file", "/nonexistent/path/vectors.json",
	)
	if err == nil {
		t.Fatal("expected error for non-existent file")
	}
}

// ---------------------------------------------------------------------------
// batch-delete
// ---------------------------------------------------------------------------

func TestVectorCmd_BatchDelete_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "batch-delete",
		"--namespace", "ns",
		"--ids", "1,2,3",
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_BatchDelete_MissingIDs(t *testing.T) {
	_, err := executeVectorCmd(t, "batch-delete", "--namespace", "ns")
	if err == nil {
		t.Fatal("expected error for missing --ids flag")
	}
}

func TestVectorCmd_BatchDelete_InvalidIDs(t *testing.T) {
	_, err := executeVectorCmd(t, "batch-delete",
		"--namespace", "ns",
		"--ids", "1,bad,3",
	)
	if err == nil {
		t.Fatal("expected error for invalid IDs")
	}
}

// ---------------------------------------------------------------------------
// list
// ---------------------------------------------------------------------------

func TestVectorCmd_List_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "list", "--namespace", "myns")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_List_MissingNamespace(t *testing.T) {
	_, err := executeVectorCmd(t, "list")
	if err == nil {
		t.Fatal("expected error for missing --namespace flag")
	}
}

// ---------------------------------------------------------------------------
// update-metadata
// ---------------------------------------------------------------------------

func TestVectorCmd_UpdateMetadata_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "update-metadata",
		"--namespace", "ns",
		"--id", "5",
		"--metadata", `{"color":"blue"}`,
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_UpdateMetadata_MissingMetadata(t *testing.T) {
	_, err := executeVectorCmd(t, "update-metadata",
		"--namespace", "ns",
		"--id", "5",
	)
	if err == nil {
		t.Fatal("expected error for missing --metadata flag")
	}
}

func TestVectorCmd_UpdateMetadata_InvalidID(t *testing.T) {
	_, err := executeVectorCmd(t, "update-metadata",
		"--namespace", "ns",
		"--id", "notanumber",
		"--metadata", `{}`,
	)
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
}

func TestVectorCmd_UpdateMetadata_InvalidMetadataJSON(t *testing.T) {
	_, err := executeVectorCmd(t, "update-metadata",
		"--namespace", "ns",
		"--id", "1",
		"--metadata", "bad json",
	)
	if err == nil {
		t.Fatal("expected error for invalid metadata JSON")
	}
}

// ---------------------------------------------------------------------------
// search
// ---------------------------------------------------------------------------

func TestVectorCmd_Search_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "search",
		"--namespace", "ns",
		"--vector", "1.0,2.0,3.0",
		"--k", "5",
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_Search_MissingVector(t *testing.T) {
	_, err := executeVectorCmd(t, "search", "--namespace", "ns", "--k", "5")
	if err == nil {
		t.Fatal("expected error for missing --vector flag")
	}
}

func TestVectorCmd_Search_InvalidVector(t *testing.T) {
	_, err := executeVectorCmd(t, "search",
		"--namespace", "ns",
		"--vector", "bad,values",
		"--k", "5",
	)
	if err == nil {
		t.Fatal("expected error for invalid vector")
	}
}

func TestVectorCmd_Search_InvalidConsistency(t *testing.T) {
	_, err := executeVectorCmd(t, "search",
		"--namespace", "ns",
		"--vector", "1.0,2.0",
		"--k", "5",
		"--consistency", "strong",
	)
	if err == nil {
		t.Fatal("expected error for invalid consistency level")
	}
}

// ---------------------------------------------------------------------------
// search-metadata
// ---------------------------------------------------------------------------

func TestVectorCmd_SearchMetadata_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "search-metadata",
		"--namespace", "ns",
		"--filters", `{"tag":"test"}`,
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_SearchMetadata_MissingFilters(t *testing.T) {
	_, err := executeVectorCmd(t, "search-metadata", "--namespace", "ns")
	if err == nil {
		t.Fatal("expected error for missing --filters flag")
	}
}

func TestVectorCmd_SearchMetadata_InvalidFilters(t *testing.T) {
	_, err := executeVectorCmd(t, "search-metadata",
		"--namespace", "ns",
		"--filters", "bad json",
	)
	if err == nil {
		t.Fatal("expected error for invalid filters JSON")
	}
}

func TestVectorCmd_SearchMetadata_InvalidConsistency(t *testing.T) {
	_, err := executeVectorCmd(t, "search-metadata",
		"--namespace", "ns",
		"--filters", `{}`,
		"--consistency", "strong",
	)
	if err == nil {
		t.Fatal("expected error for invalid consistency level")
	}
}

// ---------------------------------------------------------------------------
// search-hybrid
// ---------------------------------------------------------------------------

func TestVectorCmd_SearchHybrid_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "search-hybrid",
		"--namespace", "ns",
		"--vector", "1.0,2.0,3.0",
		"--filters", `{"tag":"test"}`,
		"--k", "5",
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_SearchHybrid_MissingVector(t *testing.T) {
	_, err := executeVectorCmd(t, "search-hybrid",
		"--namespace", "ns",
		"--filters", `{}`,
		"--k", "5",
	)
	if err == nil {
		t.Fatal("expected error for missing --vector flag")
	}
}

func TestVectorCmd_SearchHybrid_InvalidVector(t *testing.T) {
	_, err := executeVectorCmd(t, "search-hybrid",
		"--namespace", "ns",
		"--vector", "bad",
		"--filters", `{}`,
		"--k", "5",
	)
	if err == nil {
		t.Fatal("expected error for invalid vector")
	}
}

func TestVectorCmd_SearchHybrid_InvalidFilters(t *testing.T) {
	_, err := executeVectorCmd(t, "search-hybrid",
		"--namespace", "ns",
		"--vector", "1.0,2.0",
		"--filters", "bad json",
		"--k", "5",
	)
	if err == nil {
		t.Fatal("expected error for invalid filters JSON")
	}
}

func TestVectorCmd_SearchHybrid_InvalidConsistency(t *testing.T) {
	_, err := executeVectorCmd(t, "search-hybrid",
		"--namespace", "ns",
		"--vector", "1.0,2.0",
		"--filters", `{}`,
		"--k", "5",
		"--consistency", "strong",
	)
	if err == nil {
		t.Fatal("expected error for invalid consistency level")
	}
}

// ---------------------------------------------------------------------------
// search-by-id
// ---------------------------------------------------------------------------

func TestVectorCmd_SearchByID_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "search-by-id",
		"--namespace", "ns",
		"--id", "42",
		"--k", "5",
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_SearchByID_MissingID(t *testing.T) {
	_, err := executeVectorCmd(t, "search-by-id", "--namespace", "ns", "--k", "5")
	if err == nil {
		t.Fatal("expected error for missing --id flag")
	}
}

func TestVectorCmd_SearchByID_InvalidID(t *testing.T) {
	_, err := executeVectorCmd(t, "search-by-id",
		"--namespace", "ns",
		"--id", "notanumber",
		"--k", "5",
	)
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
}

func TestVectorCmd_SearchByID_InvalidConsistency(t *testing.T) {
	_, err := executeVectorCmd(t, "search-by-id",
		"--namespace", "ns",
		"--id", "1",
		"--k", "5",
		"--consistency", "strong",
	)
	if err == nil {
		t.Fatal("expected error for invalid consistency level")
	}
}

// ---------------------------------------------------------------------------
// rebuild-index
// ---------------------------------------------------------------------------

func TestVectorCmd_RebuildIndex_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "rebuild-index", "--namespace", "ns")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_RebuildIndex_MissingNamespace(t *testing.T) {
	_, err := executeVectorCmd(t, "rebuild-index")
	if err == nil {
		t.Fatal("expected error for missing --namespace flag")
	}
}

// ---------------------------------------------------------------------------
// rebuild-progress
// ---------------------------------------------------------------------------

func TestVectorCmd_RebuildProgress_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "rebuild-progress",
		"--namespace", "ns",
		"--job-id", "job-123",
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_RebuildProgress_MissingJobID(t *testing.T) {
	_, err := executeVectorCmd(t, "rebuild-progress", "--namespace", "ns")
	if err == nil {
		t.Fatal("expected error for missing --job-id flag")
	}
}

// ---------------------------------------------------------------------------
// index-stats
// ---------------------------------------------------------------------------

func TestVectorCmd_IndexStats_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "index-stats", "--namespace", "ns")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_IndexStats_MissingNamespace(t *testing.T) {
	_, err := executeVectorCmd(t, "index-stats")
	if err == nil {
		t.Fatal("expected error for missing --namespace flag")
	}
}

// ---------------------------------------------------------------------------
// compact
// ---------------------------------------------------------------------------

func TestVectorCmd_Compact_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "compact", "--namespace", "ns")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_Compact_MissingNamespace(t *testing.T) {
	_, err := executeVectorCmd(t, "compact")
	if err == nil {
		t.Fatal("expected error for missing --namespace flag")
	}
}

// ---------------------------------------------------------------------------
// compact-progress
// ---------------------------------------------------------------------------

func TestVectorCmd_CompactProgress_Success(t *testing.T) {
	_, err := executeVectorCmd(t, "compact-progress",
		"--namespace", "ns",
		"--operation-id", "op-456",
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_CompactProgress_MissingOperationID(t *testing.T) {
	_, err := executeVectorCmd(t, "compact-progress", "--namespace", "ns")
	if err == nil {
		t.Fatal("expected error for missing --operation-id flag")
	}
}

// ---------------------------------------------------------------------------
// Output format coverage - JSON/YAML paths for commands with switch statements
// ---------------------------------------------------------------------------

// executeVectorCmdWithFormat is like executeVectorCmd but also sets outputFormat.
func executeVectorCmdWithFormat(t *testing.T, format string, args ...string) (string, error) {
	t.Helper()

	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	tr, err := embedded.New(kvSvc, adminSvc, embedded.WithVectorService(&noopVectorService{}))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := cl.Connect(context.Background()); err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer func() { _ = cl.Close() }()

	oldClient := client
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
		outputFormat = oldFormat
	})
	client = cl
	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = format

	cmd := newVectorCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs(args)
	execErr := cmd.ExecuteContext(context.Background())
	return buf.String(), execErr
}

func TestVectorCmd_NamespaceCreate_JSONOutput(t *testing.T) {
	_, err := executeVectorCmdWithFormat(t, "json", "namespace", "create",
		"--namespace", "myns",
		"--dimension", "128",
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_NamespaceCreate_YAMLOutput(t *testing.T) {
	_, err := executeVectorCmdWithFormat(t, "yaml", "namespace", "create",
		"--namespace", "myns",
		"--dimension", "128",
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_NamespaceList_YAMLOutput(t *testing.T) {
	_, err := executeVectorCmdWithFormat(t, "yaml", "namespace", "list")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_Upsert_JSONOutput(t *testing.T) {
	_, err := executeVectorCmdWithFormat(t, "json", "upsert",
		"--namespace", "ns",
		"--id", "1",
		"--vector", "1.0,2.0",
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_Upsert_YAMLOutput(t *testing.T) {
	_, err := executeVectorCmdWithFormat(t, "yaml", "upsert",
		"--namespace", "ns",
		"--id", "1",
		"--vector", "1.0,2.0",
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_BatchUpsert_JSONOutput(t *testing.T) {
	tmpFile, err := os.CreateTemp(t.TempDir(), "vectors-*.json")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	_, _ = tmpFile.WriteString(`[{"id":1,"vector":[1.0,2.0,3.0]}]`)
	_ = tmpFile.Close()

	_, execErr := executeVectorCmdWithFormat(t, "json", "batch-upsert",
		"--namespace", "ns",
		"--file", tmpFile.Name(),
	)
	if execErr != nil {
		t.Fatalf("expected no error, got: %v", execErr)
	}
}

func TestVectorCmd_BatchUpsert_YAMLOutput(t *testing.T) {
	tmpFile, err := os.CreateTemp(t.TempDir(), "vectors-*.json")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	_, _ = tmpFile.WriteString(`[{"id":1,"vector":[1.0,2.0]}]`)
	_ = tmpFile.Close()

	_, execErr := executeVectorCmdWithFormat(t, "yaml", "batch-upsert",
		"--namespace", "ns",
		"--file", tmpFile.Name(),
	)
	if execErr != nil {
		t.Fatalf("expected no error, got: %v", execErr)
	}
}

func TestVectorCmd_BatchDelete_JSONOutput(t *testing.T) {
	_, err := executeVectorCmdWithFormat(t, "json", "batch-delete",
		"--namespace", "ns",
		"--ids", "1,2",
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_BatchDelete_YAMLOutput(t *testing.T) {
	_, err := executeVectorCmdWithFormat(t, "yaml", "batch-delete",
		"--namespace", "ns",
		"--ids", "1,2",
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_List_JSONOutput(t *testing.T) {
	_, err := executeVectorCmdWithFormat(t, "json", "list", "--namespace", "ns")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_List_YAMLOutput(t *testing.T) {
	_, err := executeVectorCmdWithFormat(t, "yaml", "list", "--namespace", "ns")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_SearchMetadata_JSONOutput(t *testing.T) {
	_, err := executeVectorCmdWithFormat(t, "json", "search-metadata",
		"--namespace", "ns",
		"--filters", `{}`,
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_SearchMetadata_YAMLOutput(t *testing.T) {
	_, err := executeVectorCmdWithFormat(t, "yaml", "search-metadata",
		"--namespace", "ns",
		"--filters", `{}`,
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_RebuildIndex_JSONOutput(t *testing.T) {
	_, err := executeVectorCmdWithFormat(t, "json", "rebuild-index", "--namespace", "ns")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_RebuildIndex_YAMLOutput(t *testing.T) {
	_, err := executeVectorCmdWithFormat(t, "yaml", "rebuild-index", "--namespace", "ns")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_IndexStats_JSONOutput(t *testing.T) {
	_, err := executeVectorCmdWithFormat(t, "json", "index-stats", "--namespace", "ns")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_IndexStats_YAMLOutput(t *testing.T) {
	_, err := executeVectorCmdWithFormat(t, "yaml", "index-stats", "--namespace", "ns")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_Compact_JSONOutput(t *testing.T) {
	_, err := executeVectorCmdWithFormat(t, "json", "compact", "--namespace", "ns")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestVectorCmd_Compact_YAMLOutput(t *testing.T) {
	_, err := executeVectorCmdWithFormat(t, "yaml", "compact", "--namespace", "ns")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}
