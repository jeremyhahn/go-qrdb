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
	"crypto"
	"errors"
	"reflect"
	"strconv"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// staticSignerProvider hands out one signer, or fails.
type staticSignerProvider struct {
	signer    vlog.HeadSigner
	err       error
	asked     []vlog.HeadSignerRequest
	created   vlog.CreatedKey
	createErr error
}

func (p *staticSignerProvider) CreateKey(context.Context, vlog.CreateKeyRequest) (vlog.CreatedKey, error) {
	return p.created, p.createErr
}

func (p *staticSignerProvider) HeadSigner(_ context.Context, req vlog.HeadSignerRequest) (vlog.HeadSigner, error) {
	p.asked = append(p.asked, req)
	return p.signer, p.err
}

var _ vlog.HeadSignerProvider = (*staticSignerProvider)(nil)

// hsmProvider is a HeadSignerProvider whose keys never leave it -- the shape
// of a hardware or KMS provider a composer passes in: CreateKey answers its
// own reference and the public key, never a Record, so go-qrdb stores no key.
type hsmProvider struct {
	keys    map[string]vlog.SigningKey
	created []vlog.CreateKeyRequest
	asked   []vlog.HeadSignerRequest
}

var _ vlog.HeadSignerProvider = (*hsmProvider)(nil)

func newHSMProvider() *hsmProvider { return &hsmProvider{keys: map[string]vlog.SigningKey{}} }

func (p *hsmProvider) CreateKey(_ context.Context, req vlog.CreateKeyRequest) (vlog.CreatedKey, error) {
	p.created = append(p.created, req)
	k, err := vlog.GenerateSigningKey(req.Type, req.Version)
	if err != nil {
		return vlog.CreatedKey{}, err
	}
	signer, _, err := k.Signer()
	if err != nil {
		return vlog.CreatedKey{}, err
	}
	ref := "hsm:" + strconv.FormatUint(uint64(req.Version), 10)
	p.keys[ref] = k
	return vlog.CreatedKey{Ref: ref, Public: signer.Public()}, nil
}

func (p *hsmProvider) HeadSigner(_ context.Context, req vlog.HeadSignerRequest) (vlog.HeadSigner, error) {
	p.asked = append(p.asked, req)
	k, ok := p.keys[req.KeyRef]
	if !ok {
		return nil, &vlog.KeyNotFoundError{Version: req.KeyVersion}
	}
	return vlog.NewSoftwareHeadSigner(vlog.LogID{TenantID: req.TenantID, Database: req.Database, Log: req.Log}.String(), k)
}

// hsmKey creates a key in p for the test log and answers the descriptor's
// record of it.
func hsmKey(t *testing.T, p *hsmProvider, version uint32) vlog.HeadKey {
	t.Helper()
	req := vlog.CreateKeyRequest{TenantID: "acme", Database: testLogRef.Database, Log: testLogRef.Log, Type: vlog.SignatureEd25519, Version: version}
	created, err := p.CreateKey(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	key, err := created.HeadKey(req)
	if err != nil {
		t.Fatalf("HeadKey: %v", err)
	}
	return key
}

var testLogRef = LogRef{Database: "ledger", Log: "events"}

// acmeCtx is a caller in tenant acme.
func acmeCtx() context.Context {
	return tenant.WithTenantID(context.Background(), "acme")
}

// newTestLogService composes a LogService over host on shard 1, gated open.
// A nil signers composes the software default.
func newTestLogService(t *testing.T, host *mockHost, signers vlog.HeadSignerProvider) LogService {
	t.Helper()
	svc, err := NewLogService(LogServiceConfig{
		Resolver:  &fixedShardResolver{host: &gatedHost{mockHost: host, enabled: map[uint64]bool{1: true}}, shardID: 1},
		Databases: noDatabases(t),
		Signers:   signers,
	})
	if err != nil {
		t.Fatalf("NewLogService: %v", err)
	}
	return svc
}

// noDatabases is a database registry holding no database: every log names a
// database it has no record of, which the log service lets through.
func noDatabases(t *testing.T) *database.Manager {
	t.Helper()
	mgr := databasetest.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}

// barrierDatabases is a database registry holding acme's barrier-enabled
// database "secure", unsealed under the system barrier.
func barrierDatabases(t *testing.T) *database.Manager {
	t.Helper()
	system, _ := newTestSoftwareBarrier(t)
	ctx := context.Background()
	if err := system.Initialize(ctx, seal.MustCredentials(ctx, []byte("system-pass"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return newPassphraseDatabases(t, system)
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
	svc, err := NewLogService(LogServiceConfig{Resolver: &fixedShardResolver{}, Databases: noDatabases(t), Signers: &staticSignerProvider{}})
	if err != nil || svc == nil {
		t.Fatalf("NewLogService = %v, %v; want a service", svc, err)
	}
}

func TestNewLogService_RequiresTheResolver(t *testing.T) {
	_, err := NewLogService(LogServiceConfig{Databases: noDatabases(t), Signers: &staticSignerProvider{}})
	var notConfigured *SubsystemNotConfiguredError
	if !errors.As(err, &notConfigured) || notConfigured.Subsystem != "vlog shard resolver" {
		t.Errorf("missing resolver: err = %v, want SubsystemNotConfiguredError", err)
	}
}

// TestNewLogService_RequiresTheDatabaseLookup pins that a log service is never
// composed without the registry it checks each database's isolation against.
func TestNewLogService_RequiresTheDatabaseLookup(t *testing.T) {
	_, err := NewLogService(LogServiceConfig{Resolver: &fixedShardResolver{}, Signers: &staticSignerProvider{}})
	var notConfigured *SubsystemNotConfiguredError
	if !errors.As(err, &notConfigured) || notConfigured.Subsystem != "vlog database lookup" {
		t.Errorf("missing database lookup: err = %v, want SubsystemNotConfiguredError", err)
	}
}

// failingLogDatabases is a database registry whose lookup fails.
type failingLogDatabases struct{ err error }

func (f failingLogDatabases) GetOwned(string, string) (*database.Database, error) { return nil, f.err }

var _ LogDatabaseLookup = failingLogDatabases{}

// TestLogService_RefusesABarrierDatabase pins go-qrdb#912's isolation check:
// the log registry and its keys live in the system shard under the system
// barrier, so every log operation naming a barrier-enabled database is
// refused, before a key is minted or a command proposed or read.
func TestLogService_RefusesABarrierDatabase(t *testing.T) {
	ctx := acmeCtx()
	hsm := newHSMProvider()
	host, cmds := recordingHost(sm.Result{})
	reads := 0
	host.syncReadFn = func(context.Context, uint64, interface{}) (interface{}, error) {
		reads++
		return nil, errors.New("unexpected read")
	}
	svc, err := NewLogService(LogServiceConfig{
		Resolver:  &fixedShardResolver{host: &gatedHost{mockHost: host, enabled: map[uint64]bool{1: true}}, shardID: 1},
		Databases: barrierDatabases(t),
		Signers:   hsm,
	})
	if err != nil {
		t.Fatalf("NewLogService: %v", err)
	}
	ref := LogRef{Database: "secure", Log: "events"}
	for name, call := range map[string]func() error{
		"create": func() error {
			_, err := svc.CreateLog(ctx, &CreateLogRequest{LogRef: ref, Origin: "o", SigningKeyType: vlog.SignatureEd25519})
			return err
		},
		"rotate key": func() error {
			_, err := svc.RotateKey(ctx, &RotateKeyRequest{LogRef: ref, SigningKeyType: vlog.SignatureEd25519})
			return err
		},
		"append": func() error {
			_, err := svc.Append(ctx, &AppendRequest{LogRef: ref, Entries: [][]byte{[]byte("e")}})
			return err
		},
		"sign head": func() error {
			_, err := svc.SignHead(ctx, &SignHeadRequest{LogRef: ref})
			return err
		},
		"expire bodies": func() error {
			_, err := svc.ExpireBodies(ctx, &ExpireBodiesRequest{LogRef: ref, Cutoff: 1})
			return err
		},
		"get entries": func() error {
			_, err := svc.GetEntries(ctx, &GetEntriesRequest{LogRef: ref})
			return err
		},
		"head": func() error {
			_, err := svc.Head(ctx, &HeadRequest{LogRef: ref})
			return err
		},
		"prove inclusion": func() error {
			_, err := svc.ProveInclusion(ctx, &InclusionRequest{LogRef: ref, Index: 0, Size: 1})
			return err
		},
		"prove consistency": func() error {
			_, err := svc.ProveConsistency(ctx, &ConsistencyRequest{LogRef: ref, Size1: 1, Size2: 2})
			return err
		},
		"list": func() error {
			_, err := svc.ListLogs(ctx, &ListLogsRequest{Database: "secure"})
			return err
		},
	} {
		var refused *BarrierDatabaseLogNotSupportedError
		if err := call(); !errors.As(err, &refused) || refused.TenantID != "acme" || refused.Database != "secure" {
			t.Errorf("%s: err = %v, want BarrierDatabaseLogNotSupportedError for acme's secure", name, err)
		}
	}
	if len(*cmds) != 0 || reads != 0 || len(hsm.created) != 0 {
		t.Fatalf("a refused log operation proposed %d commands, read %d times and minted %d keys", len(*cmds), reads, len(hsm.created))
	}

	// A database without a barrier, and one the registry has no record of,
	// are let through.
	if _, err := svc.Append(ctx, &AppendRequest{LogRef: testLogRef, Entries: [][]byte{[]byte("e")}}); err != nil {
		t.Fatalf("Append to an unrecorded database: %v", err)
	}
}

// TestLogService_DatabaseLookupFailureRefuses pins that a registry that cannot
// answer refuses the operation rather than letting it through unchecked.
func TestLogService_DatabaseLookupFailureRefuses(t *testing.T) {
	host, cmds := recordingHost(sm.Result{})
	dropping := &database.DroppingError{Name: "ledger"}
	svc, err := NewLogService(LogServiceConfig{
		Resolver:  &fixedShardResolver{host: &gatedHost{mockHost: host, enabled: map[uint64]bool{1: true}}, shardID: 1},
		Databases: failingLogDatabases{err: dropping},
	})
	if err != nil {
		t.Fatalf("NewLogService: %v", err)
	}
	if _, err := svc.Append(acmeCtx(), &AppendRequest{LogRef: testLogRef, Entries: [][]byte{[]byte("e")}}); !errors.Is(err, dropping) {
		t.Errorf("Append: err = %v, want the lookup's error", err)
	}
	if _, err := svc.ListLogs(acmeCtx(), &ListLogsRequest{Database: "ledger"}); !errors.Is(err, dropping) {
		t.Errorf("ListLogs: err = %v, want the lookup's error", err)
	}
	if len(*cmds) != 0 {
		t.Fatalf("a refused append proposed %d commands", len(*cmds))
	}
}

// TestNewLogService_DefaultsToTheSoftwareProvider pins that a composer which
// passes no provider gets the software one: the created key is the log's own
// sealed record, proposed with the descriptor.
func TestNewLogService_DefaultsToTheSoftwareProvider(t *testing.T) {
	host, cmds := recordingHost(sm.Result{})
	svc, err := NewLogService(LogServiceConfig{Resolver: &fixedShardResolver{host: &gatedHost{mockHost: host, enabled: map[uint64]bool{1: true}}, shardID: 1}, Databases: noDatabases(t)})
	if err != nil {
		t.Fatalf("NewLogService: %v", err)
	}
	if _, err := svc.CreateLog(acmeCtx(), &CreateLogRequest{LogRef: testLogRef, Origin: "o", SigningKeyType: vlog.SignatureECDSAP256SHA256}); err != nil {
		t.Fatalf("CreateLog: %v", err)
	}
	_, tenantID, body := decodeLogCommand(t, (*cmds)[0])
	if _, _, key, err := protocol.DecodeVerifiableLogCreate(tenantID, body); err != nil || key == nil {
		t.Fatalf("decoded create key = %v, %v; want the software record", key, err)
	}
}

func TestLogService_CreateLog_ProposesTheDescriptor(t *testing.T) {
	host, cmds := recordingHost(sm.Result{})
	svc := newTestLogService(t, host, nil)
	resp, err := svc.CreateLog(acmeCtx(), &CreateLogRequest{LogRef: testLogRef, Origin: "db.example.com/events"})
	if err != nil {
		t.Fatalf("CreateLog: %v", err)
	}
	want := vlog.LogID{TenantID: "acme", Database: "ledger", Log: "events"}
	if resp.ID != want || resp.Key != nil {
		t.Fatalf("CreateLog = %+v, want %+v with no key", resp, want)
	}
	subtype, tenantID, body := decodeLogCommand(t, (*cmds)[0])
	if subtype != protocol.VerifiableLogSubtypeCreate {
		t.Fatalf("subtype = %d, want create", subtype)
	}
	id, desc, key, err := protocol.DecodeVerifiableLogCreate(tenantID, body)
	if err != nil || id != want || desc.Origin != "db.example.com/events" || desc.Hash != vlog.HashSHA256 || key != nil || len(desc.Keys) != 0 {
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
	if resp.Key == nil || resp.Key.Ref != vlog.KeyRef(vlog.FirstKeyVersion) || resp.Key.Version != vlog.FirstKeyVersion {
		t.Fatalf("Key = %+v, want the first key version's", resp.Key)
	}
	if len(*cmds) != 1 {
		t.Fatalf("CreateLog proposed %d commands, want the one carrying descriptor and key", len(*cmds))
	}
	_, tenantID, body := decodeLogCommand(t, (*cmds)[0])
	_, desc, key, err := protocol.DecodeVerifiableLogCreate(tenantID, body)
	if err != nil || key == nil || key.Version != vlog.FirstKeyVersion || len(desc.Keys) != 1 || desc.Keys[0].Ref != resp.Key.Ref {
		t.Fatalf("decoded create = %+v, key %v, %v; want the generated key", desc, key, err)
	}
}

// TestLogService_CreateLog_HardwareProviderWritesNoRecord pins the seam: an
// injected provider creates the key, the descriptor records its reference
// and public key, and the proposal carries no key record.
func TestLogService_CreateLog_HardwareProviderWritesNoRecord(t *testing.T) {
	host, cmds := recordingHost(sm.Result{})
	hsm := newHSMProvider()
	svc := newTestLogService(t, host, hsm)
	resp, err := svc.CreateLog(acmeCtx(), &CreateLogRequest{LogRef: testLogRef, Origin: "o", SigningKeyType: vlog.SignatureEd25519})
	if err != nil {
		t.Fatalf("CreateLog: %v", err)
	}
	if len(hsm.created) != 1 || hsm.created[0].Version != vlog.FirstKeyVersion || hsm.created[0].TenantID != "acme" {
		t.Fatalf("provider asked to create %+v", hsm.created)
	}
	if resp.Key == nil || resp.Key.Ref != "hsm:1" {
		t.Fatalf("Key = %+v, want the provider's reference", resp.Key)
	}
	subtype, tenantID, body := decodeLogCommand(t, (*cmds)[0])
	_, desc, key, err := protocol.DecodeVerifiableLogCreate(tenantID, body)
	if subtype != protocol.VerifiableLogSubtypeCreate || err != nil || key != nil || len(desc.Keys) != 1 || desc.Keys[0].Ref != "hsm:1" {
		t.Fatalf("decoded create = %d, %+v, key %v, %v; want the reference and no record", subtype, desc, key, err)
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

// TestLogService_CreateLog_RefusesWhatTheProviderAnswers pins that a
// provider's failure, and a key it answers that cannot be recorded, refuse
// the create before anything is proposed.
func TestLogService_CreateLog_RefusesWhatTheProviderAnswers(t *testing.T) {
	providerErr := errors.New("hsm offline")
	for name, p := range map[string]*staticSignerProvider{
		"provider failure": {createErr: providerErr},
		"no public key":    {created: vlog.CreatedKey{Ref: "hsm:1"}},
		"no reference":     {created: vlog.CreatedKey{Public: newHSMProviderKey(t)}},
	} {
		t.Run(name, func(t *testing.T) {
			host, cmds := recordingHost(sm.Result{})
			svc := newTestLogService(t, host, p)
			if _, err := svc.CreateLog(acmeCtx(), &CreateLogRequest{LogRef: testLogRef, SigningKeyType: vlog.SignatureEd25519}); err == nil {
				t.Fatal("CreateLog: no error")
			}
			if len(*cmds) != 0 {
				t.Fatalf("a refused create proposed %d commands", len(*cmds))
			}
		})
	}
}

// newHSMProviderKey answers a fresh Ed25519 public key.
func newHSMProviderKey(t *testing.T) crypto.PublicKey {
	t.Helper()
	k, err := vlog.GenerateSigningKey(vlog.SignatureEd25519, vlog.FirstKeyVersion)
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	signer, _, err := k.Signer()
	if err != nil {
		t.Fatalf("Signer: %v", err)
	}
	return signer.Public()
}

// rotateHost answers the descriptor read with desc and records proposals.
func rotateHost(desc *vlog.Descriptor) (*mockHost, *[][]byte) {
	return signHeadHost(desc, nil)
}

func TestLogService_RotateKey_ThroughTheInjectedProvider(t *testing.T) {
	hsm := newHSMProvider()
	desc := &vlog.Descriptor{Origin: "o", Hash: vlog.HashSHA256, Keys: []vlog.HeadKey{hsmKey(t, hsm, vlog.FirstKeyVersion)}}
	host, cmds := rotateHost(desc)
	svc := newTestLogService(t, host, hsm)
	resp, err := svc.RotateKey(acmeCtx(), &RotateKeyRequest{LogRef: testLogRef, SigningKeyType: vlog.SignatureECDSAP256SHA256})
	if err != nil {
		t.Fatalf("RotateKey: %v", err)
	}
	if resp.Key.Version != vlog.FirstKeyVersion+1 || resp.Key.Ref != "hsm:2" {
		t.Fatalf("RotateKey = %+v, want version 2 under the provider's reference", resp.Key)
	}
	subtype, tenantID, body := decodeLogCommand(t, (*cmds)[0])
	if subtype != protocol.VerifiableLogSubtypeRotateKey {
		t.Fatalf("subtype = %d, want rotate key", subtype)
	}
	_, next, record, err := protocol.DecodeVerifiableLogRotateKey(tenantID, body)
	if err != nil || record != nil || next.Ref != "hsm:2" || next.Version != 2 {
		t.Fatalf("decoded rotate = %+v, record %v, %v; want the reference and no record", next, record, err)
	}
}

func TestLogService_RotateKey_SoftwareDefaultCarriesTheRecord(t *testing.T) {
	desc := &vlog.Descriptor{Origin: "o", Hash: vlog.HashSHA256, Keys: []vlog.HeadKey{hsmKey(t, newHSMProvider(), vlog.FirstKeyVersion)}}
	host, cmds := rotateHost(desc)
	svc := newTestLogService(t, host, nil)
	resp, err := svc.RotateKey(acmeCtx(), &RotateKeyRequest{LogRef: testLogRef, SigningKeyType: vlog.SignatureEd25519})
	if err != nil {
		t.Fatalf("RotateKey: %v", err)
	}
	_, tenantID, body := decodeLogCommand(t, (*cmds)[0])
	_, next, record, err := protocol.DecodeVerifiableLogRotateKey(tenantID, body)
	if err != nil || record == nil || record.Version != 2 || next.Ref != vlog.KeyRef(2) || resp.Key.Ref != next.Ref {
		t.Fatalf("decoded rotate = %+v, record %v, %v; want the sealed version-2 record", next, record, err)
	}
}

func TestLogService_RotateKey_Refusals(t *testing.T) {
	ctx := acmeCtx()
	desc := &vlog.Descriptor{Origin: "o", Hash: vlog.HashSHA256}
	host, cmds := rotateHost(desc)
	providerErr := errors.New("hsm offline")
	svc := newTestLogService(t, host, &staticSignerProvider{createErr: providerErr})
	if _, err := svc.RotateKey(ctx, &RotateKeyRequest{LogRef: testLogRef, SigningKeyType: vlog.SignatureEd25519}); !errors.Is(err, providerErr) {
		t.Errorf("provider failure: err = %v", err)
	}
	if _, err := svc.RotateKey(ctx, &RotateKeyRequest{LogRef: testLogRef, SigningKeyType: "rot13"}); err == nil {
		t.Error("unknown key type: no error")
	}
	var crossTenant *tenant.CrossTenantDeniedError
	if _, err := svc.RotateKey(ctx, &RotateKeyRequest{LogRef: LogRef{TenantID: "other", Database: "d", Log: "l"}, SigningKeyType: vlog.SignatureEd25519}); !errors.As(err, &crossTenant) {
		t.Errorf("cross-tenant rotate: err = %v, want CrossTenantDeniedError", err)
	}
	host.syncReadFn = func(context.Context, uint64, interface{}) (interface{}, error) { return nil, errors.New("disk") }
	var readErr *ReadError
	if _, err := svc.RotateKey(ctx, &RotateKeyRequest{LogRef: testLogRef, SigningKeyType: vlog.SignatureEd25519}); !errors.As(err, &readErr) {
		t.Errorf("descriptor read failure: err = %v, want ReadError", err)
	}
	if len(*cmds) != 0 {
		t.Fatalf("a refused rotate proposed %d commands", len(*cmds))
	}
}

func TestLogService_Writes_RefusedBeforeGateActive(t *testing.T) {
	inner, cmds := recordingHost(sm.Result{})
	svc, err := NewLogService(LogServiceConfig{
		Resolver:  &fixedShardResolver{host: &gatedHost{mockHost: inner, enabled: map[uint64]bool{}}, shardID: 4},
		Databases: noDatabases(t),
		Signers:   &staticSignerProvider{},
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

// featureHost is a host on which exactly the features in active are active.
type featureHost struct {
	*mockHost
	active map[feature.Feature]bool
}

var _ FeatureGatedHost = (*featureHost)(nil)

func (h *featureHost) FeatureEnabled(_ uint64, f feature.Feature) bool { return h.active[f] }

func TestLogService_KeyWrites_RefusedBeforeKeyFeatureActive(t *testing.T) {
	ctx := acmeCtx()
	desc := &vlog.Descriptor{Origin: "o", Hash: vlog.HashSHA256, Keys: []vlog.HeadKey{hsmKey(t, newHSMProvider(), vlog.FirstKeyVersion)}}
	inner, cmds := rotateHost(desc)
	device := newHSMProvider()
	svc, err := NewLogService(LogServiceConfig{
		Resolver:  &fixedShardResolver{host: &featureHost{mockHost: inner, active: map[feature.Feature]bool{featuregate.VerifiableLog: true}}, shardID: 4},
		Databases: noDatabases(t),
		Signers:   device,
	})
	if err != nil {
		t.Fatalf("NewLogService: %v", err)
	}
	for name, write := range map[string]func() error{
		"create": func() error {
			_, err := svc.CreateLog(ctx, &CreateLogRequest{LogRef: testLogRef, Origin: "o"})
			return err
		},
		"create with key": func() error {
			_, err := svc.CreateLog(ctx, &CreateLogRequest{LogRef: testLogRef, Origin: "o", SigningKeyType: vlog.SignatureEd25519})
			return err
		},
		"rotate key": func() error {
			_, err := svc.RotateKey(ctx, &RotateKeyRequest{LogRef: testLogRef, SigningKeyType: vlog.SignatureEd25519})
			return err
		},
	} {
		var inactive *LogFeatureNotActiveError
		if err := write(); !errors.As(err, &inactive) || inactive.Feature != featuregate.VerifiableLogKeys || inactive.ShardID != 4 {
			t.Fatalf("%s: err = %v, want LogFeatureNotActiveError for verifiable-log-keys on shard 4", name, err)
		}
	}
	if len(*cmds) != 0 {
		t.Fatalf("a write the replicas cannot all apply proposed %d commands", len(*cmds))
	}
	if len(device.created) != 0 {
		t.Fatalf("a refused write minted %d keys", len(device.created))
	}
	if _, err := svc.Append(ctx, &AppendRequest{LogRef: testLogRef, Entries: [][]byte{[]byte("e")}}); err != nil {
		t.Fatalf("Append under verifiable-log alone: %v", err)
	}
	if len(*cmds) != 1 {
		t.Fatalf("Append proposed %d commands, want 1", len(*cmds))
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
	svc, _ := NewLogService(LogServiceConfig{Resolver: &fixedShardResolver{err: resolverErr}, Databases: noDatabases(t), Signers: &staticSignerProvider{}})
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

	// Each state machine log refusal is its typed vlog error, naming the log
	// the request did: a not-found one is the error a read of the same log
	// returns (go-qrdb#912).
	id := vlog.LogID{TenantID: "acme", Database: testLogRef.Database, Log: testLogRef.Log}
	for reason, want := range map[protocol.LogRefusalReason]error{
		protocol.LogRefusalNotFound:         &vlog.LogNotFoundError{ID: id},
		protocol.LogRefusalExists:           &vlog.LogExistsError{ID: id},
		protocol.LogRefusalCheckpointExists: &vlog.CheckpointExistsError{Size: 7},
		protocol.LogRefusalExpireNotAllowed: &vlog.ExpireNotAllowedError{ID: id},
	} {
		host, _ = recordingHost(sm.Result{Data: protocol.EncodeLogRefusal(reason, 7, "vlog: refused")})
		svc = newTestLogService(t, host, nil)
		if _, err := svc.Append(ctx, &AppendRequest{LogRef: testLogRef, Entries: [][]byte{[]byte("a")}}); !reflect.DeepEqual(err, want) {
			t.Errorf("log refusal %d: err = %#v, want %#v", reason, err, want)
		}
	}
	var rejected *CommandRejectedError
	host, _ = recordingHost(sm.Result{Data: protocol.EncodeAppError(protocol.AppErrorInvalid, "bad batch")})
	svc = newTestLogService(t, host, nil)
	if _, err := svc.Append(ctx, &AppendRequest{LogRef: testLogRef, Entries: [][]byte{[]byte("a")}}); !errors.As(err, &rejected) {
		t.Errorf("other refusal: err = %v, want CommandRejectedError", err)
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

// TestLogService_SignHead_RecordsTheCheckpoint signs through an injected
// provider whose key go-qrdb never holds, across a rotation: the head is
// signed by the current version and verifies against the public key the
// descriptor records.
func TestLogService_SignHead_RecordsTheCheckpoint(t *testing.T) {
	hsm := newHSMProvider()
	desc := &vlog.Descriptor{Origin: "db.example.com/events", Hash: vlog.HashSHA256, Keys: []vlog.HeadKey{
		hsmKey(t, hsm, vlog.FirstKeyVersion), hsmKey(t, hsm, vlog.FirstKeyVersion+1),
	}}
	root := bytes.Repeat([]byte{4}, 32)
	host, cmds := signHeadHost(desc, &vlog.Head{Size: 3, Root: root})
	svc := newTestLogService(t, host, hsm)

	resp, err := svc.SignHead(acmeCtx(), &SignHeadRequest{LogRef: testLogRef})
	if err != nil {
		t.Fatalf("SignHead: %v", err)
	}
	pub, err := desc.Keys[1].Public()
	if err != nil {
		t.Fatalf("Public: %v", err)
	}
	if err := vlog.VerifyHead(pub, desc.Origin, resp.Head); err != nil {
		t.Fatalf("signed head does not verify: %v", err)
	}
	if err := desc.VerifyHead(resp.Head); err != nil || resp.Head.KeyVersion != 2 {
		t.Fatalf("Descriptor.VerifyHead = %v, key version %d; want version 2 accepted", err, resp.Head.KeyVersion)
	}
	if hsm.asked[0].KeyRef != "hsm:2" || hsm.asked[0].KeyVersion != 2 || hsm.asked[0].TenantID != "acme" {
		t.Fatalf("signer asked for %+v", hsm.asked[0])
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
	hsm := newHSMProvider()
	desc := &vlog.Descriptor{Origin: "o", Keys: []vlog.HeadKey{hsmKey(t, hsm, vlog.FirstKeyVersion)}}
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

	// A log with no head-signing key has nothing to sign with.
	host, _ = signHeadHost(&vlog.Descriptor{Origin: "o"}, head)
	svc = newTestLogService(t, host, hsm)
	var notFound *vlog.KeyNotFoundError
	if _, err := svc.SignHead(ctx, &SignHeadRequest{LogRef: testLogRef}); !errors.As(err, &notFound) {
		t.Errorf("no key: err = %v, want KeyNotFoundError", err)
	}

	// A signer over another key, or reporting another version, is refused
	// before its head is recorded.
	other := newHSMProvider()
	hsmKey(t, other, vlog.FirstKeyVersion)
	stale := newHSMProvider()
	hsmKey(t, stale, vlog.FirstKeyVersion)
	staleSigner, err := stale.HeadSigner(ctx, vlog.HeadSignerRequest{TenantID: "acme", Database: testLogRef.Database, Log: testLogRef.Log, KeyRef: "hsm:1"})
	if err != nil {
		t.Fatalf("HeadSigner: %v", err)
	}
	rotated := &vlog.Descriptor{Origin: "o", Keys: []vlog.HeadKey{desc.Keys[0], hsmKey(t, hsm, vlog.FirstKeyVersion+1)}}
	for name, c := range map[string]struct {
		desc    *vlog.Descriptor
		signers vlog.HeadSignerProvider
	}{
		"another key":     {desc, other},
		"another version": {rotated, &staticSignerProvider{signer: staleSigner}},
	} {
		h, recorded := signHeadHost(c.desc, head)
		if _, err := newTestLogService(t, h, c.signers).SignHead(ctx, &SignHeadRequest{LogRef: testLogRef}); err == nil {
			t.Errorf("%s: no error", name)
		}
		if len(*recorded) != 0 {
			t.Errorf("%s: proposed %d commands", name, len(*recorded))
		}
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
