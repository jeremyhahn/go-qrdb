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
	stderrors "errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

func validMoveRequest() *MovePrefixRequest {
	return &MovePrefixRequest{
		Src: "t/db/s/1/", Dst: "t/db/p/", ExpectedKeys: 3,
		Ops: []Op{
			{Type: OpPut, Key: []byte("t/db/meta"), Value: []byte("m2")},
			{Type: OpDelete, Key: []byte("t/db/p/9")},
		},
		Compares: []Compare{
			{Key: []byte("t/db/p/1"), Target: CompareModRevision, Result: CompareEqual, ModRevision: 0},
			{Key: []byte("t/db/p/2"), Target: CompareModRevision, Result: CompareLess, ModRevision: 6},
		},
	}
}

func TestMVCCKVService_MovePrefix_RefusesInvalidBeforeProposing(t *testing.T) {
	cases := map[string]func(r *MovePrefixRequest){
		"empty src":         func(r *MovePrefixRequest) { r.Src = "" },
		"empty dst":         func(r *MovePrefixRequest) { r.Dst = "" },
		"src under dst":     func(r *MovePrefixRequest) { r.Dst = "t/db/s/" },
		"dst under src":     func(r *MovePrefixRequest) { r.Dst = "t/db/s/1/x/" },
		"expected zero":     func(r *MovePrefixRequest) { r.ExpectedKeys = 0 },
		"range op":          func(r *MovePrefixRequest) { r.Ops = []Op{{Type: OpRange, Key: []byte("a")}} },
		"ranged delete":     func(r *MovePrefixRequest) { r.Ops = []Op{{Type: OpDelete, Key: []byte("a"), RangeEnd: []byte("b")}} },
		"op with empty key": func(r *MovePrefixRequest) { r.Ops = []Op{{Type: OpPut}} },
		"compare with empty key": func(r *MovePrefixRequest) {
			r.Compares = []Compare{{Target: CompareModRevision, Result: CompareEqual}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			inner, cmds := recordingHost(sm.Result{Value: 7})
			host := &gatedHost{mockHost: inner, enabled: map[uint64]bool{1: true}}
			req := validMoveRequest()
			mutate(req)
			_, err := NewMVCCKVService(host, 1).(*MVCCKVService).MovePrefix(context.Background(), req)
			var invalid *PrefixMoveInvalidError
			if !stderrors.As(err, &invalid) {
				t.Fatalf("MovePrefix = %v, want *PrefixMoveInvalidError", err)
			}
			if len(*cmds) != 0 || len(host.asked) != 0 {
				t.Fatalf("a refused move proposed %d commands and asked the gate %v", len(*cmds), host.asked)
			}
		})
	}
	if _, err := NewMVCCKVService(&mockHost{}, 1).(*MVCCKVService).MovePrefix(context.Background(), nil); err == nil {
		t.Fatal("a nil request was accepted")
	}
}

func TestMVCCKVService_MovePrefix_RefusedUntilTheGateIsActive(t *testing.T) {
	inner, cmds := recordingHost(sm.Result{Value: 7})
	host := &gatedHost{mockHost: inner, enabled: map[uint64]bool{1: false}}
	_, err := NewMVCCKVService(host, 1).(*MVCCKVService).MovePrefix(context.Background(), validMoveRequest())
	var notActive *feature.NotActiveError
	if !stderrors.As(err, &notActive) || notActive.Feature != featuregate.PrefixMove || notActive.ShardID != 1 {
		t.Fatalf("MovePrefix on an inactive gate = %v, want a prefix-move NotActiveError for shard 1", err)
	}
	if len(*cmds) != 0 {
		t.Fatalf("an inactive gate still proposed %d commands", len(*cmds))
	}
}

func TestMVCCKVService_MovePrefix_ProposesTheMove(t *testing.T) {
	inner, cmds := recordingHost(sm.Result{Value: 7})
	host := &gatedHost{mockHost: inner, enabled: map[uint64]bool{1: true}}
	rev, err := NewMVCCKVService(host, 1).(*MVCCKVService).MovePrefix(context.Background(), validMoveRequest())
	if err != nil || rev != 7 {
		t.Fatalf("MovePrefix = (%d, %v), want (7, nil)", rev, err)
	}
	if len(*cmds) != 1 || (*cmds)[0][0] != protocol.OpMVCCMovePrefix {
		t.Fatalf("proposed %x, want one OpMVCCMovePrefix", *cmds)
	}
	src, dst, expected, ops, cmps, err := protocol.DecodeMVCCMovePrefix((*cmds)[0][1:])
	if err != nil || string(src) != "t/db/s/1/" || string(dst) != "t/db/p/" || expected != 3 {
		t.Fatalf("the proposed command decodes as %q %q %d %v", src, dst, expected, err)
	}
	if len(ops) != 2 || ops[0].Type != protocol.OpMVCCPut || string(ops[0].Key) != "t/db/meta" ||
		string(ops[0].Value) != "m2" || ops[0].LeaseID != 0 ||
		ops[1].Type != protocol.OpMVCCDelete || string(ops[1].Key) != "t/db/p/9" {
		t.Fatalf("the proposed ops are %+v", ops)
	}
	if len(cmps) != 2 ||
		string(cmps[0].Key) != "t/db/p/1" || cmps[0].Target != protocol.CompareTargetModRevision ||
		cmps[0].Result != protocol.CompareResultEqual || cmps[0].ModRevision != 0 ||
		string(cmps[1].Key) != "t/db/p/2" || cmps[1].Target != protocol.CompareTargetModRevision ||
		cmps[1].Result != protocol.CompareResultLess || cmps[1].ModRevision != 6 {
		t.Fatalf("the proposed compares are %+v", cmps)
	}

	appErr, _ := recordingHost(sm.Result{Data: protocol.EncodeCapabilityError("subsystem not configured")})
	var unsupported *CapabilityUnsupportedError
	if _, err := NewMVCCKVService(appErr, 1).(*MVCCKVService).MovePrefix(context.Background(), validMoveRequest()); !stderrors.As(err, &unsupported) {
		t.Fatalf("an encoded application error = %v, want a CapabilityUnsupportedError", err)
	}
}

// TestMVCCKVService_MovePrefix_GuardRefusalIsWriteGuardRefused: a compare the
// engine found not to hold comes back as *WriteGuardRefusedError naming its
// key, distinct from the count conflict.
func TestMVCCKVService_MovePrefix_GuardRefusalIsWriteGuardRefused(t *testing.T) {
	inner, _ := recordingHost(sm.Result{Data: protocol.EncodeGuardRefusal([]byte("t/db/p/2"))})
	host := &gatedHost{mockHost: inner, enabled: map[uint64]bool{1: true}}
	_, err := NewMVCCKVService(host, 1).(*MVCCKVService).MovePrefix(context.Background(), validMoveRequest())
	var refused *WriteGuardRefusedError
	if !stderrors.As(err, &refused) || string(refused.Key) != "t/db/p/2" {
		t.Fatalf("MovePrefix = %v, want *WriteGuardRefusedError naming t/db/p/2", err)
	}
}

// movingMVCC is a fakeMVCC that records the prefix moves it is handed.
type movingMVCC struct {
	*fakeMVCC
	moves []*MovePrefixRequest
	err   error
}

func (m *movingMVCC) MovePrefix(_ context.Context, req *MovePrefixRequest) (int64, error) {
	m.moves = append(m.moves, req)
	return 9, m.err
}

func TestHardConfinedMVCC_MovePrefix(t *testing.T) {
	ctx := context.Background()
	confined := map[string]func(r *MovePrefixRequest){
		"src in a confined namespace": func(r *MovePrefixRequest) { r.Src = delegationPolicyConfinedPrefix + "t/" },
		"src above a confined one":    func(r *MovePrefixRequest) { r.Src = InternalKeyPrefixSystem },
		"dst in a confined namespace": func(r *MovePrefixRequest) { r.Dst = InternalKeyPrefixSystem + "cluster_ca/x/" },
		"op on a material key": func(r *MovePrefixRequest) {
			r.Ops = []Op{{Type: OpPut, Key: []byte(InternalKeyPrefixSystem + "cert_ca/t/material"), Value: []byte("k")}}
		},
		"compare on a material key": func(r *MovePrefixRequest) {
			r.Compares = []Compare{{Key: []byte(InternalKeyPrefixSystem + "cert_ca/t/material"), Target: CompareModRevision, Result: CompareEqual}}
		},
	}
	for name, mutate := range confined {
		t.Run(name, func(t *testing.T) {
			inner := &movingMVCC{fakeMVCC: newFakeMVCC()}
			req := validMoveRequest()
			mutate(req)
			_, err := NewHardConfinedMVCC(inner).(PrefixMover).MovePrefix(ctx, req)
			var hc *HardConfinedKeyError
			if !stderrors.As(err, &hc) || len(inner.moves) != 0 {
				t.Fatalf("MovePrefix = %v after %d forwards, want *HardConfinedKeyError and none", err, len(inner.moves))
			}
		})
	}

	inner := &movingMVCC{fakeMVCC: newFakeMVCC()}
	req := validMoveRequest()
	if rev, err := NewHardConfinedMVCC(inner).(PrefixMover).MovePrefix(ctx, req); err != nil || rev != 9 || len(inner.moves) != 1 || inner.moves[0] != req {
		t.Fatalf("MovePrefix = (%d, %v) with forwards %v, want the request forwarded", rev, err, inner.moves)
	}

	_, err := NewHardConfinedMVCC(newFakeMVCC()).(PrefixMover).MovePrefix(ctx, validMoveRequest())
	var unsupported *PrefixMoveUnsupportedError
	if !stderrors.As(err, &unsupported) {
		t.Fatalf("MovePrefix over an inner without it = %v, want *PrefixMoveUnsupportedError", err)
	}
}
