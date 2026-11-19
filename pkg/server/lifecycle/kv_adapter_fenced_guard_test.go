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

//go:build !integration

package lifecycle

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// guardedBatchWriter is the fenced policy write both fenced tenant stores
// carry (tenant.fencedSwap).
type guardedBatchWriter interface {
	PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error)
}

// TestFencedStores_PutAllIfAllUnchanged proves both fenced tenant stores
// commit a guarded batch while its guard holds and refuse it, writing
// nothing, once the guard's key holds another value.
func TestFencedStores_PutAllIfAllUnchanged(t *testing.T) {
	for name := range fencedCreators(newEvaluatingTxService()) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			tx := newEvaluatingTxService()
			fc := fencedCreators(tx)[name]
			if created, err := fc.PutIfAllAbsent(ctx, "owner", []byte("A")); err != nil || !created {
				t.Fatalf("PutIfAllAbsent(owner) = %v, %v; want true, nil", created, err)
			}
			multi, ok := fc.(guardedBatchWriter)
			if !ok {
				t.Fatalf("%T has no PutAllIfAllUnchanged", fc)
			}
			applied, err := multi.PutAllIfAllUnchanged(ctx,
				[]kvstore.Guard{{Key: "owner", Value: []byte("A")}},
				[]kvstore.KV{{Key: "rec/1", Value: []byte("one")}})
			if err != nil || !applied {
				t.Fatalf("PutAllIfAllUnchanged(held) = %v, %v; want true, nil", applied, err)
			}
			if got := tx.data["rec/1"]; got == nil || string(got.Value) != "one" {
				t.Fatalf("the guarded write was not applied: %+v", got)
			}
			applied, err = multi.PutAllIfAllUnchanged(ctx,
				[]kvstore.Guard{{Key: "owner", Value: []byte("B")}},
				[]kvstore.KV{{Key: "rec/2", Value: []byte("two")}})
			if err != nil || applied {
				t.Fatalf("PutAllIfAllUnchanged(lost) = %v, %v; want false, nil", applied, err)
			}
			if got := tx.data["rec/2"]; got != nil {
				t.Fatalf("a refused guarded batch wrote rec/2: %+v", got)
			}
		})
	}
}
