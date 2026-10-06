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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// sessionBatchService keeps real per-session state for the chunked import
// tools: chunks must arrive in sequence, commit reports the staged rows,
// and a finished session is gone.
type sessionBatchService struct {
	mockBatchService
	begun    service.ImportOptions
	sessions map[string][][]byte
}

func newSessionBatchService() *sessionBatchService {
	return &sessionBatchService{sessions: map[string][][]byte{}}
}

func (m *sessionBatchService) ImportBegin(_ context.Context, opts service.ImportOptions) (string, error) {
	if opts.Format != "csv" && opts.Format != "jsonl" {
		return "", &service.ImportInvalidFormatError{Format: opts.Format}
	}
	m.begun = opts
	m.sessions["sess-1"] = nil
	return "sess-1", nil
}

func (m *sessionBatchService) ImportAppend(_ context.Context, id string, seq uint64, chunk []byte) error {
	chunks, ok := m.sessions[id]
	if !ok {
		return &service.ImportSessionNotFoundError{SessionID: id}
	}
	if seq != uint64(len(chunks)) {
		return &service.ImportSequenceError{SessionID: id, Got: seq, Want: uint64(len(chunks))}
	}
	m.sessions[id] = append(chunks, chunk)
	return nil
}

func (m *sessionBatchService) ImportCommit(_ context.Context, id string) (*service.ImportResult, error) {
	chunks, ok := m.sessions[id]
	if !ok {
		return nil, &service.ImportSessionNotFoundError{SessionID: id}
	}
	delete(m.sessions, id)
	rows := bytes.Count(bytes.Join(chunks, nil), []byte("\n"))
	return &service.ImportResult{Total: rows, Succeeded: rows}, nil
}

func (m *sessionBatchService) ImportAbort(_ context.Context, id string) error {
	if _, ok := m.sessions[id]; !ok {
		return &service.ImportSessionNotFoundError{SessionID: id}
	}
	delete(m.sessions, id)
	return nil
}

func TestImportSessionTools_BeginAppendCommit(t *testing.T) {
	batch := newSessionBatchService()

	res, err := invokeTool(t, handleImportBegin(batch), "qrdb_batch_import_begin", map[string]any{
		"format": "csv", "filename": "rows.csv", "key_field": "id", "value_mode": "field", "value_field": "v",
		"transactional": true,
	})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	var begun struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(resultText(t, res)), &begun); err != nil || begun.SessionID != "sess-1" {
		t.Fatalf("begin result = %q err = %v", resultText(t, res), err)
	}
	// skip_header defaults to true when absent.
	want := service.ImportOptions{Format: "csv", Filename: "rows.csv", KeyField: "id", ValueMode: "field", ValueField: "v", SkipHeader: true, Transactional: true}
	if batch.begun != want {
		t.Fatalf("service saw %+v, want %+v", batch.begun, want)
	}

	for seq, chunk := range []string{"id,v\na,1\n", "b,2\n"} {
		res, err := invokeTool(t, handleImportAppend(batch), "qrdb_batch_import_append", map[string]any{
			"session_id": begun.SessionID, "seq": float64(seq), "chunk": b64(chunk),
		})
		if err != nil || res.IsError {
			t.Fatalf("append seq %d: res=%+v err=%v", seq, res, err)
		}
	}
	if got := string(bytes.Join(batch.sessions["sess-1"], nil)); got != "id,v\na,1\nb,2\n" {
		t.Fatalf("staged %q: chunks were not base64-decoded in order", got)
	}

	_, err = invokeTool(t, handleImportAppend(batch), "qrdb_batch_import_append", map[string]any{
		"session_id": begun.SessionID, "seq": float64(9), "chunk": b64("c,3\n"),
	})
	var seqErr *service.ImportSequenceError
	if !errors.As(err, &seqErr) || seqErr.Got != 9 || seqErr.Want != 2 {
		t.Fatalf("gap: err = %v, want ImportSequenceError{Got:9 Want:2}", err)
	}

	res, err = invokeTool(t, handleImportCommit(batch), "qrdb_batch_import_commit", map[string]any{"session_id": begun.SessionID})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	var result service.ImportResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &result); err != nil || result.Total != 3 || result.Succeeded != 3 {
		t.Fatalf("commit result = %+v err = %v", result, err)
	}

	_, err = invokeTool(t, handleImportCommit(batch), "qrdb_batch_import_commit", map[string]any{"session_id": begun.SessionID})
	var nf *service.ImportSessionNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("second commit: err = %v, want ImportSessionNotFoundError", err)
	}
	requireToolError(t, err, "qrdb_batch_import_commit", nf)
}

func TestImportSessionTools_Abort(t *testing.T) {
	batch := newSessionBatchService()
	if _, err := invokeTool(t, handleImportBegin(batch), "qrdb_batch_import_begin", map[string]any{
		"format": "jsonl", "key_field": "id", "value_mode": "document",
	}); err != nil {
		t.Fatalf("begin: %v", err)
	}
	res, err := invokeTool(t, handleImportAbort(batch), "qrdb_batch_import_abort", map[string]any{"session_id": "sess-1"})
	if err != nil || resultText(t, res) != "{}" {
		t.Fatalf("abort: res=%+v err=%v", res, err)
	}
	if len(batch.sessions) != 0 {
		t.Fatalf("abort left sessions %v", batch.sessions)
	}
	_, err = invokeTool(t, handleImportAbort(batch), "qrdb_batch_import_abort", map[string]any{"session_id": "sess-1"})
	var nf *service.ImportSessionNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("second abort: err = %v, want ImportSessionNotFoundError", err)
	}
}

// TestImportSessionTools_ArgumentRefusals proves every missing or malformed
// argument is answered as a tool-result error before the service is touched.
func TestImportSessionTools_ArgumentRefusals(t *testing.T) {
	batch := newSessionBatchService()
	batch.sessions["sess-1"] = nil
	cases := []struct {
		name string
		call func() (*mcp.CallToolResult, error)
	}{
		{"begin no format", func() (*mcp.CallToolResult, error) {
			return invokeTool(t, handleImportBegin(batch), "qrdb_batch_import_begin", map[string]any{"key_field": "id", "value_mode": "raw"})
		}},
		{"begin no key_field", func() (*mcp.CallToolResult, error) {
			return invokeTool(t, handleImportBegin(batch), "qrdb_batch_import_begin", map[string]any{"format": "csv", "value_mode": "raw"})
		}},
		{"begin no value_mode", func() (*mcp.CallToolResult, error) {
			return invokeTool(t, handleImportBegin(batch), "qrdb_batch_import_begin", map[string]any{"format": "csv", "key_field": "id"})
		}},
		{"append no session", func() (*mcp.CallToolResult, error) {
			return invokeTool(t, handleImportAppend(batch), "qrdb_batch_import_append", map[string]any{"seq": float64(0), "chunk": b64("x")})
		}},
		{"append no seq", func() (*mcp.CallToolResult, error) {
			return invokeTool(t, handleImportAppend(batch), "qrdb_batch_import_append", map[string]any{"session_id": "sess-1", "chunk": b64("x")})
		}},
		{"append no chunk", func() (*mcp.CallToolResult, error) {
			return invokeTool(t, handleImportAppend(batch), "qrdb_batch_import_append", map[string]any{"session_id": "sess-1", "seq": float64(0)})
		}},
		{"append bad base64", func() (*mcp.CallToolResult, error) {
			return invokeTool(t, handleImportAppend(batch), "qrdb_batch_import_append", map[string]any{"session_id": "sess-1", "seq": float64(0), "chunk": "!!not base64!!"})
		}},
		{"commit no session", func() (*mcp.CallToolResult, error) {
			return invokeTool(t, handleImportCommit(batch), "qrdb_batch_import_commit", nil)
		}},
		{"abort no session", func() (*mcp.CallToolResult, error) {
			return invokeTool(t, handleImportAbort(batch), "qrdb_batch_import_abort", nil)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tc.call()
			requireArgumentRefusal(t, res, err)
		})
	}
	if batch.begun != (service.ImportOptions{}) || len(batch.sessions["sess-1"]) != 0 {
		t.Fatalf("service reached despite refused arguments: begun=%+v staged=%v", batch.begun, batch.sessions["sess-1"])
	}
}

func TestImportSessionTools_BeginServiceErrorIsToolError(t *testing.T) {
	batch := newSessionBatchService()
	_, err := invokeTool(t, handleImportBegin(batch), "qrdb_batch_import_begin", map[string]any{
		"format": "xml", "key_field": "id", "value_mode": "raw",
	})
	var fe *service.ImportInvalidFormatError
	if !errors.As(err, &fe) || fe.Format != "xml" {
		t.Fatalf("err = %v, want ImportInvalidFormatError(xml)", err)
	}
	requireToolError(t, err, "qrdb_batch_import_begin", fe)
}
