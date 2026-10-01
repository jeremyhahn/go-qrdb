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

package statemachine

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// heldLease returns a lessor holding one granted lease, and its ID.
func heldLease(t *testing.T) (*lease.Lessor, int64) {
	t.Helper()
	lr := lease.NewLessor()
	l, err := lr.Grant(memory.NewStorage(), 60)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	return lr, l.ID
}

func TestLeaseRefusal_AcceptsNoLeaseAndHeldLeases(t *testing.T) {
	lr, id := heldLease(t)
	if _, refused := LeaseRefusal(lr, lease.NoLease, id); refused {
		t.Fatal("a write naming no lease and a held lease was refused")
	}
	if _, refused := LeaseRefusal(nil, lease.NoLease); refused {
		t.Fatal("a write naming no lease was refused on a state machine without a lessor")
	}
}

func TestLeaseRefusal_RefusesUnknownLeaseAsInvalid(t *testing.T) {
	lr, id := heldLease(t)
	res, refused := LeaseRefusal(lr, id, id+1)
	if !refused {
		t.Fatal("a write naming a lease the lessor does not hold was accepted")
	}
	if _, kind, ok := protocol.DecodeAppErrorKind(res.Data); !ok || kind != protocol.AppErrorInvalid {
		t.Fatalf("refusal kind = %v (decoded=%v), want AppErrorInvalid", kind, ok)
	}
}

func TestLeaseRefusal_NoLessorIsACapabilityRefusal(t *testing.T) {
	res, refused := LeaseRefusal(nil, 7)
	if !refused {
		t.Fatal("a write naming a lease was accepted by a state machine without a lessor")
	}
	if _, kind, ok := protocol.DecodeAppErrorKind(res.Data); !ok || kind != protocol.AppErrorCapability {
		t.Fatalf("refusal kind = %v (decoded=%v), want AppErrorCapability", kind, ok)
	}
}

func TestOpsLeaseRefusal_ChecksEveryPutAndIgnoresDeletes(t *testing.T) {
	lr, id := heldLease(t)
	ok := []protocol.DecodedOp{
		{Type: protocol.OpMVCCPut, Key: []byte("a"), LeaseID: id},
		{Type: protocol.OpMVCCPut, Key: []byte("b")},
		// A delete carries no lease to bind; its LeaseID field is ignored.
		{Type: protocol.OpMVCCDelete, Key: []byte("c"), LeaseID: id + 50},
	}
	if _, refused := OpsLeaseRefusal(lr, ok); refused {
		t.Fatal("an operation set naming only held leases was refused")
	}

	bad := append(ok, protocol.DecodedOp{Type: protocol.OpMVCCPut, Key: []byte("d"), LeaseID: id + 1})
	if _, refused := OpsLeaseRefusal(lr, bad); !refused {
		t.Fatal("an operation set whose last put names an unknown lease was accepted")
	}
}
