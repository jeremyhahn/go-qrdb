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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRESTBatch_SendsTheDocumentedRequest checks ExecuteBatch's verb, path
// and body, and that the answer decodes into the result.
func TestRESTBatch_SendsTheDocumentedRequest(t *testing.T) {
	assertSendsTheDocumentedRequest(t, []wireCase{
		{name: "ExecuteBatch", verb: http.MethodPost, path: "/batch",
			wantBody: batchWireRequest{Ops: []txWireOp{{Type: "put", Key: "k", Value: "v"}}},
			answer:   batchWireResponse{Responses: []txWireOpResponse{{Succeeded: true}}, Revision: 5},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.ExecuteBatch(ctx, []transport.Op{{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")}})
				if err == nil && (len(resp.Responses) != 1 || !resp.Responses[0].Succeeded || resp.Revision != 5) {
					return errors.New("batch answer not decoded, want one succeeded op at revision 5")
				}
				return err
			}},
	})
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

// TestRESTBatch_Closed checks that the batch RPCs on a closed transport
// refuse before the wire.
func TestRESTBatch_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(ctx context.Context, rt *Transport) error{
		"ExecuteBatch": func(ctx context.Context, rt *Transport) error {
			_, err := rt.ExecuteBatch(ctx, nil)
			return err
		},
		"ImportFile": func(ctx context.Context, rt *Transport) error {
			_, err := rt.ImportFile(ctx, "data.csv", strings.NewReader("k,v"), transport.ImportOptions{})
			return err
		},
	})
}
