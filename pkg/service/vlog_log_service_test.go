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

//go:build qrdb_vlog

package service

import (
	"bytes"
	"context"
	"errors"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// staticSignerProvider hands out one signer, or fails.
type staticSignerProvider struct {
	signer vlog.HeadSigner
	err    error
	asked  []vlog.HeadSignerRequest
}

func (p *staticSignerProvider) HeadSigner(_ context.Context, req vlog.HeadSignerRequest) (vlog.HeadSigner, error) {
	p.asked = append(p.asked, req)
	return p.signer, p.err
}

var _ vlog.HeadSignerProvider = (*staticSignerProvider)(nil)

var testLogRef = LogRef{Database: "ledger", Log: "events"}

// acmeCtx is a caller in tenant acme.
func acmeCtx() context.Context {
	return tenant.WithTenantID(context.Background(), "acme")
}

// newTestLogService composes a LogService over host on shard 1, gated open.
func newTestLogService(t *testing.T, host *mockHost, signers vlog.HeadSignerProvider) LogService {
	t.Helper()
	if signers == nil {
		signers = &staticSignerProvider{}
	}
	svc, err := NewLogService(LogServiceConfig{
		Resolver: &fixedShardResolver{host: &gatedHost{mockHost: host, enabled: map[uint64]bool{1: true}}, shardID: 1},
		Signers:  signers,
	})
	if err != nil {
		t.Fatalf("NewLogService: %v", err)
	}
	return svc
}

// decodeLogCommand splits one proposed OpVerifiableLog command.
func decodeLogCommand(t *testing.T, cmd []byte) (byte, string, []byte) {
	t.Helper()
	if len(cmd) == 0 || cmd[0] != protocol.OpVerifiableLog {
		t.Fatalf("proposed command is not OpVerifiableLog: %x", cmd)
	}
	subtype, tenantID, body, err := protocol.DecodeVerifiableLogCommand(cmd[1:])
	if err != nil {
		t.Fatalf("DecodeVerifiableLogCommand: %v", err)
	}
	return subtype, tenantID, body
}

// readHost answers every SyncRead with the function's answer.
func readHost(answer func(query interface{}) (interface{}, error)) *mockHost {
	return &mockHost{syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
		return answer(query)
	}}
}

func TestNewLogService_Composes(t *testing.T) {
	svc, err := NewLogService(LogServiceConfig{Resolver: &fixedShardResolver{}, Signers: &staticSignerProvider{}})
	if err != nil || svc == nil {
		t.Fatalf("NewLogService = %v, %v; want a service", svc, err)
	}
}

func TestNewLogService_RequiresEveryDependency(t *testing.T) {
	for name, cfg := range map[string]LogServiceConfig{
		"vlog shard resolver":       {Signers: &staticSignerProvider{}},
		"vlog head signer provider": {Resolver: &fixedShardResolver{}},
	} {
		_, err := NewLogService(cfg)
		var notConfigured *SubsystemNotConfiguredError
		if !errors.As(err, &notConfigured) || notConfigured.Subsystem != name {
			t.Errorf("missing %s: err = %v, want SubsystemNotConfiguredError", name, err)
		}
	}
}

func TestLogService_CreateLog_ProposesTheDescriptor(t *testing.T) {
	host, cmds := recordingHost(sm.Result{})
	svc := newTestLogService(t, host, nil)
	resp, err := svc.CreateLog(acmeCtx(), &CreateLogRequest{LogRef: testLogRef, Origin: "db.example.com/events", KeyRef: "hsm:7"})
	if err != nil {
		t.Fatalf("CreateLog: %v", err)
	}
	want := vlog.LogID{TenantID: "acme", Database: "ledger", Log: "events"}
	if resp.ID != want || resp.KeyRef != "hsm:7" {
		t.Fatalf("CreateLog = %+v, want %+v with KeyRef hsm:7", resp, want)
	}
	subtype, tenantID, body := decodeLogCommand(t, (*cmds)[0])
	if subtype != protocol.VerifiableLogSubtypeCreate {
		t.Fatalf("subtype = %d, want create", subtype)
	}
	id, desc, key, err := protocol.DecodeVerifiableLogCreate(tenantID, body)
	if err != nil || id != want || desc.Origin != "db.example.com/events" || desc.Hash != vlog.HashSHA256 || key != nil {
		t.Fatalf("decoded create = %+v, %+v, key %v, %v", id, desc, key, err)
	}
}

func TestLogService_CreateLog_GeneratesTheLogsOwnKey(t *testing.T) {
	host, cmds := recordingHost(sm.Result{})
	svc := newTestLogService(t, host, nil)
	resp, err := svc.CreateLog(acmeCtx(), &CreateLogRequest{LogRef: testLogRef, Origin: "o", SigningKeyType: vlog.SignatureEd25519})
	if err != nil {
		t.Fatalf("CreateLog: %v", err)
	}
	if resp.KeyRef != vlog.KeyRef(vlog.FirstKeyVersion) {
		t.Fatalf("KeyRef = %q, want the first key version's", resp.KeyRef)
	}
	_, tenantID, body := decodeLogCommand(t, (*cmds)[0])
	_, desc, key, err := protocol.DecodeVerifiableLogCreate(tenantID, body)
	if err != nil || key == nil || key.Version != vlog.FirstKeyVersion || desc.KeyRef != resp.KeyRef {
		t.Fatalf("decoded create = %+v, key %v, %v; want the generated key", desc, key, err)
	}
}

func TestLogService_CreateLog_Refusals(t *testing.T) {
	host, cmds := recordingHost(sm.Result{})
	svc := newTestLogService(t, host, nil)
	ctx := acmeCtx()

	var crossTenant *tenant.CrossTenantDeniedError
	if _, err := svc.CreateLog(ctx, &CreateLogRequest{LogRef: LogRef{TenantID: "other", Database: "d", Log: "l"}}); !errors.As(err, &crossTenant) {
		t.Errorf("cross-tenant create: err = %v, want CrossTenantDeniedError", err)
	}
	var badName *vlog.InvalidLogNameError
	if _, err := svc.CreateLog(ctx, &CreateLogRequest{LogRef: LogRef{Database: "d", Log: "a/b"}}); !errors.As(err, &badName) {
		t.Errorf("slash in name: err = %v, want InvalidLogNameError", err)
	}
	if _, err := svc.CreateLog(ctx, &CreateLogRequest{LogRef: testLogRef, SigningKeyType: "rot13"}); err == nil {
		t.Error("unknown key type: no error")
	}
	if len(*cmds) != 0 {
		t.Fatalf("a refused create proposed %d commands", len(*cmds))
	}
}

func TestLogService_Writes_RefusedBeforeGateActive(t *testing.T) {
	inner, cmds := recordingHost(sm.Result{})
	svc, err := NewLogService(LogServiceConfig{
		Resolver: &fixedShardResolver{host: &gatedHost{mockHost: inner, enabled: map[uint64]bool{}}, shardID: 4},
		Signers:  &staticSignerProvider{},
	})
	if err != nil {
		t.Fatalf("NewLogService: %v", err)
	}
	_, err = svc.Append(acmeCtx(), &AppendRequest{LogRef: testLogRef, Entries: [][]byte{[]byte("e")}})
	var inactive *LogFeatureNotActiveError
	if !errors.As(err, &inactive) || inactive.TenantID != "acme" || inactive.ShardID != 4 {
		t.Fatalf("err = %v, want LogFeatureNotActiveError for acme on shard 4", err)
	}
	if len(*cmds) != 0 {
		t.Fatalf("a gated write proposed %d commands", len(*cmds))
	}
}

func TestLogService_Append_ReturnsTheFirstIndex(t *testing.T) {
	host, cmds := recordingHost(sm.Result{Value: 12})
	svc := newTestLogService(t, host, nil)
	resp, err := svc.Append(acmeCtx(), &AppendRequest{LogRef: testLogRef, Entries: [][]byte{[]byte("a"), []byte("b")}})
	if err != nil || resp.FirstIndex != 12 || resp.Count != 2 {
		t.Fatalf("Append = %+v, %v; want first index 12, count 2", resp, err)
	}
	_, tenantID, body := decodeLogCommand(t, (*cmds)[0])
	_, entries, err := protocol.DecodeVerifiableLogAppend(tenantID, body)
	if err != nil || len(entries) != 2 || !bytes.Equal(entries[1], []byte("b")) {
		t.Fatalf("decoded append = %q, %v", entries, err)
	}
}

func TestLogService_Append_Refusals(t *testing.T) {
	ctx := acmeCtx()

	resolverErr := errors.New("no group")
	svc, _ := NewLogService(LogServiceConfig{Resolver: &fixedShardResolver{err: resolverErr}, Signers: &staticSignerProvider{}})
	if _, err := svc.Append(ctx, &AppendRequest{LogRef: testLogRef, Entries: [][]byte{[]byte("a")}}); !errors.Is(err, resolverErr) {
		t.Errorf("resolver failure: err = %v", err)
	}

	svc = newTestLogService(t, proposeFailureHost(errors.New("raft down")), nil)
	if _, err := svc.Append(ctx, &AppendRequest{LogRef: testLogRef, Entries: [][]byte{[]byte("a")}}); err == nil {
		t.Error("propose failure: no error")
	}

	host, _ := recordingHost(sm.Result{})
	svc = newTestLogService(t, host, nil)
	var encode *EncodeRequestError
	if _, err := svc.Append(ctx, &AppendRequest{LogRef: testLogRef}); !errors.As(err, &encode) {
		t.Errorf("empty append: err = %v, want EncodeRequestError", err)
	}
	var crossTenant *tenant.CrossTenantDeniedError
	if _, err := svc.Append(ctx, &AppendRequest{LogRef: LogRef{TenantID: "other", Database: "d", Log: "l"}}); !errors.As(err, &crossTenant) {
		t.Errorf("cross-tenant append: err = %v, want CrossTenantDeniedError", err)
	}
}

func TestLogService_ListLogs_ReadsTheCallersDatabase(t *testing.T) {
	want := []vlog.LogID{{TenantID: "acme", Database: "ledger", Log: "events"}}
	var asked vlog.ListRequest
	svc := newTestLogService(t, readHost(func(q interface{}) (interface{}, error) {
		asked, _ = q.(vlog.ListRequest)
		return want, nil
	}), nil)
	resp, err := svc.ListLogs(acmeCtx(), &ListLogsRequest{Database: "ledger"})
	if err != nil || len(resp.Logs) != 1 || resp.Logs[0] != want[0] {
		t.Fatalf("ListLogs = %+v, %v", resp, err)
	}
	if asked != (vlog.ListRequest{TenantID: "acme", Database: "ledger"}) {
		t.Fatalf("read %+v, want acme's ledger", asked)
	}
}

func TestLogService_ListLogs_Refusals(t *testing.T) {
	ctx := acmeCtx()
	svc := newTestLogService(t, readHost(func(interface{}) (interface{}, error) {
		return nil, &quicraft.NotLeaderError{ShardID: 1, LeaderID: 3, LeaderAddress: "n3:4433"}
	}), nil)
	var notLeader *NotLeaderError
	if _, err := svc.ListLogs(ctx, &ListLogsRequest{Database: "ledger"}); !errors.As(err, &notLeader) || notLeader.LeaderID != 3 {
		t.Errorf("not leader: err = %v, want NotLeaderError naming node 3", err)
	}
	var crossTenant *tenant.CrossTenantDeniedError
	if _, err := svc.ListLogs(ctx, &ListLogsRequest{TenantID: "other", Database: "ledger"}); !errors.As(err, &crossTenant) {
		t.Errorf("cross-tenant list: err = %v, want CrossTenantDeniedError", err)
	}
}

func TestLogService_GetEntries_ReturnsThePage(t *testing.T) {
	var asked vlog.GetRequest
	svc := newTestLogService(t, readHost(func(q interface{}) (interface{}, error) {
		asked, _ = q.(vlog.GetRequest)
		return []vlog.Entry{{Index: 5, Body: []byte("x")}}, nil
	}), nil)
	resp, err := svc.GetEntries(acmeCtx(), &GetEntriesRequest{LogRef: testLogRef, From: 5, Limit: 10})
	if err != nil || len(resp.Entries) != 1 || resp.Entries[0].Index != 5 {
		t.Fatalf("GetEntries = %+v, %v", resp, err)
	}
	if asked.From != 5 || asked.Limit != 10 || asked.ID.TenantID != "acme" {
		t.Fatalf("read %+v", asked)
	}
}

func TestLogService_GetEntries_Refusals(t *testing.T) {
	ctx := acmeCtx()
	fault := errors.New("disk")
	svc := newTestLogService(t, readHost(func(interface{}) (interface{}, error) { return nil, fault }), nil)
	var readErr *ReadError
	if _, err := svc.GetEntries(ctx, &GetEntriesRequest{LogRef: testLogRef}); !errors.As(err, &readErr) || !errors.Is(err, fault) {
		t.Errorf("read failure: err = %v, want ReadError wrapping the fault", err)
	}
	svc = newTestLogService(t, readHost(func(interface{}) (interface{}, error) { return "not entries", nil }), nil)
	var wrongType *unexpectedResultTypeError
	if _, err := svc.GetEntries(ctx, &GetEntriesRequest{LogRef: testLogRef}); !errors.As(err, &wrongType) {
		t.Errorf("wrong answer type: err = %v, want unexpectedResultTypeError", err)
	}
	var badName *vlog.InvalidLogNameError
	if _, err := svc.GetEntries(ctx, &GetEntriesRequest{LogRef: LogRef{Log: "l"}}); !errors.As(err, &badName) {
		t.Errorf("no database: err = %v, want InvalidLogNameError", err)
	}
}

func TestLogService_ProveInclusion_CarriesBothSizes(t *testing.T) {
	proof := [][]byte{{1}, {2}}
	svc := newTestLogService(t, readHost(func(q interface{}) (interface{}, error) {
		if r, ok := q.(vlog.InclusionRequest); !ok || r.Index != 3 || r.Size != 8 {
			t.Errorf("read %+v", q)
		}
		return proof, nil
	}), nil)
	resp, err := svc.ProveInclusion(acmeCtx(), &InclusionRequest{LogRef: testLogRef, Index: 3, Size: 8})
	if err != nil || resp.Index != 3 || resp.Size != 8 || len(resp.Hashes) != 2 {
		t.Fatalf("ProveInclusion = %+v, %v", resp, err)
	}
}

func TestLogService_ProveInclusion_Refusals(t *testing.T) {
	ctx := acmeCtx()
	svc := newTestLogService(t, readHost(func(interface{}) (interface{}, error) {
		return nil, &vlog.SizeOutOfRangeError{}
	}), nil)
	var readErr *ReadError
	if _, err := svc.ProveInclusion(ctx, &InclusionRequest{LogRef: testLogRef, Index: 9, Size: 2}); !errors.As(err, &readErr) {
		t.Errorf("out of range: err = %v, want ReadError", err)
	}
	var crossTenant *tenant.CrossTenantDeniedError
	if _, err := svc.ProveInclusion(ctx, &InclusionRequest{LogRef: LogRef{TenantID: "other", Database: "d", Log: "l"}}); !errors.As(err, &crossTenant) {
		t.Errorf("cross-tenant proof: err = %v, want CrossTenantDeniedError", err)
	}
}

func TestLogService_ProveConsistency_CarriesBothSizes(t *testing.T) {
	svc := newTestLogService(t, readHost(func(q interface{}) (interface{}, error) {
		if r, ok := q.(vlog.ConsistencyRequest); !ok || r.Size1 != 4 || r.Size2 != 9 {
			t.Errorf("read %+v", q)
		}
		return [][]byte{{7}}, nil
	}), nil)
	resp, err := svc.ProveConsistency(acmeCtx(), &ConsistencyRequest{LogRef: testLogRef, Size1: 4, Size2: 9})
	if err != nil || resp.Size1 != 4 || resp.Size2 != 9 || len(resp.Hashes) != 1 {
		t.Fatalf("ProveConsistency = %+v, %v", resp, err)
	}
}

func TestLogService_ProveConsistency_Refusals(t *testing.T) {
	ctx := acmeCtx()
	svc := newTestLogService(t, readHost(func(interface{}) (interface{}, error) { return [][]string{}, nil }), nil)
	var wrongType *unexpectedResultTypeError
	if _, err := svc.ProveConsistency(ctx, &ConsistencyRequest{LogRef: testLogRef, Size1: 1, Size2: 2}); !errors.As(err, &wrongType) {
		t.Errorf("wrong answer type: err = %v, want unexpectedResultTypeError", err)
	}
	var badName *vlog.InvalidLogNameError
	if _, err := svc.ProveConsistency(ctx, &ConsistencyRequest{LogRef: LogRef{Database: "d/x", Log: "l"}}); !errors.As(err, &badName) {
		t.Errorf("slash in database: err = %v, want InvalidLogNameError", err)
	}
}

func TestLogService_Head_ReadsTheHead(t *testing.T) {
	svc := newTestLogService(t, readHost(func(interface{}) (interface{}, error) {
		return &vlog.Head{Size: 6, Root: []byte{9}}, nil
	}), nil)
	resp, err := svc.Head(acmeCtx(), &HeadRequest{LogRef: testLogRef})
	if err != nil || resp.Head.Size != 6 || !bytes.Equal(resp.Head.Root, []byte{9}) {
		t.Fatalf("Head = %+v, %v", resp, err)
	}
}

func TestLogService_Head_Refusals(t *testing.T) {
	ctx := acmeCtx()
	svc := newTestLogService(t, readHost(func(interface{}) (interface{}, error) {
		return nil, &vlog.LogNotFoundError{}
	}), nil)
	var readErr *ReadError
	if _, err := svc.Head(ctx, &HeadRequest{LogRef: testLogRef}); !errors.As(err, &readErr) {
		t.Errorf("missing log: err = %v, want ReadError", err)
	}
	var crossTenant *tenant.CrossTenantDeniedError
	if _, err := svc.Head(ctx, &HeadRequest{LogRef: LogRef{TenantID: "other", Database: "d", Log: "l"}}); !errors.As(err, &crossTenant) {
		t.Errorf("cross-tenant head: err = %v, want CrossTenantDeniedError", err)
	}
}

// signHeadHost answers the descriptor and head reads and records proposals.
func signHeadHost(desc *vlog.Descriptor, head *vlog.Head) (*mockHost, *[][]byte) {
	host, cmds := recordingHost(sm.Result{})
	host.syncReadFn = func(_ context.Context, _ uint64, q interface{}) (interface{}, error) {
		switch q.(type) {
		case vlog.DescriptorRequest:
			return desc, nil
		case vlog.HeadRequest:
			return head, nil
		}
		return nil, errors.New("unexpected read")
	}
	return host, cmds
}

func TestLogService_SignHead_RecordsTheCheckpoint(t *testing.T) {
	key, err := vlog.GenerateSigningKey(vlog.SignatureEd25519, vlog.FirstKeyVersion)
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	signer, err := vlog.NewSoftwareHeadSigner("db.example.com/events", key)
	if err != nil {
		t.Fatalf("NewSoftwareHeadSigner: %v", err)
	}
	desc := &vlog.Descriptor{Origin: "db.example.com/events", Hash: vlog.HashSHA256, KeyRef: vlog.KeyRef(key.Version)}
	root := bytes.Repeat([]byte{4}, 32)
	host, cmds := signHeadHost(desc, &vlog.Head{Size: 3, Root: root})
	signers := &staticSignerProvider{signer: signer}
	svc := newTestLogService(t, host, signers)

	resp, err := svc.SignHead(acmeCtx(), &SignHeadRequest{LogRef: testLogRef})
	if err != nil {
		t.Fatalf("SignHead: %v", err)
	}
	if err := vlog.VerifyHead(signer.Public(), desc.Origin, resp.Head); err != nil {
		t.Fatalf("signed head does not verify: %v", err)
	}
	if signers.asked[0].KeyRef != desc.KeyRef || signers.asked[0].TenantID != "acme" {
		t.Fatalf("signer asked for %+v", signers.asked[0])
	}
	subtype, tenantID, body := decodeLogCommand(t, (*cmds)[0])
	if subtype != protocol.VerifiableLogSubtypeCheckpoint {
		t.Fatalf("subtype = %d, want checkpoint", subtype)
	}
	_, size, gotRoot, note, err := protocol.DecodeVerifiableLogCheckpoint(tenantID, body)
	if err != nil || size != 3 || !bytes.Equal(gotRoot, root) || !bytes.Equal(note, resp.Checkpoint) {
		t.Fatalf("decoded checkpoint = %d, %x, %q, %v", size, gotRoot, note, err)
	}
}

func TestLogService_SignHead_Refusals(t *testing.T) {
	ctx := acmeCtx()
	desc := &vlog.Descriptor{Origin: "o", KeyRef: "hsm:1"}
	head := &vlog.Head{Size: 1, Root: bytes.Repeat([]byte{1}, 32)}

	providerErr := errors.New("hsm offline")
	host, cmds := signHeadHost(desc, head)
	svc := newTestLogService(t, host, &staticSignerProvider{err: providerErr})
	if _, err := svc.SignHead(ctx, &SignHeadRequest{LogRef: testLogRef}); !errors.Is(err, providerErr) {
		t.Errorf("provider failure: err = %v", err)
	}

	host, _ = signHeadHost(nil, head)
	host.syncReadFn = func(context.Context, uint64, interface{}) (interface{}, error) { return nil, errors.New("disk") }
	svc = newTestLogService(t, host, nil)
	var readErr *ReadError
	if _, err := svc.SignHead(ctx, &SignHeadRequest{LogRef: testLogRef}); !errors.As(err, &readErr) {
		t.Errorf("descriptor read failure: err = %v, want ReadError", err)
	}

	host, _ = signHeadHost(desc, nil)
	host.syncReadFn = func(_ context.Context, _ uint64, q interface{}) (interface{}, error) {
		if _, ok := q.(vlog.DescriptorRequest); ok {
			return desc, nil
		}
		return nil, errors.New("disk")
	}
	svc = newTestLogService(t, host, nil)
	if _, err := svc.SignHead(ctx, &SignHeadRequest{LogRef: testLogRef}); !errors.As(err, &readErr) {
		t.Errorf("head read failure: err = %v, want ReadError", err)
	}

	var crossTenant *tenant.CrossTenantDeniedError
	if _, err := svc.SignHead(ctx, &SignHeadRequest{LogRef: LogRef{TenantID: "other", Database: "d", Log: "l"}}); !errors.As(err, &crossTenant) {
		t.Errorf("cross-tenant sign: err = %v, want CrossTenantDeniedError", err)
	}
	if len(*cmds) != 0 {
		t.Fatalf("a refused sign proposed %d commands", len(*cmds))
	}
}

func TestLogService_ExpireBodies_ReturnsTheCount(t *testing.T) {
	host, cmds := recordingHost(sm.Result{Value: 5})
	svc := newTestLogService(t, host, nil)
	resp, err := svc.ExpireBodies(acmeCtx(), &ExpireBodiesRequest{LogRef: testLogRef, Cutoff: 5})
	if err != nil || resp.Expired != 5 {
		t.Fatalf("ExpireBodies = %+v, %v", resp, err)
	}
	_, tenantID, body := decodeLogCommand(t, (*cmds)[0])
	if _, cutoff, err := protocol.DecodeVerifiableLogExpireBodies(tenantID, body); err != nil || cutoff != 5 {
		t.Fatalf("decoded cutoff = %d, %v", cutoff, err)
	}
}

func TestLogService_ExpireBodies_Refusals(t *testing.T) {
	ctx := acmeCtx()
	refusal := protocol.EncodeAppError(protocol.AppErrorConflict, "vlog: bodies may not expire")
	host, _ := recordingHost(sm.Result{Data: refusal})
	svc := newTestLogService(t, host, nil)
	var proposeErr *ProposeError
	var conflict *CommandConflictError
	if _, err := svc.ExpireBodies(ctx, &ExpireBodiesRequest{LogRef: testLogRef, Cutoff: 1}); !errors.As(err, &proposeErr) || !errors.As(err, &conflict) {
		t.Errorf("refused apply: err = %v, want ProposeError wrapping CommandConflictError", err)
	}

	var crossTenant *tenant.CrossTenantDeniedError
	if _, err := svc.ExpireBodies(ctx, &ExpireBodiesRequest{LogRef: LogRef{TenantID: "other", Database: "d", Log: "l"}}); !errors.As(err, &crossTenant) {
		t.Errorf("cross-tenant expire: err = %v, want CrossTenantDeniedError", err)
	}
	var badName *vlog.InvalidLogNameError
	if _, err := svc.ExpireBodies(ctx, &ExpireBodiesRequest{LogRef: LogRef{Database: "d"}}); !errors.As(err, &badName) {
		t.Errorf("no log name: err = %v, want InvalidLogNameError", err)
	}
}
