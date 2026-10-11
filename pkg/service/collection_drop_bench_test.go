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
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
)

// BenchmarkDropCollection measures the teardown of a collection's documents
// against the in-memory KV, for sizes that expose its complexity: a teardown
// linear in the collection's size takes ten times as long for ten times the
// documents. It exists because the teardown once sorted its keys with an
// insertion sort, which made a large drop unable to finish inside the
// sweeper's per-database budget.
func BenchmarkDropCollection(b *testing.B) {
	for _, n := range []int{10_000, 100_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			ctx := context.Background()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				kv := newResolvedKVSvc()
				svc := NewCollectionService(kv)
				info, err := svc.CreateCollection(ctx, &CreateCollectionRequest{Database: "db", Name: "c", IDStrategy: IDStrategyIncrement})
				if err != nil {
					b.Fatal(err)
				}
				c := svc.resolveCodec(info)
				for id := uint64(1); id <= uint64(n); id++ {
					data, err := marshalDocument(&dao.DynamicEntity{ID: id, Fields: map[string]interface{}{"n": float64(id), "s": "0123456789"}}, c)
					if err != nil {
						b.Fatal(err)
					}
					if err := kv.Put(ctx, collectionDocumentKey(info, id), data); err != nil {
						b.Fatal(err)
					}
				}
				b.StartTimer()

				freed, err := svc.DropCollectionMetered(ctx, &DropCollectionRequest{Database: "db", Name: "c"})
				if err != nil {
					b.Fatal(err)
				}
				if freed == 0 {
					b.Fatal("the drop freed nothing")
				}
			}
		})
	}
}
