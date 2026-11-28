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

package engine

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/vector"
)

// seedVectorReads creates namespace "ns" (dimension 2) holding vectors 3
// and 4 through the engine's own apply path.
func seedVectorReads(t *testing.T, eng Engine) {
	t.Helper()
	createNamespace(t, eng, "ns", 2)
	for _, id := range []uint64{3, 4} {
		result := applyVectorCmd(t, eng, mustEncodeVector(protocol.EncodeVectorUpsert("ns", 42, protocol.VectorUpsertParams{
			HasID: true, ID: id, Embedding: []float32{float32(id), 1}, Metadata: map[string]string{"k": "v"},
		})))
		if _, refused := protocol.DecodeAppError(result.Data); refused {
			t.Fatalf("upsert %d refused: %+v", id, result)
		}
	}
}

func lookupVectorQuery(t *testing.T, eng Engine, q interface{}) interface{} {
	t.Helper()
	got, err := eng.Lookup(context.Background(), q)
	if err != nil {
		t.Fatalf("Lookup(%T): %v", q, err)
	}
	return got
}

func TestVectorReads_NamespaceQueriesAnswerFromTheEngineStore(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, _ storage.Storage) {
		seedVectorReads(t, eng)

		a, ok := lookupVectorQuery(t, eng, service.VectorNamespaceQuery{Namespace: "ns"}).(*service.VectorNamespaceAnswer)
		if !ok || a == nil {
			t.Fatalf("namespace answer is not a *VectorNamespaceAnswer")
		}
		if a.Namespace != "ns" || a.Count != 2 || a.Record.Dimension != 2 || a.Record.NextID != 5 {
			t.Fatalf("namespace answer = %+v, want ns with 2 vectors, dimension 2, NextID 5", a)
		}

		list, ok := lookupVectorQuery(t, eng, service.VectorNamespaceListQuery{}).([]service.VectorNamespaceAnswer)
		if !ok || len(list) != 1 || list[0].Namespace != "ns" || list[0].Count != 2 {
			t.Fatalf("namespace list = %+v, want the one namespace ns", list)
		}

		if got := lookupVectorQuery(t, eng, service.VectorNamespaceQuery{Namespace: "absent"}); got != nil {
			t.Fatalf("absent namespace answered %#v, want untyped nil", got)
		}
	})
}

func TestVectorReads_GetAndScanAnswerFromTheEngineStore(t *testing.T) {
	forEachVectorEngine(t, func(t *testing.T, eng Engine, _ storage.Storage) {
		seedVectorReads(t, eng)

		rec, ok := lookupVectorQuery(t, eng, service.VectorGetQuery{Namespace: "ns", ID: 3}).(*vector.VectorRecord)
		if !ok || rec == nil || len(rec.Embedding) != 2 || rec.Embedding[0] != 3 || rec.Metadata["k"] != "v" || rec.CreatedAtUnixNano != 42 {
			t.Fatalf("get answer = %#v, want vector 3", rec)
		}
		if got := lookupVectorQuery(t, eng, service.VectorGetQuery{Namespace: "ns", ID: 9}); got != nil {
			t.Fatalf("absent ID answered %#v, want untyped nil", got)
		}
		if got := lookupVectorQuery(t, eng, service.VectorGetQuery{Namespace: "absent", ID: 3}); got != nil {
			t.Fatalf("absent namespace answered %#v, want untyped nil", got)
		}

		all, _ := lookupVectorQuery(t, eng, service.VectorScanQuery{Namespace: "ns"}).([]service.VectorScanItem)
		if len(all) != 2 || all[0].ID != 3 || all[1].ID != 4 || all[1].Record.Embedding[0] != 4 {
			t.Fatalf("full scan = %+v, want IDs 3, 4", all)
		}
		from, _ := lookupVectorQuery(t, eng, service.VectorScanQuery{Namespace: "ns", FromID: 4}).([]service.VectorScanItem)
		if len(from) != 1 || from[0].ID != 4 {
			t.Fatalf("scan from 4 = %+v, want ID 4", from)
		}
		limited, _ := lookupVectorQuery(t, eng, service.VectorScanQuery{Namespace: "ns", Limit: 1}).([]service.VectorScanItem)
		if len(limited) != 1 || limited[0].ID != 3 {
			t.Fatalf("scan limit 1 = %+v, want ID 3", limited)
		}
		if got, _ := lookupVectorQuery(t, eng, service.VectorScanQuery{Namespace: "absent"}).([]service.VectorScanItem); len(got) != 0 {
			t.Fatalf("absent namespace scan = %+v, want empty", got)
		}
	})
}
