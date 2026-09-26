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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// proposeToRaft used to discard its Result. That was harmless while a state
// machine reported an unimplemented operation by returning an Apply error —
// SyncPropose surfaced it, and the write plainly failed. go-qrdb#129 changed
// that: returning it tripped the shard's circuit breaker and cost the cluster
// the shard, so a refusal now travels in Result.Data instead.
//
// Which turns a discarded Result into a fail-open. The refusal arrives with a
// nil error, and a caller that never looks at Data is told its vector was
// stored when the state machine refused to store it. These tests cover both
// halves of reading it: the refusal that must be surfaced, and the result this
// code cannot read, which must not be mistaken for success.

// newRaftVectorServiceForResult builds a Raft-mode vector service whose
// SyncPropose returns exactly the given result with a nil error, so the test
// controls the one thing under test — what proposeToRaft does with the value
// it gets back.
func newRaftVectorServiceForResult(t *testing.T, result interface{}) VectorService {
	t.Helper()
	store := newVecMockStorage()
	store.listNamespacesFn = func(_ context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{}, nil
	}
	store.getNamespaceInfoFn = func(_ context.Context, _ string) (*vector.NamespaceInfo, error) {
		return nil, &KeyNotFoundError{Key: "namespace"}
	}
	store.createNamespaceFn = func(_ context.Context, _ *vector.NamespaceInfo) error { return nil }

	svc, err := NewVectorService(store, newVecTestLogger(),
		WithRaftNode(&mockRaftNode{result: result, resultSet: true}),
		WithNodeID("node-1"),
	)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	return svc
}

func createNamespaceThroughRaft(t *testing.T, svc VectorService) error {
	t.Helper()
	_, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "ns",
		Dimension: 8,
	})
	return err
}

// A state machine that refuses the command encodes the reason into
// Result.Data. The caller must be told, not congratulated.
func TestVectorProposeToRaft_SurfacesAnEncodedRefusal(t *testing.T) {
	refusal := (&statemachine.InvalidOpcodeError{Op: 0xFE}).Error()
	svc := newRaftVectorServiceForResult(t, sm.Result{Data: protocol.EncodeCapabilityError(refusal)})

	err := createNamespaceThroughRaft(t, svc)
	if err == nil {
		t.Fatal("the state machine REFUSED the command and the caller was told the vector namespace " +
			"was created; a refusal in Result.Data must not read as success (go-qrdb#129)")
	}
	if !strings.Contains(err.Error(), "254") {
		t.Errorf("error = %v; want it to carry the state machine's reason (opcode 254)", err)
	}
}

// The control. An ordinary applied result carries no encoded error, and must
// not be turned into a failure — otherwise the check above would "pass" by
// rejecting everything, and every Raft-mode vector write would break.
func TestVectorProposeToRaft_AcceptsAnAppliedResult(t *testing.T) {
	svc := newRaftVectorServiceForResult(t, sm.Result{Value: 1})

	if err := createNamespaceThroughRaft(t, svc); err != nil {
		t.Fatalf("an applied result was rejected: %v", err)
	}
}

// interfaces.RaftNode types its result as interface{}, so nothing statically
// prevents an implementation returning some other shape. Skipping the check in
// that case is the same fail-open in a subtler place: the outcome would go
// unverified, and whether it did would depend on which implementation happened
// to be wired in.
//
// Not being able to read an outcome is not evidence that the outcome was good,
// so each of these must be refused. *sm.Result is the one to watch — it is the
// natural mistake, it carries a perfectly readable refusal, and a plain type
// assertion silently declines to look at it.
func TestVectorProposeToRaft_RefusesAnUnreadableResult(t *testing.T) {
	refusal := sm.Result{Data: protocol.EncodeCapabilityError("statemachine: mvcc not configured")}

	for _, tc := range []struct {
		name   string
		result interface{}
	}{
		{"nil", nil},
		{"pointer to sm.Result carrying a refusal", &refusal},
		{"unrelated type", "applied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newRaftVectorServiceForResult(t, tc.result)

			err := createNamespaceThroughRaft(t, svc)
			if err == nil {
				t.Fatalf("SyncPropose returned %T, whose outcome this code cannot read, and the "+
					"caller was told the write succeeded", tc.result)
			}
		})
	}
}
