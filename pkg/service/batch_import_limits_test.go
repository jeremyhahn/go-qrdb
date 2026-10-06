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

package service

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// parseAndImportOf asserts svc is a *BatchService and returns it, so these
// tests can call parseAndImport directly -- the shared tail both the
// retired single-message Import and ImportCommit call -- rather than
// driving a whole chunked session (ImportBegin/ImportAppend/ImportCommit)
// just to reach it. These tests are about parseAndImport's own row/size/
// proposal-chunking limits, not session lifecycle; batch_import_session_test.go
// owns the session-level tests (quotas, ownership, idempotent replay, etc).
func parseAndImportOf(t *testing.T, svc BatchServicer) *BatchService {
	t.Helper()
	bs, ok := svc.(*BatchService)
	if !ok {
		t.Fatalf("svc is %T, not *BatchService", svc)
	}
	return bs
}

// ---------------------------------------------------------------------------
// Import - Row count limit
// ---------------------------------------------------------------------------

func TestBatchService_Import_TooManyRows(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	var sb strings.Builder
	sb.WriteString("id,value\n")
	for i := 0; i < maxImportRows+1; i++ {
		sb.WriteString("k")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString(",v\n")
	}

	_, err := parseAndImportOf(t, svc).parseAndImport(context.Background(), []byte(sb.String()), ImportOptions{
		Format:     "csv",
		KeyField:   "id",
		ValueMode:  "row",
		SkipHeader: true,
	})

	var itmr *ImportTooManyRowsError
	if !errors.As(err, &itmr) {
		t.Fatalf("expected *ImportTooManyRowsError, got %T: %v", err, err)
	}
	if itmr.Max != maxImportRows {
		t.Fatalf("expected Max=%d, got %d", maxImportRows, itmr.Max)
	}
}

// ---------------------------------------------------------------------------
// Import - Per-row key/value size limits
// ---------------------------------------------------------------------------

func TestBatchService_Import_KeyTooLarge_NonTransactional(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}
	svc := NewBatchService(host, 1)

	hugeKey := strings.Repeat("k", maxImportKeySize+1)
	csvData := "id,value\n" + hugeKey + ",v1\n"

	result, err := parseAndImportOf(t, svc).parseAndImport(context.Background(), []byte(csvData), ImportOptions{
		Format:     "csv",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
		SkipHeader: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("expected Failed=1, got %d", result.Failed)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Message, "key exceeds maximum size") {
		t.Fatalf("expected key-too-large error, got %+v", result.Errors)
	}
}

func TestBatchService_Import_KeyTooLarge_Transactional(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	hugeKey := strings.Repeat("k", maxImportKeySize+1)
	csvData := "id,value\n" + hugeKey + ",v1\n"

	_, err := parseAndImportOf(t, svc).parseAndImport(context.Background(), []byte(csvData), ImportOptions{
		Format:        "csv",
		KeyField:      "id",
		ValueMode:     "field",
		ValueField:    "value",
		SkipHeader:    true,
		Transactional: true,
	})

	var ita *ImportTransactionAbortedError
	if !errors.As(err, &ita) {
		t.Fatalf("expected *ImportTransactionAbortedError, got %T: %v", err, err)
	}
	if !strings.Contains(ita.Message, "key exceeds maximum size") {
		t.Fatalf("expected message about oversized key, got %q", ita.Message)
	}
}

func TestBatchService_Import_ValueTooLarge_NonTransactional(t *testing.T) {
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			return sm.Result{Value: 1}, nil
		},
	}
	svc := NewBatchService(host, 1)

	hugeValue := strings.Repeat("v", maxImportValueSize+1)
	csvData := "id,value\nkey1," + hugeValue + "\n"

	result, err := parseAndImportOf(t, svc).parseAndImport(context.Background(), []byte(csvData), ImportOptions{
		Format:     "csv",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
		SkipHeader: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("expected Failed=1, got %d", result.Failed)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Message, "value exceeds maximum size") {
		t.Fatalf("expected value-too-large error, got %+v", result.Errors)
	}
}

func TestBatchService_Import_ValueTooLarge_Transactional(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	hugeValue := strings.Repeat("v", maxImportValueSize+1)
	csvData := "id,value\nkey1," + hugeValue + "\n"

	_, err := parseAndImportOf(t, svc).parseAndImport(context.Background(), []byte(csvData), ImportOptions{
		Format:        "csv",
		KeyField:      "id",
		ValueMode:     "field",
		ValueField:    "value",
		SkipHeader:    true,
		Transactional: true,
	})

	var ita *ImportTransactionAbortedError
	if !errors.As(err, &ita) {
		t.Fatalf("expected *ImportTransactionAbortedError, got %T: %v", err, err)
	}
	if !strings.Contains(ita.Message, "value exceeds maximum size") {
		t.Fatalf("expected message about oversized value, got %q", ita.Message)
	}
}

// ---------------------------------------------------------------------------
// Import - Archive expansion (zip/tar bomb) limits
// ---------------------------------------------------------------------------

// testExpandedBudget is the expansion budget the two archive-bomb tests run
// under. It is deliberately tiny.
//
// The decision under test is expansionLimitedReader refusing once the shared
// budget is exhausted, and that decision is identical at any budget -- but
// asserting it at the production 400 MB means parseTar/parseZip materialising
// 400 MB through io.ReadAll, whose growth doubling peaks higher still, all
// under -race. Measured on one box, these two tests alone: 6.9 GB peak RSS and
// 41.0s before, 0.6 GB and 1.3s after. That 6.9 GB is what OOM-killed
// pkg/service on CI three times in one day (go-qrdb#148, runs 6793 and 6862
// twice), each time taking main red and costing a re-run of a ~35 minute suite.
//
// The production default is NOT changed by this, and
// TestBatchService_ImportExpandedBudget_DefaultsToProductionLimit fails if it
// ever is.
const testExpandedBudget = 1 << 20 // 1 MB

// newBudgetedBatchService returns a BatchService whose archive expansion
// budget is testExpandedBudget rather than the production limit.
//
// It builds the struct directly rather than going through NewBatchService,
// because the budget is deliberately not settable through the exported
// constructor: nothing in production should be able to lower it.
func newBudgetedBatchService() *BatchService {
	return &BatchService{host: &mockHost{}, shardID: 1, expandedBudget: testExpandedBudget}
}

// The archive expansion budget resolves to the production limit unless a test
// overrides it.
//
// This is the guard on the seam introduced for the two tests below. The whole
// point of making the budget a field is that the shipped default is unchanged,
// and a claim like that is worth nothing unless something fails when it stops
// being true. It covers the exported constructor, the zero value (so a caller
// building BatchService directly is not silently unprotected) and a negative
// value (so the bound cannot be disabled by setting it to -1).
func TestBatchService_ImportExpandedBudget_DefaultsToProductionLimit(t *testing.T) {
	if got := NewBatchService(&mockHost{}, 1).(*BatchService).importExpandedBudget(); got != maxImportExpandedBytes {
		t.Errorf("NewBatchService budget = %d, want the production %d", got, maxImportExpandedBytes)
	}
	if got := (&BatchService{}).importExpandedBudget(); got != maxImportExpandedBytes {
		t.Errorf("zero-value BatchService budget = %d, want the production %d", got, maxImportExpandedBytes)
	}
	if got := (&BatchService{expandedBudget: -1}).importExpandedBudget(); got != maxImportExpandedBytes {
		t.Errorf("negative budget = %d, want the production %d", got, maxImportExpandedBytes)
	}
	if l := newImportExpansionLimiter(0); l.budget != maxImportExpandedBytes {
		t.Errorf("newImportExpansionLimiter(0) budget = %d, want the production %d", l.budget, maxImportExpandedBytes)
	}
}

// TestBatchService_Import_Zip_ExpandedTooLarge builds a highly-compressible
// zip member (all zero bytes) whose decompressed size exceeds the expansion
// budget in force, simulating a zip bomb. The bounded reader must stop
// extraction and return a typed error instead of allocating the full expanded
// content.
func TestBatchService_Import_Zip_ExpandedTooLarge(t *testing.T) {
	svc := newBudgetedBatchService()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fw, err := zw.CreateHeader(&zip.FileHeader{
		Name:   "bomb.csv",
		Method: zip.Deflate,
	})
	if err != nil {
		t.Fatalf("failed to create zip entry: %v", err)
	}
	// Write more than the expansion budget of highly-compressible bytes.
	chunk := bytes.Repeat([]byte{0}, 64<<10) // 64 KB of zeros per write.
	written := 0
	for written <= testExpandedBudget {
		if _, werr := fw.Write(chunk); werr != nil {
			t.Fatalf("failed writing zip content: %v", werr)
		}
		written += len(chunk)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("failed to close zip writer: %v", err)
	}

	_, err = parseAndImportOf(t, svc).parseAndImport(context.Background(), buf.Bytes(), ImportOptions{
		Format:    "archive",
		KeyField:  "id",
		ValueMode: "row",
	})

	var iae *ImportArchiveExpandedTooLargeError
	if !errors.As(err, &iae) {
		t.Fatalf("expected *ImportArchiveExpandedTooLargeError, got %T: %v", err, err)
	}
	if iae.Max != testExpandedBudget {
		t.Fatalf("expected Max=%d (the budget actually applied), got %d", testExpandedBudget, iae.Max)
	}
}

// TestBatchService_Import_TarGz_ExpandedTooLarge is the tar.gz analogue of
// TestBatchService_Import_Zip_ExpandedTooLarge.
func TestBatchService_Import_TarGz_ExpandedTooLarge(t *testing.T) {
	svc := newBudgetedBatchService()

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	size := int64(testExpandedBudget) + (64 << 10)
	hdr := &tar.Header{
		Name: "bomb.csv",
		Mode: 0600,
		Size: size,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("failed to write tar header: %v", err)
	}
	chunk := bytes.Repeat([]byte{0}, 64<<10)
	var written int64
	for written < size {
		n := int64(len(chunk))
		if written+n > size {
			n = size - written
		}
		if _, werr := tw.Write(chunk[:n]); werr != nil {
			t.Fatalf("failed writing tar content: %v", werr)
		}
		written += n
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("failed to close tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("failed to close gzip writer: %v", err)
	}

	_, err := parseAndImportOf(t, svc).parseAndImport(context.Background(), buf.Bytes(), ImportOptions{
		Format:    "archive",
		KeyField:  "id",
		ValueMode: "row",
	})

	var iae *ImportArchiveExpandedTooLargeError
	if !errors.As(err, &iae) {
		t.Fatalf("expected *ImportArchiveExpandedTooLargeError, got %T: %v", err, err)
	}
}

// TestBatchService_Import_Zip_TooManyEntries builds a zip archive with more
// entries than maxImportArchiveEntries, each of which is empty (0 bytes),
// so the expansion budget alone would not catch it.
func TestBatchService_Import_Zip_TooManyEntries(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < maxImportArchiveEntries+1; i++ {
		_, err := zw.Create("f" + strconv.Itoa(i) + ".txt")
		if err != nil {
			t.Fatalf("failed to create zip entry %d: %v", i, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("failed to close zip writer: %v", err)
	}

	_, err := parseAndImportOf(t, svc).parseAndImport(context.Background(), buf.Bytes(), ImportOptions{
		Format:    "archive",
		KeyField:  "id",
		ValueMode: "row",
	})

	var itme *ImportTooManyEntriesError
	if !errors.As(err, &itme) {
		t.Fatalf("expected *ImportTooManyEntriesError, got %T: %v", err, err)
	}
	if itme.Max != maxImportArchiveEntries {
		t.Fatalf("expected Max=%d, got %d", maxImportArchiveEntries, itme.Max)
	}
}

// ---------------------------------------------------------------------------
// Import - Proposal chunking / rejection
// ---------------------------------------------------------------------------

// TestBatchService_Import_NonTransactional_ChunksOversizedProposal builds a
// non-transactional import whose encoded operations exceed
// maxImportProposalBytes and verifies it is split across multiple
// SyncPropose calls (rather than rejected, and rather than sent as one
// oversized command) with every row ultimately succeeding.
func TestBatchService_Import_NonTransactional_ChunksOversizedProposal(t *testing.T) {
	var proposeCalls int
	var maxCmdLen int
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			proposeCalls++
			if len(cmd) > maxCmdLen {
				maxCmdLen = len(cmd)
			}
			return sm.Result{Value: 1}, nil
		},
	}
	svc := NewBatchService(host, 1)

	// Each row's value is ~60KB; enough rows to exceed the 8MB proposal
	// cap comfortably while staying well under maxImportRows.
	rowValue := strings.Repeat("v", 60_000)
	rowsNeeded := (maxImportProposalBytes / 60_000) * 3 // ~3x the single-chunk capacity.

	var sb strings.Builder
	sb.WriteString("id,value\n")
	for i := 0; i < rowsNeeded; i++ {
		sb.WriteString("key")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString(",")
		sb.WriteString(rowValue)
		sb.WriteString("\n")
	}

	result, err := parseAndImportOf(t, svc).parseAndImport(context.Background(), []byte(sb.String()), ImportOptions{
		Format:     "csv",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
		SkipHeader: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != rowsNeeded {
		t.Fatalf("expected Total=%d, got %d", rowsNeeded, result.Total)
	}
	if result.Succeeded != rowsNeeded {
		t.Fatalf("expected Succeeded=%d, got %d", rowsNeeded, result.Succeeded)
	}
	if result.Failed != 0 {
		t.Fatalf("expected Failed=0, got %d", result.Failed)
	}
	if proposeCalls < 2 {
		t.Fatalf("expected the import to be split across multiple proposals, got %d propose call(s)", proposeCalls)
	}
	if maxCmdLen > maxImportProposalBytes {
		t.Fatalf("expected every proposed command to be <= %d bytes, got %d", maxImportProposalBytes, maxCmdLen)
	}
}

// TestBatchService_Import_Transactional_RejectsOversizedProposal verifies
// that a transactional import whose encoded operations would exceed
// maxImportProposalBytes is rejected outright -- never proposed -- rather
// than silently split across multiple non-atomic proposals.
func TestBatchService_Import_Transactional_RejectsOversizedProposal(t *testing.T) {
	var proposeCalls int
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			proposeCalls++
			return sm.Result{Value: 1}, nil
		},
	}
	svc := NewBatchService(host, 1)

	rowValue := strings.Repeat("v", 60_000)
	rowsNeeded := (maxImportProposalBytes / 60_000) * 3

	var sb strings.Builder
	sb.WriteString("id,value\n")
	for i := 0; i < rowsNeeded; i++ {
		sb.WriteString("key")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString(",")
		sb.WriteString(rowValue)
		sb.WriteString("\n")
	}

	_, err := parseAndImportOf(t, svc).parseAndImport(context.Background(), []byte(sb.String()), ImportOptions{
		Format:        "csv",
		KeyField:      "id",
		ValueMode:     "field",
		ValueField:    "value",
		SkipHeader:    true,
		Transactional: true,
	})

	var ipte *ImportProposalTooLargeError
	if !errors.As(err, &ipte) {
		t.Fatalf("expected *ImportProposalTooLargeError, got %T: %v", err, err)
	}
	if ipte.Max != maxImportProposalBytes {
		t.Fatalf("expected Max=%d, got %d", maxImportProposalBytes, ipte.Max)
	}
	if proposeCalls != 0 {
		t.Fatalf("expected no propose calls for a rejected oversized transactional import, got %d", proposeCalls)
	}
}

// TestBatchService_Import_NonTransactional_ChunkFailureAborts verifies that
// when one chunk's SyncPropose call suffers an outcome-unknown fault, the
// import aborts immediately -- no later chunk is even attempted -- instead
// of recording the chunk as a per-row failure and continuing (go-qrdb#576's
// security review finding 4: that outcome is never provably "nothing
// stored", so it can never be downgraded to a clean per-row failure in
// either transactional mode). See
// TestBatchService_Import_NonTransactional_ChunkRefusalContinues for the
// complementary case -- a clean, provably-nothing-stored chunk REFUSAL --
// which still continues exactly as before.
func TestBatchService_Import_NonTransactional_ChunkFailureAborts(t *testing.T) {
	var proposeCalls int
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			proposeCalls++
			if proposeCalls == 1 {
				return sm.Result{}, errors.New("raft timeout")
			}
			return sm.Result{Value: 1}, nil
		},
	}
	svc := NewBatchService(host, 1)

	rowValue := strings.Repeat("v", 60_000)
	rowsNeeded := (maxImportProposalBytes / 60_000) * 3

	var sb strings.Builder
	sb.WriteString("id,value\n")
	for i := 0; i < rowsNeeded; i++ {
		sb.WriteString("key")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString(",")
		sb.WriteString(rowValue)
		sb.WriteString("\n")
	}

	result, err := parseAndImportOf(t, svc).parseAndImport(context.Background(), []byte(sb.String()), ImportOptions{
		Format:     "csv",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
		SkipHeader: true,
	})
	if err == nil {
		t.Fatal("expected an error for an outcome-unknown propose fault on the first chunk")
	}
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Errorf("error = %v, want errors.Is(err, ErrOutcomeUnknown)", err)
	}
	if result != nil {
		t.Errorf("result = %+v, want nil on an aborted import", result)
	}
	if proposeCalls != 1 {
		t.Fatalf("propose was called %d times; the import must abort after the first chunk's fault, never attempting the rest", proposeCalls)
	}
}

// TestBatchService_Import_NonTransactional_ChunkRefusalContinues is
// TestBatchService_Import_NonTransactional_ChunkFailureAborts's complementary
// case: a chunk that commits and is refused ON ITS MERITS (a result-level
// AppError, checkResultError's path) -- provably stored nothing, never
// ambiguous -- still marks that chunk's rows failed and continues to the
// next chunk, exactly as a non-transactional import always has.
func TestBatchService_Import_NonTransactional_ChunkRefusalContinues(t *testing.T) {
	var proposeCalls int
	host := &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) (sm.Result, error) {
			proposeCalls++
			if proposeCalls == 1 {
				return sm.Result{Data: protocol.EncodeAppError(protocol.AppErrorConflict, "refused")}, nil
			}
			return sm.Result{Value: 1}, nil
		},
	}
	svc := NewBatchService(host, 1)

	rowValue := strings.Repeat("v", 60_000)
	rowsNeeded := (maxImportProposalBytes / 60_000) * 3

	var sb strings.Builder
	sb.WriteString("id,value\n")
	for i := 0; i < rowsNeeded; i++ {
		sb.WriteString("key")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString(",")
		sb.WriteString(rowValue)
		sb.WriteString("\n")
	}

	result, err := parseAndImportOf(t, svc).parseAndImport(context.Background(), []byte(sb.String()), ImportOptions{
		Format:     "csv",
		KeyField:   "id",
		ValueMode:  "field",
		ValueField: "value",
		SkipHeader: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if proposeCalls < 2 {
		t.Fatalf("expected multiple propose calls, got %d", proposeCalls)
	}
	if result.Failed == 0 {
		t.Fatalf("expected at least one failed row from the refused chunk, got Failed=0")
	}
	if result.Succeeded == 0 {
		t.Fatalf("expected at least one succeeded row from the later, successful chunk, got Succeeded=0")
	}
	if result.Succeeded+result.Failed != rowsNeeded {
		t.Fatalf("expected Succeeded+Failed=%d, got %d", rowsNeeded, result.Succeeded+result.Failed)
	}
}
