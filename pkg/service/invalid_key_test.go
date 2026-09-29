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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// neverProposingHost fails the test if anything is proposed: an invalid key must be
// refused before it reaches consensus.
func neverProposingHost(t *testing.T) *mockHost {
	t.Helper()
	fail := func() { t.Error("an empty-key request was proposed; it must be refused first") }
	return &mockHost{
		syncProposeFn: func(context.Context, uint64, []byte) (sm.Result, error) {
			fail()
			return sm.Result{}, nil
		},
		proposeWithModeFn: func(context.Context, uint64, []byte, writemode.Mode) (sm.Result, error) {
			fail()
			return sm.Result{}, nil
		},
	}
}

func assertInvalidKey(t *testing.T, err error) {
	t.Helper()
	var ik *InvalidKeyError
	var ere *EncodeRequestError
	if !errors.As(err, &ik) || !errors.As(err, &ere) {
		t.Fatalf("err = %v (%T); want an InvalidKeyError inside an EncodeRequestError", err, err)
	}
	if ik.Error() == "" {
		t.Error("InvalidKeyError has an empty message")
	}
	if !errors.Is(err, ErrNotStored) {
		t.Error("a request refused before proposal must report ErrNotStored")
	}
}

// TestTransactionService_RefusesEmptyKeysBeforeProposing is the proposer half
// of go-qrdb#136's review finding B1: every state machine refuses an empty
// key at apply, and the transaction service refuses it without a round through
// consensus — in a compare, in a success op and in a failure op.
func TestTransactionService_RefusesEmptyKeysBeforeProposing(t *testing.T) {
	ok := Op{Type: OpPut, Key: []byte("k"), Value: []byte("v")}
	bad := Op{Type: OpPut, Value: []byte("v")}
	cases := map[string]struct {
		compares         []Compare
		success, failure []Op
	}{
		"compare":    {compares: []Compare{{Target: CompareVersion}}, success: []Op{ok}},
		"success op": {success: []Op{ok, bad}},
		"failure op": {success: []Op{ok}, failure: []Op{bad}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc := NewTransactionService(neverProposingHost(t), 1)
			_, err := svc.Execute(context.Background(), tc.compares, tc.success, tc.failure)
			assertInvalidKey(t, err)
			_, err = svc.ExecuteWithMode(context.Background(), tc.compares, tc.success, tc.failure, writemode.Sync)
			assertInvalidKey(t, err)
		})
	}
}

// TestBatchService_RefusesEmptyKeysBeforeProposing is the batch service's
// half: a put or delete naming an empty key refuses the whole batch unproposed.
func TestBatchService_RefusesEmptyKeysBeforeProposing(t *testing.T) {
	for name, bad := range map[string]Op{
		"put":    {Type: OpPut, Value: []byte("v")},
		"delete": {Type: OpDelete},
	} {
		t.Run(name, func(t *testing.T) {
			svc := NewBatchService(neverProposingHost(t), 1)
			ops := []Op{{Type: OpPut, Key: []byte("k"), Value: []byte("v")}, bad}
			_, err := svc.Execute(context.Background(), ops)
			assertInvalidKey(t, err)
			_, err = svc.ExecuteWithMode(context.Background(), ops, writemode.Sync)
			assertInvalidKey(t, err)
		})
	}
}
