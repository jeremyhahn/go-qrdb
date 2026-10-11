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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A stored document is a flat object whose "id" key is the document's own
// identity, and an object whose only other key is a "fields" object is read as
// the legacy {"id", "fields"} wrapper. A document field named "id", or a lone
// object-valued "fields", would therefore be stored and read back as something
// else -- the "id" silently dropped, the "fields" object unwrapped into the
// document -- while the caller was told the write succeeded and charged for
// what it sent. Each such document is refused before anything is stored.

// reservedFieldCases are documents whose fields the storage layout cannot
// hold as sent.
var reservedFieldCases = map[string]map[string]interface{}{
	"id field":          {"id": "shadow", "k": "v"},
	"numeric id field":  {"id": 7.0},
	"lone fields map":   {"fields": map[string]interface{}{"a": 1.0}},
	"lone fields empty": {"fields": map[string]interface{}{}},
}

// TestInsertDocument_RefusesAFieldTheLayoutCannotHold covers every document
// verb that stores a body.
func TestInsertDocument_RefusesAFieldTheLayoutCannotHold(t *testing.T) {
	for name, doc := range reservedFieldCases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			kv := newResolvedKVSvc()
			svc := NewCollectionService(kv)
			createCollection(t, svc, "db", "c", IDStrategyIncrement, "")
			ins, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: map[string]interface{}{"k": "v"}})
			require.NoError(t, err)
			prefix := docPrefixForTest(t, svc, "db", "c")

			_, err = svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: doc})
			assertReservedField(t, err)
			_, err = svc.UpdateDocument(ctx, &UpdateDocumentRequest{Database: "db", Collection: "c", ID: ins.ID, Document: doc})
			assertReservedField(t, err)
			_, err = svc.UpsertDocument(ctx, &UpsertDocumentRequest{Database: "db", Collection: "c", ID: ins.ID + 1, Document: doc})
			assertReservedField(t, err)

			assert.Len(t, kv.listAll(prefix), 1, "a refused document stores nothing")
			got, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "db", Collection: "c", ID: ins.ID})
			require.NoError(t, err)
			assert.Equal(t, map[string]interface{}{"k": "v"}, got.Document, "a refused update changes nothing")
		})
	}
}

// TestInsertDocument_StoresWhatTheLayoutCanHold: "fields" beside other
// fields, or holding a non-object, round-trips unchanged, and so does "_id",
// which names the identity rather than a field.
func TestInsertDocument_StoresWhatTheLayoutCanHold(t *testing.T) {
	ctx := context.Background()
	svc := NewCollectionService(newResolvedKVSvc())
	createCollection(t, svc, "db", "c", IDStrategyIncrement, "")
	for _, doc := range []map[string]interface{}{
		{"fields": map[string]interface{}{"a": 1.0}, "other": "x"},
		{"fields": "text"},
		{"identity": "x", "ID": "upper"},
	} {
		ins, err := svc.InsertDocument(ctx, &InsertDocumentRequest{Database: "db", Collection: "c", Document: doc})
		require.NoError(t, err)
		got, err := svc.GetDocument(ctx, &GetDocumentRequest{Database: "db", Collection: "c", ID: ins.ID})
		require.NoError(t, err)
		assert.Equal(t, doc, got.Document)
	}
}

func assertReservedField(t *testing.T, err error) {
	t.Helper()
	var reserved *CollectionReservedFieldError
	require.ErrorAs(t, err, &reserved)
	assert.NotEmpty(t, reserved.Field)
	assert.Contains(t, reserved.Error(), reserved.Field)
}
