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
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// wireBigDocumentID is above 2^53, where a JSON number read as a float64
// rounds to a neighbouring ID.
const wireBigDocumentID uint64 = 1<<53 + 1

// documentIDCalls are the verbs that decode a document ID from a tool result,
// each returning the IDs it decoded.
func documentIDCalls() map[string]struct {
	tool string
	call func(context.Context, *Transport) ([]uint64, error)
} {
	return map[string]struct {
		tool string
		call func(context.Context, *Transport) ([]uint64, error)
	}{
		"InsertDocument": {"qrdb_collection_insert", func(ctx context.Context, tr *Transport) ([]uint64, error) {
			r, err := tr.InsertDocument(ctx, transport.InsertDocumentRequest{Database: "app", Collection: "c", Document: map[string]any{"k": "v"}})
			if err != nil {
				return nil, err
			}
			return []uint64{r.ID}, nil
		}},
		"GetDocument": {"qrdb_collection_get", func(ctx context.Context, tr *Transport) ([]uint64, error) {
			r, err := tr.GetDocument(ctx, "app", "c", wireBigDocumentID)
			if err != nil {
				return nil, err
			}
			return []uint64{r.ID}, nil
		}},
		"ListDocuments": {"qrdb_collection_list_documents", func(ctx context.Context, tr *Transport) ([]uint64, error) {
			r, err := tr.ListDocuments(ctx, transport.ListDocumentsRequest{Database: "app", Collection: "c"})
			if err != nil {
				return nil, err
			}
			ids := make([]uint64, len(r.Documents))
			for i, d := range r.Documents {
				ids[i] = d.ID
			}
			return ids, nil
		}},
	}
}

// TestMCP_DocumentIDResponse_DecodesTheDecimalString pins that each verb reads
// the ID the server writes, a decimal string, exactly above 2^53.
func TestMCP_DocumentIDResponse_DecodesTheDecimalString(t *testing.T) {
	tr, cleanup := connectStubInProcess(t, map[string]string{
		"qrdb_collection_insert":         `{"id":"9007199254740993"}`,
		"qrdb_collection_get":            `{"id":"9007199254740993","document":{"k":"v"}}`,
		"qrdb_collection_list_documents": `{"documents":[{"id":"9007199254740993","document":{}},{"id":"3","document":{}}],"total_count":2}`,
	})
	defer cleanup()

	for name, c := range documentIDCalls() {
		ids, err := c.call(context.Background(), tr)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(ids) == 0 || ids[0] != wireBigDocumentID {
			t.Fatalf("%s decoded IDs %v, want %d first", name, ids, wireBigDocumentID)
		}
		if name == "ListDocuments" && (len(ids) != 2 || ids[1] != 3) {
			t.Fatalf("ListDocuments decoded IDs %v, want [%d 3]", ids, wireBigDocumentID)
		}
	}
}

// TestMCP_DocumentIDResponse_RefusesANumber pins that a JSON-number ID, the
// form that loses precision, is a decode error naming the verb rather than a
// document ID that may be another document's.
func TestMCP_DocumentIDResponse_RefusesANumber(t *testing.T) {
	tr, cleanup := connectStubInProcess(t, map[string]string{
		"qrdb_collection_insert":         `{"id":9007199254740993}`,
		"qrdb_collection_get":            `{"id":9007199254740993,"document":{}}`,
		"qrdb_collection_list_documents": `{"documents":[{"id":3,"document":{}}],"total_count":1}`,
	})
	defer cleanup()

	for name, c := range documentIDCalls() {
		ids, err := c.call(context.Background(), tr)
		var de *jsonDecodeError
		var re *transport.RequestError
		if !errors.As(err, &de) || !errors.As(err, &re) || re.Method != name {
			t.Fatalf("%s over a numeric ID = (%v, %v), want a decode error naming %s", name, ids, err, name)
		}
	}
}
