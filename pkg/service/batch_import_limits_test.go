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
)

// ---------------------------------------------------------------------------
// Import - Input size limit
// ---------------------------------------------------------------------------

func TestBatchService_Import_InputTooLarge(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	oversized := bytes.Repeat([]byte("a"), maxImportInputBytes+1)

	_, err := svc.Import(context.Background(), bytes.NewReader(oversized), ImportOptions{
		Format:    "csv",
		KeyField:  "id",
		ValueMode: "row",
	})

	var iite *ImportInputTooLargeError
	if !errors.As(err, &iite) {
		t.Fatalf("expected *ImportInputTooLargeError, got %T: %v", err, err)
	}
	if iite.Max != maxImportInputBytes {
		t.Fatalf("expected Max=%d, got %d", maxImportInputBytes, iite.Max)
	}
}

func TestBatchService_Import_InputAtLimit_NotRejected(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	// A file that is exactly at the byte cap (not over it) must not be
	// rejected by the size gate; it should fail parsing normally instead
	// (this payload isn't valid CSV content for the configured key field,
	// but format detection/parsing errors are a distinct failure mode from
	// the size gate under test here).
	atLimit := bytes.Repeat([]byte("a"), maxImportInputBytes)

	_, err := svc.Import(context.Background(), bytes.NewReader(atLimit), ImportOptions{
		Format:    "csv",
		KeyField:  "id",
		ValueMode: "row",
	})

	var iite *ImportInputTooLargeError
	if errors.As(err, &iite) {
		t.Fatalf("did not expect *ImportInputTooLargeError for input at the exact limit")
	}
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

	_, err := svc.Import(context.Background(), strings.NewReader(sb.String()), ImportOptions{
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

	result, err := svc.Import(context.Background(), strings.NewReader(csvData), ImportOptions{
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

	_, err := svc.Import(context.Background(), strings.NewReader(csvData), ImportOptions{
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

	result, err := svc.Import(context.Background(), strings.NewReader(csvData), ImportOptions{
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

	_, err := svc.Import(context.Background(), strings.NewReader(csvData), ImportOptions{
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

// TestBatchService_Import_Zip_ExpandedTooLarge builds a highly-compressible
// zip member (all zero bytes) whose declared/actual decompressed size
// exceeds maxImportExpandedBytes, simulating a zip bomb. The bounded reader
// must stop extraction and return a typed error instead of allocating the
// full expanded content.
func TestBatchService_Import_Zip_ExpandedTooLarge(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

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
	chunk := bytes.Repeat([]byte{0}, 1<<20) // 1 MB of zeros per write.
	written := 0
	for written <= maxImportExpandedBytes {
		if _, werr := fw.Write(chunk); werr != nil {
			t.Fatalf("failed writing zip content: %v", werr)
		}
		written += len(chunk)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("failed to close zip writer: %v", err)
	}

	_, err = svc.Import(context.Background(), bytes.NewReader(buf.Bytes()), ImportOptions{
		Format:    "archive",
		KeyField:  "id",
		ValueMode: "row",
	})

	var iae *ImportArchiveExpandedTooLargeError
	if !errors.As(err, &iae) {
		t.Fatalf("expected *ImportArchiveExpandedTooLargeError, got %T: %v", err, err)
	}
	if iae.Max != maxImportExpandedBytes {
		t.Fatalf("expected Max=%d, got %d", maxImportExpandedBytes, iae.Max)
	}
}

// TestBatchService_Import_TarGz_ExpandedTooLarge is the tar.gz analogue of
// TestBatchService_Import_Zip_ExpandedTooLarge.
func TestBatchService_Import_TarGz_ExpandedTooLarge(t *testing.T) {
	svc := NewBatchService(&mockHost{}, 1)

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	size := int64(maxImportExpandedBytes) + (1 << 20)
	hdr := &tar.Header{
		Name: "bomb.csv",
		Mode: 0600,
		Size: size,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("failed to write tar header: %v", err)
	}
	chunk := bytes.Repeat([]byte{0}, 1<<20)
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

	_, err := svc.Import(context.Background(), bytes.NewReader(buf.Bytes()), ImportOptions{
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

	_, err := svc.Import(context.Background(), bytes.NewReader(buf.Bytes()), ImportOptions{
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

	result, err := svc.Import(context.Background(), strings.NewReader(sb.String()), ImportOptions{
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

	_, err := svc.Import(context.Background(), strings.NewReader(sb.String()), ImportOptions{
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

// TestBatchService_Import_NonTransactional_ChunkFailureContinues verifies
// that when one chunk's SyncPropose call fails, subsequent chunks are still
// attempted (matching the existing non-transactional "continue on error"
// semantics, now applied per chunk instead of per whole-import).
func TestBatchService_Import_NonTransactional_ChunkFailureContinues(t *testing.T) {
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

	result, err := svc.Import(context.Background(), strings.NewReader(sb.String()), ImportOptions{
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
		t.Fatalf("expected at least one failed row from the failing chunk, got Failed=0")
	}
	if result.Succeeded == 0 {
		t.Fatalf("expected at least one succeeded row from the later, successful chunk, got Succeeded=0")
	}
	if result.Succeeded+result.Failed != rowsNeeded {
		t.Fatalf("expected Succeeded+Failed=%d, got %d", rowsNeeded, result.Succeeded+result.Failed)
	}
}
