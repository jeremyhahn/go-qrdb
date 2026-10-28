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
	"encoding/json"
	"strconv"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// bigDocumentID is above 2^53, where a float64 can no longer hold every
// integer: read as a JSON number it rounds to a neighbouring ID.
const bigDocumentID uint64 = 1<<53 + 1

// documentIDOnWire returns the raw JSON of the "id" member of obj, so a test
// can tell a string from a number, which a typed decode cannot.
func documentIDOnWire(t *testing.T, obj []byte) string {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(obj, &raw); err != nil {
		t.Fatalf("result %s is not a JSON object: %v", obj, err)
	}
	return string(raw["id"])
}

// TestCollectionDocumentIDs_TravelAsStrings pins that every collection tool
// returning a document ID writes it as a decimal string, exactly, for an ID
// above 2^53 and for a small one.
func TestCollectionDocumentIDs_TravelAsStrings(t *testing.T) {
	for _, id := range []uint64{bigDocumentID, 7} {
		want := strconv.Quote(strconv.FormatUint(id, 10))
		svc := newMockCollectionService()
		svc.insertDocumentFn = func(context.Context, *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
			return &service.InsertDocumentResponse{ID: id}, nil
		}
		svc.getDocumentFn = func(_ context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
			return &service.GetDocumentResponse{ID: req.ID, Document: map[string]interface{}{"n": "a"}}, nil
		}
		svc.updateDocumentFn = func(_ context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
			return &service.UpdateDocumentResponse{ID: req.ID}, nil
		}
		svc.listDocumentsFn = func(context.Context, *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
			return &service.ListDocumentsResponse{Documents: []service.GetDocumentResponse{{ID: id}}, TotalCount: 1}, nil
		}
		base := map[string]any{"database": "mydb", "collection": "users"}
		with := func(extra map[string]any) map[string]any {
			args := map[string]any{}
			for k, v := range base {
				args[k] = v
			}
			for k, v := range extra {
				args[k] = v
			}
			return args
		}
		idArg := strconv.FormatUint(id, 10)

		calls := map[string]func() (*mcp.CallToolResult, error){
			"qrdb_collection_insert": func() (*mcp.CallToolResult, error) {
				return handleCollectionInsert(svc)(context.Background(), buildCallToolRequest(t, "qrdb_collection_insert", with(map[string]any{"document": `{"n":"a"}`})))
			},
			"qrdb_collection_get": func() (*mcp.CallToolResult, error) {
				return handleCollectionGet(svc)(context.Background(), buildCallToolRequest(t, "qrdb_collection_get", with(map[string]any{"id": idArg})))
			},
			"qrdb_collection_update": func() (*mcp.CallToolResult, error) {
				return handleCollectionUpdate(svc)(context.Background(), buildCallToolRequest(t, "qrdb_collection_update", with(map[string]any{"id": idArg, "document": `{"n":"b"}`})))
			},
		}
		for tool, call := range calls {
			r, err := call()
			if err != nil {
				t.Fatalf("%s(%d): %v", tool, id, err)
			}
			if got := documentIDOnWire(t, []byte(resultText(t, r))); got != want {
				t.Fatalf("%s(%d) id on the wire = %s, want %s", tool, id, got, want)
			}
		}

		r, err := handleCollectionListDocuments(svc)(context.Background(), buildCallToolRequest(t, "qrdb_collection_list_documents", base))
		if err != nil {
			t.Fatalf("list_documents(%d): %v", id, err)
		}
		var page struct {
			Documents []json.RawMessage `json:"documents"`
		}
		if err := json.Unmarshal([]byte(resultText(t, r)), &page); err != nil || len(page.Documents) != 1 {
			t.Fatalf("list_documents(%d) page = %v, %v", id, page, err)
		}
		if got := documentIDOnWire(t, page.Documents[0]); got != want {
			t.Fatalf("list_documents(%d) id on the wire = %s, want %s", id, got, want)
		}
	}
}

// TestDocumentResultOf_CarriesIDAndDocument pins the conversion of one
// service document, at both ends of the ID range a float64 can and cannot
// hold.
func TestDocumentResultOf_CarriesIDAndDocument(t *testing.T) {
	for _, id := range []uint64{bigDocumentID, ^uint64(0)} {
		got := documentResultOf(&service.GetDocumentResponse{ID: id, Document: map[string]interface{}{"k": "v"}, Revision: "r1"})
		if got.ID != id || got.Document["k"] != "v" {
			t.Fatalf("documentResultOf(%d) = %+v", id, got)
		}
		data, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var back documentResult
		if err := json.Unmarshal(data, &back); err != nil || back.ID != id {
			t.Fatalf("%s decodes to %d, %v; want %d", data, back.ID, err, id)
		}
	}
}

// TestDocumentListResultOf_KeepsPageFields pins that a page keeps its token,
// count and order, and that an empty page is an empty array rather than
// null.
func TestDocumentListResultOf_KeepsPageFields(t *testing.T) {
	got := documentListResultOf(&service.ListDocumentsResponse{
		Documents:     []service.GetDocumentResponse{{ID: bigDocumentID}, {ID: 2}},
		NextPageToken: "next",
		TotalCount:    9,
	})
	if len(got.Documents) != 2 || got.Documents[0].ID != bigDocumentID || got.Documents[1].ID != 2 ||
		got.NextPageToken != "next" || got.TotalCount != 9 {
		t.Fatalf("documentListResultOf = %+v", got)
	}

	data, err := json.Marshal(documentListResultOf(&service.ListDocumentsResponse{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"documents":[],"total_count":0}`; string(data) != want {
		t.Fatalf("empty page = %s, want %s", data, want)
	}
}

// TestDocumentIDResult_RefusesANumber pins that the result's ID decodes only
// from a string, so a reader can never be handed a rounded number.
func TestDocumentIDResult_RefusesANumber(t *testing.T) {
	var r documentIDResult
	if err := json.Unmarshal([]byte(`{"id":9007199254740993}`), &r); err == nil {
		t.Fatalf("a JSON number decoded to %d", r.ID)
	}
	if err := json.Unmarshal([]byte(`{"id":"9007199254740993"}`), &r); err != nil || r.ID != bigDocumentID {
		t.Fatalf("decimal string decoded to %d, %v", r.ID, err)
	}
}
