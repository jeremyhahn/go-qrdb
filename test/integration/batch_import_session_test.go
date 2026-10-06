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

//go:build integration

// batch_import_session_test.go exercises the chunked import session REST
// API (pkg/api/rest/handlers_batch_import_session.go) end to end against a
// real, freshly-spawned qrdbd node: ImportBegin/ImportAppend/ImportCommit/
// ImportAbort over raw HTTPS, the same lifecycle `qrdb batch import` drives
// internally once the CLI moves to it (a later phase). This is the first
// transport wired to the session API; the other transports, the SDKs, and
// the CLI itself follow in later phases -- see docs/transactions/.
package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// importBeginResp mirrors pkg/api/rest's batchImportBeginResponse.
type importBeginResp struct {
	SessionID string `json:"session_id"`
}

// importCommitResp mirrors pkg/api/rest's batchImportResponse.
type importCommitResp struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Errors    []struct {
		Row     int    `json:"row"`
		Key     string `json:"key"`
		Message string `json:"message"`
	} `json:"errors"`
}

// importSessionBaseURL returns the REST base URL for node's chunked import
// session endpoints.
func importSessionBaseURL(node *multiProtoNode) string {
	return fmt.Sprintf("https://127.0.0.1:%d/batch/import", node.restPort)
}

// importBegin starts a session and returns its ID.
func importBegin(t *testing.T, client *http.Client, node *multiProtoNode, body string) string {
	t.Helper()
	resp, err := client.Post(importSessionBaseURL(node)+"/begin", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ImportBegin status = %d, body = %s", resp.StatusCode, data)
	}
	var out importBeginResp
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("ImportBegin decode: %v (body %s)", err, data)
	}
	if out.SessionID == "" {
		t.Fatal("ImportBegin returned an empty session_id")
	}
	return out.SessionID
}

// importAppend stages chunk as sequence seq of sessionID, returning the
// HTTP status so a caller can assert on refusal cases too.
func importAppend(t *testing.T, client *http.Client, node *multiProtoNode, sessionID string, seq int, chunk []byte) (int, []byte) {
	t.Helper()
	url := fmt.Sprintf("%s/%s/append?seq=%d", importSessionBaseURL(node), sessionID, seq)
	resp, err := client.Post(url, "application/octet-stream", bytes.NewReader(chunk))
	if err != nil {
		t.Fatalf("ImportAppend(seq=%d): %v", seq, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// importCommit commits sessionID, returning the HTTP status and (on
// success) the decoded result.
func importCommit(t *testing.T, client *http.Client, node *multiProtoNode, sessionID string) (int, *importCommitResp) {
	t.Helper()
	url := fmt.Sprintf("%s/%s/commit", importSessionBaseURL(node), sessionID)
	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		t.Fatalf("ImportCommit: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	var out importCommitResp
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("ImportCommit decode: %v (body %s)", err, data)
	}
	return resp.StatusCode, &out
}

// importAbort aborts sessionID, returning the HTTP status.
func importAbort(t *testing.T, client *http.Client, node *multiProtoNode, sessionID string) int {
	t.Helper()
	url := fmt.Sprintf("%s/%s/abort", importSessionBaseURL(node), sessionID)
	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		t.Fatalf("ImportAbort: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode
}

// kvFound reports whether key exists, read over REST. The import session
// tests only need presence, not the (base64-encoded, JSON-row-serialized)
// value itself.
func kvFound(t *testing.T, client *http.Client, node *multiProtoNode, key string) bool {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", node.restPort, key)
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("kv get %s: %v", key, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		return true
	case http.StatusNotFound:
		return false
	default:
		t.Fatalf("kv get %s: unexpected status %d", key, resp.StatusCode)
		return false
	}
}

// TestBatchImportSession_REST_RoundTrip drives the full chunked import
// session lifecycle over REST: Begin, two Append calls, Commit, and proves
// the imported keys are readable afterward.
func TestBatchImportSession_REST_RoundTrip(t *testing.T) {
	node := startMultiProtocolNode(t)
	client := mtlsHTTPClientFromFiles(t, node.ca, node.clientCert, node.clientKey, 20*time.Second)

	sessionID := importBegin(t, client, node, `{"format":"csv","key_field":"id","value_mode":"row","skip_header":true}`)

	if status, body := importAppend(t, client, node, sessionID, 0, []byte("id,name\n")); status != http.StatusNoContent {
		t.Fatalf("ImportAppend(seq=0) status = %d, body = %s", status, body)
	}
	if status, body := importAppend(t, client, node, sessionID, 1, []byte("sess-key-1,Alice\nsess-key-2,Bob\n")); status != http.StatusNoContent {
		t.Fatalf("ImportAppend(seq=1) status = %d, body = %s", status, body)
	}

	status, result := importCommit(t, client, node, sessionID)
	if status != http.StatusOK {
		t.Fatalf("ImportCommit status = %d", status)
	}
	if result.Total != 2 || result.Succeeded != 2 || result.Failed != 0 {
		t.Fatalf("ImportCommit result = %+v, want Total=2 Succeeded=2 Failed=0", result)
	}

	for _, key := range []string{"sess-key-1", "sess-key-2"} {
		if !kvFound(t, client, node, key) {
			t.Errorf("key %q was not imported", key)
		}
	}

	// The session is released on a successful commit: committing again
	// must report the session gone.
	status, _ = importCommit(t, client, node, sessionID)
	if status != http.StatusNotFound {
		t.Errorf("second ImportCommit status = %d, want 404", status)
	}
}

// TestBatchImportSession_REST_IdempotentReplay proves a retried
// ImportAppend of the same sequence with identical bytes succeeds as a
// no-op, the behavior a client retries after a dropped response relies on.
func TestBatchImportSession_REST_IdempotentReplay(t *testing.T) {
	node := startMultiProtocolNode(t)
	client := mtlsHTTPClientFromFiles(t, node.ca, node.clientCert, node.clientKey, 20*time.Second)

	sessionID := importBegin(t, client, node, `{"format":"csv","key_field":"id","value_mode":"row","skip_header":true}`)

	chunk := []byte("id,name\nsess-key-3,Carol\n")
	if status, body := importAppend(t, client, node, sessionID, 0, chunk); status != http.StatusNoContent {
		t.Fatalf("first ImportAppend status = %d, body = %s", status, body)
	}
	if status, body := importAppend(t, client, node, sessionID, 0, chunk); status != http.StatusNoContent {
		t.Fatalf("replay ImportAppend status = %d, body = %s", status, body)
	}

	status, result := importCommit(t, client, node, sessionID)
	if status != http.StatusOK {
		t.Fatalf("ImportCommit status = %d", status)
	}
	if result.Total != 1 {
		t.Errorf("ImportCommit Total = %d, want 1 (the replay must not have staged a second copy)", result.Total)
	}
}

// TestBatchImportSession_REST_SequenceGapRefused proves a gapped sequence
// is refused before authentication's worth of work is wasted staging it.
func TestBatchImportSession_REST_SequenceGapRefused(t *testing.T) {
	node := startMultiProtocolNode(t)
	client := mtlsHTTPClientFromFiles(t, node.ca, node.clientCert, node.clientKey, 20*time.Second)

	sessionID := importBegin(t, client, node, `{"format":"csv","key_field":"id","value_mode":"row","skip_header":true}`)

	// seq 1 with nothing appended yet (want 0) is a gap.
	status, body := importAppend(t, client, node, sessionID, 1, []byte("x"))
	if status != http.StatusBadRequest {
		t.Fatalf("ImportAppend(seq=1) status = %d, body = %s, want 400", status, body)
	}
}

// TestBatchImportSession_REST_Abort proves Abort discards a session: a
// subsequent commit reports the session gone, and nothing it staged was
// ever imported.
func TestBatchImportSession_REST_Abort(t *testing.T) {
	node := startMultiProtocolNode(t)
	client := mtlsHTTPClientFromFiles(t, node.ca, node.clientCert, node.clientKey, 20*time.Second)

	sessionID := importBegin(t, client, node, `{"format":"csv","key_field":"id","value_mode":"row","skip_header":true}`)
	if status, body := importAppend(t, client, node, sessionID, 0, []byte("id,name\nsess-key-abort,Zed\n")); status != http.StatusNoContent {
		t.Fatalf("ImportAppend status = %d, body = %s", status, body)
	}

	if status := importAbort(t, client, node, sessionID); status != http.StatusNoContent {
		t.Fatalf("ImportAbort status = %d, want 204", status)
	}

	if status, _ := importCommit(t, client, node, sessionID); status != http.StatusNotFound {
		t.Errorf("ImportCommit after abort status = %d, want 404", status)
	}
	if kvFound(t, client, node, "sess-key-abort") {
		t.Error("aborted session's chunk was imported anyway")
	}
}
