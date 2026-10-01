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

package backend

import (
	"context"
	stderrors "errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// This file is UNCONSTRAINED so its helper is a superset of every caller's
// constraint. It holds the go-qrdb#520 assertions for SimpleStateMachine.

// assertRefusedNotHalted asserts an Update refused an entry the way Raft can
// survive: nil error, and the refusal readable in the result payload as a
// capability error.
func assertRefusedNotHalted(t *testing.T, updateErr error, result sm.Result, wantSubstr string) {
	t.Helper()
	if updateErr != nil {
		t.Fatalf("Update returned %v; a refusal must not halt Apply (go-qrdb#520)", updateErr)
	}
	msg, kind, ok := protocol.DecodeAppErrorKind(result.Data)
	if !ok {
		t.Fatalf("result payload %q carries no encoded application error", result.Data)
	}
	if kind != protocol.AppErrorCapability {
		t.Errorf("refusal %q encoded WITHOUT the capability marker", msg)
	}
	if !strings.Contains(msg, wantSubstr) {
		t.Errorf("encoded refusal = %q, want it to contain %q", msg, wantSubstr)
	}
}

// TestUnsupportedOperationErrorDeclaresItsCapability keeps the declaration and
// the classifier honest at the type level.
func TestUnsupportedOperationErrorDeclaresItsCapability(t *testing.T) {
	capErr, ok := protocol.AsCapabilityError(error(&UnsupportedOperationError{Op: 0x2A}))
	if !ok {
		t.Fatal("UnsupportedOperationError is not recognised as an absent capability")
	}
	if got, want := capErr.Capability(), "opcode 0x2a"; got != want {
		t.Errorf("Capability() = %q, want %q", got, want)
	}
}

// TestSimpleStateMachineStillHaltsOnAFault is the half of the contract that
// must not be lost: an error that does not declare itself an absent capability
// halts Apply, so a replica that has actually diverged stops rather than
// serving wrong data.
func TestSimpleStateMachineStillHaltsOnAFault(t *testing.T) {
	fs := newFailStorage(memory.NewStorage())
	fs.failSet = true
	fs.setErr = stderrors.New("storage set failure")

	ssm := NewSimpleStateMachine(fs, nil)
	defer func() { _ = ssm.Close(context.Background()) }()

	put, err := kv.EncodePut([]byte("k"), []byte("v"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	results := make([]sm.Result, 1)
	updateErr := ssm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: put}}, results)
	if updateErr == nil {
		t.Fatal("Update returned nil for a storage fault; divergence must halt Apply")
	}
	if _, ok := protocol.AsCapabilityError(updateErr); ok {
		t.Fatalf("storage fault %v classified as an absent capability", updateErr)
	}
}
