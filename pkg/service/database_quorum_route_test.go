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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// registryLockWindow is how long the test waits for a call that takes the
// database registry's lock. Under the routed submission the call is blocked
// until the submission returns, so the wait expires; a submission that
// bypassed the registry holds no lock, and the call returns at once.
const registryLockWindow = 100 * time.Millisecond

// TestDatabaseBarrierQuorumSubmit_UnsealRunsUnderTheRegistryLock pins the
// route of a database unseal quorum: DatabaseBarrierQuorumSubmit submits
// through DatabaseBarrierManager.SubmitHolderShare, so the unseal it
// completes runs under the database registry's lock -- ordered against a
// whole-node seal -- rather than against the barrier directly. The barrier's
// unseal observer, which runs inside the completing submission, asks for the
// registry's pending unseals, which takes that lock: it must not get it until
// the submission is done.
func TestDatabaseBarrierQuorumSubmit_UnsealRunsUnderTheRegistryLock(t *testing.T) {
	svc, _ := newHolderDatabaseService(t)
	ctx := dbAdminCtx("acme")
	holders := claimedDatabaseHolders(t, svc, ctx)
	for _, h := range holders {
		if _, err := submitDBSvc(t, svc, ctx, seal.OpSeal, h); err != nil {
			t.Fatal(err)
		}
	}
	_, barrier, err := svc.barrierMgr.Holders("vault")
	if err != nil {
		t.Fatal(err)
	}

	observed := false
	lockFree := false
	barrier.OnUnsealed(func(context.Context) {
		observed = true
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = svc.barrierMgr.PendingUnseals()
		}()
		select {
		case <-done:
			lockFree = true
		case <-time.After(registryLockWindow):
		}
	})

	if _, err := submitDBSvc(t, svc, ctx, seal.OpUnseal, holders[0]); err != nil {
		t.Fatal(err)
	}
	out, err := submitDBSvc(t, svc, ctx, seal.OpUnseal, holders[1])
	if err != nil || !out.Complete {
		t.Fatalf("unseal quorum = %+v, %v", out, err)
	}
	if !observed {
		t.Fatal("precondition: the unseal observer never ran")
	}
	if lockFree {
		t.Fatal("the unseal quorum ran outside the database registry's lock: it bypassed DatabaseBarrierManager.SubmitHolderShare")
	}
}
