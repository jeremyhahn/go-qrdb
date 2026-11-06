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

package embedded

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// mvccCall is one MVCC or transaction operation.
type mvccCall struct {
	name string
	call func(context.Context, *Transport) (any, error)
}

func mvccCalls() []mvccCall {
	return []mvccCall{
		{"PutWithLease", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.PutWithLease(ctx, "k", []byte("v"), 1)
		}},
		{"GetRevision", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.GetRevision(ctx, "k")
		}},
		{"GetAtRevision", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.GetAtRevision(ctx, "k", 1)
		}},
		{"Range", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.Range(ctx, []byte("a"), []byte("z"), transport.RangeOptions{})
		}},
		{"Compact", func(ctx context.Context, tr *Transport) (any, error) {
			return nil, tr.Compact(ctx, 1)
		}},
		{"CurrentRevision", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.CurrentRevision(ctx)
		}},
		{"ExecuteTransaction", func(ctx context.Context, tr *Transport) (any, error) {
			return tr.ExecuteTransaction(ctx, &transport.TransactionRequest{})
		}},
	}
}

// noAnswer reports whether got is the zero answer a refused call returns.
func noAnswer(got any) bool {
	v := reflect.ValueOf(got)
	return !v.IsValid() || v.IsZero()
}

// TestEmbeddedMVCC_NilService checks that every MVCC and transaction
// operation on a transport built without the MVCC service refuses with the
// typed error and no answer.
func TestEmbeddedMVCC_NilService(t *testing.T) {
	tr := newExtTransport(t)
	for _, tc := range mvccCalls() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call(context.Background(), tr)
			var ne *NilServiceError
			if !errors.As(err, &ne) {
				t.Fatalf("%s error = %T %v, want NilServiceError", tc.name, err, err)
			}
			if !noAnswer(got) {
				t.Errorf("%s answer = %+v, want none", tc.name, got)
			}
		})
	}
}

// TestEmbeddedMVCC_Closed checks that a closed transport refuses every MVCC
// and transaction operation with ClosedError and no answer.
func TestEmbeddedMVCC_Closed(t *testing.T) {
	tr := closedExtTransport(t)
	for _, tc := range mvccCalls() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call(context.Background(), tr)
			var ce *transport.ClosedError
			if !errors.As(err, &ce) {
				t.Fatalf("%s error = %T %v, want ClosedError", tc.name, err, err)
			}
			if !noAnswer(got) {
				t.Errorf("%s answer = %+v, want none", tc.name, got)
			}
		})
	}
}
