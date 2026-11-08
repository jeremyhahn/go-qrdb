// Copyright 2026 Jeremy Hahn
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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestEmbeddedTx_RangedGetOfTheConfinedKeyIsRefused sends a get of the
// confined cluster CA record with the range end "!" through the embedded
// transport. The interval [key, "!") overlaps no confined namespace, so a
// guard reading the range end as a range would pass it while an executor
// dropping the range end would read the confined key; the request must be
// refused with a typed error and reach no servicer.
func TestEmbeddedTx_RangedGetOfTheConfinedKeyIsRefused(t *testing.T) {
	reached := false
	tr := newExtTransport(t, WithTransactionService(&mockTxService{
		executeFn: func(context.Context, []service.Compare, []service.Op, []service.Op) (*service.TransactionResponse, error) {
			reached = true
			return &service.TransactionResponse{Succeeded: true}, nil
		},
		executeWithModeFn: func(context.Context, []service.Compare, []service.Op, []service.Op, writemode.Mode) (*service.TransactionResponse, error) {
			reached = true
			return &service.TransactionResponse{Succeeded: true}, nil
		},
	}))

	for name, req := range map[string]*transport.TransactionRequest{
		"success branch": {SuccessOps: []transport.Op{{Type: transport.OpGet, Key: []byte("__sys:cluster_ca/material"), RangeEnd: []byte("!")}}},
		"failure branch": {FailureOps: []transport.Op{{Type: transport.OpGet, Key: []byte("__sys:cluster_ca/material"), RangeEnd: []byte("!")}}},
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := tr.ExecuteTransaction(context.Background(), req)
			var rangedGet *service.TransactionGetRangeEndError
			if !errors.As(err, &rangedGet) {
				t.Fatalf("ExecuteTransaction = (%+v, %v), want a TransactionGetRangeEndError", resp, err)
			}
			if resp != nil {
				t.Fatalf("response = %+v, want none: nothing may be read", resp)
			}
			if reached {
				t.Fatal("the ranged get reached the transaction servicer")
			}
		})
	}
}
