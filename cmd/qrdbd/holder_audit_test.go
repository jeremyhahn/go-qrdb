// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	secure "github.com/jeremyhahn/go-qrdb/sdk/go/secure"
)

// readTrail returns the events in the compliance audit file under dataDir,
// in order.
func readTrail(t *testing.T, dataDir string) []audit.AuditEvent {
	t.Helper()
	f, err := os.Open(filepath.Join(dataDir, complianceAuditFile))
	require.NoError(t, err, "the compliance audit trail exists")
	defer func() { _ = f.Close() }()
	var out []audit.AuditEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var ev audit.AuditEvent
		require.NoError(t, json.Unmarshal(sc.Bytes(), &ev))
		out = append(out, ev)
	}
	require.NoError(t, sc.Err())
	return out
}

// The inspection opt-out is the trail's first record.
func TestOpenComplianceAudit_RecordsTheOptOutFirst(t *testing.T) {
	dir := t.TempDir()
	sink, err := openComplianceAudit(context.Background(), dir, secure.ProcessInspectionAllowed)
	require.NoError(t, err)
	require.NoError(t, sink.Emit(context.Background(), audit.AuditEvent{Actor: "later", Resource: "r", Action: "a", Outcome: audit.OutcomeSuccess}))
	require.NoError(t, sink.Close())

	evs := readTrail(t, dir)
	require.Len(t, evs, 2)
	assert.Equal(t, inspectionAuditAction, evs[0].Action, "the opt-out is the first record")
	assert.Equal(t, "later", evs[1].Actor)
}

// A hardened node opens its trail with nothing written to it.
func TestOpenComplianceAudit_HardenedRecordsNothing(t *testing.T) {
	dir := t.TempDir()
	sink, err := openComplianceAudit(context.Background(), dir, secure.ProcessHardened)
	require.NoError(t, err)
	require.NoError(t, sink.Close())
	assert.Empty(t, readTrail(t, dir))
}

// A trail that cannot be opened fails start, as it did when the trail was
// opened after unseal: *AuthBootstrapError, step "compliance audit file".
func TestOpenComplianceAudit_UnopenableTrailFails(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, complianceAuditFile), 0o700))
	_, err := openComplianceAudit(context.Background(), dir, secure.ProcessInspectionAllowed)
	var abe *AuthBootstrapError
	require.True(t, errors.As(err, &abe), "got %T: %v", err, err)
	assert.Equal(t, "compliance audit file", abe.Step)
}

// sealedStartFlags are the flags of a single node with the system barrier
// enabled, whose first boot holds in the sealed window until an operator
// initializes it.
func sealedStartFlags(t *testing.T, dir string) (*startFlags, int) {
	t.Helper()
	adminPort, err := freePortOn(testBindHost, networkTCP, networkUDP)
	require.NoError(t, err)
	raftPort, err := freePortOn(raftListenHost, networkUDP)
	require.NoError(t, err)
	grpcPort, err := freePortOn(testBindHost, networkTCP)
	require.NoError(t, err)
	raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPort)
	return &startFlags{
		dataDir:         dir,
		smType:          "pebble",
		discoveryMethod: "static",
		peers:           []string{fmt.Sprintf("1=%s", raftAddr)},
		listen:          fmt.Sprintf("0.0.0.0:%d", raftPort),
		raftAddress:     raftAddr,
		shardID:         1,
		replicaID:       1,
		deploymentID:    1,
		adminPort:       adminPort,
		grpcPort:        grpcPort,
		enableREST:      true,
		sealConfig: qrdbconfig.SealConfig{
			Enabled:     true,
			RootKeyPath: "__sys:seal/root_key",
		},
		host:             testBindHost,
		processHardening: secure.ProcessInspectionAllowed,
	}, adminPort
}

// The compliance trail exists, with the inspection opt-out as its first
// record, while the node is still in its sealed window: the window's holder
// events -- claims, unseal quorums -- have somewhere to go. Before the trail
// moved ahead of the seal subsystem it was opened only after unseal, and a
// sealed node had no trail at all.
func TestRunStart_ComplianceTrailIsOpenInTheSealedWindow(t *testing.T) {
	dir := t.TempDir()
	writeTLSFixtures(t, dir)
	f, adminPort := sealedStartFlags(t, dir)

	done := make(chan error, 1)
	go func() { done <- runStart(f) }()
	guardDaemonShutdown(t, done)
	awaitReady(t, done, listenerReady(fmt.Sprintf("127.0.0.1:%d", adminPort)), readyDeadline(t))

	evs := readTrail(t, dir)
	require.NotEmpty(t, evs, "a node in its sealed window records on its trail")
	assert.Equal(t, inspectionAuditAction, evs[0].Action, "the opt-out is the trail's first record")

	if runErr := signalUntilReturn(t, done, syscall.SIGTERM); runErr != nil {
		t.Fatalf("runStart returned an error after a clean shutdown: %v", runErr)
	}
	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}

// A trail that cannot be opened stops start BEFORE the seal subsystem: no
// barrier is built and no sealed window is served. A node that ran its sealed
// window without a trail would gather unseal quorums off the record.
func TestRunStart_UnopenableTrailFailsBeforeTheSealedWindow(t *testing.T) {
	dir := t.TempDir()
	writeTLSFixtures(t, dir)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, complianceAuditFile), 0o700))
	f, _ := sealedStartFlags(t, dir)

	done := make(chan error, 1)
	go func() { done <- runStart(f) }()
	guardDaemonShutdown(t, done)
	var err error
	select {
	case err = <-done:
	case <-time.After(readinessTimeout):
		t.Fatalf("start did not fail within %v: it is serving its sealed window without a trail", readinessTimeout)
	}
	var abe *AuthBootstrapError
	require.True(t, errors.As(err, &abe), "got %T: %v", err, err)
	assert.Equal(t, "compliance audit file", abe.Step)
	_, statErr := os.Stat(filepath.Join(dir, "seal"))
	assert.True(t, errors.Is(statErr, os.ErrNotExist), "no seal store was opened: %v", statErr)
}

// daemonHolder is one system share holder's client side.
type daemonHolder struct {
	name     string
	key      *ecdsa.PrivateKey
	csr      []byte
	thumb    string
	envelope []byte
}

func newDaemonHolder(t *testing.T, name string) *daemonHolder {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: name}}, key)
	require.NoError(t, err)
	return &daemonHolder{name: name, key: key, csr: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})}
}

// An unseal quorum gathered in the daemon's sealed window is on the
// compliance trail the seal subsystem and awaitUnseal are given: one record
// per holder submission from the holder engine, and the barrier's own
// quorum-complete and unseal records -- each event from one emitter, keyed by
// the system tenant, carrying none of the shares, factor responses or keys.
func TestAwaitUnseal_UnsealQuorumIsAudited(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	f := &startFlags{
		dataDir:      dir,
		deploymentID: 1,
		sealConfig: qrdbconfig.SealConfig{
			Enabled:         true,
			Strategy:        "shamir",
			ShamirThreshold: 2,
			ShamirTotal:     3,
			RootKeyPath:     "__sys:seal/root_key",
		},
		host: testBindHost,
	}
	trail := audit.NewRingBuffer(64)
	sub, err := initSealSubsystem(f, ctx, trail)
	require.NoError(t, err)
	defer func() { _ = sub.Close() }()
	bundle, err := generateCATLS()
	require.NoError(t, err)
	ca, err := ceremonyCAFromBundle(bundle)
	require.NoError(t, err)

	// The first boot's ceremony: holders enrol, the barrier's unseal key is
	// split to them and each claims its envelope. The ceremony service is
	// carried into the sealed window, as an in-process migration carries it.
	svc, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: sub.SealDataDir}, sub.SealBarrier, slog.Default())
	require.NoError(t, err)
	svc.SetCA(ca)
	holders := []*daemonHolder{newDaemonHolder(t, "alice"), newDaemonHolder(t, "bob"), newDaemonHolder(t, "carol")}
	for _, h := range holders {
		res, err := svc.RegisterOfficer(ctx, seal.DomainUnseal, ceremony.RegisterOfficerRequest{Username: h.name, CSRPEM: h.csr})
		require.NoError(t, err)
		cert, err := mtls.ParseCertificatePEM(res.CertPEM)
		require.NoError(t, err)
		h.thumb = mtls.Thumbprint(cert.Raw)
	}
	_, err = sub.SealBarrier.InitializeShamir(ctx, "")
	require.NoError(t, err)
	for _, h := range holders {
		ch, err := svc.BeginClaimShare(ctx, seal.DomainUnseal, h.name, h.thumb)
		require.NoError(t, err)
		sig, err := ceremony.SignShareClaim(h.key, ceremony.ScopeSystem.ClaimLabel(), h.name, ch.Nonce)
		require.NoError(t, err)
		res, err := svc.CompleteClaimShare(ctx, seal.DomainUnseal, h.name, hex.EncodeToString(ch.Nonce), sig, h.thumb)
		require.NoError(t, err)
		h.envelope = res.Envelope
	}
	require.NoError(t, sub.SealBarrier.Seal())
	sealedAt := len(trail.Snapshot())

	sigCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		n, e := awaitUnseal(sigCtx, f, bundle, sub, ca, svc, trail)
		if n != nil {
			_ = n.Stop(context.Background())
		}
		done <- e
	}()
	// The window is open once awaitUnseal has handed the ceremony its trail.
	deadline := time.Now().Add(readinessTimeout)
	for svc.HolderDeps().Audit == nil {
		require.True(t, time.Now().Before(deadline), "the sealed window never installed the trail")
		time.Sleep(10 * time.Millisecond)
	}

	var secretMaterial [][]byte
	for _, h := range holders[:2] {
		ch, err := svc.BeginQuorum(ctx, seal.DomainUnseal, seal.OpUnseal, h.name, "")
		require.NoError(t, err)
		resp, err := mtls.SignChallenge(h.key, ch.ChallengeID, ch.MethodData[mtls.MethodDataPurpose], ch.Nonce)
		require.NoError(t, err)
		env, err := ceremony.ParseShareEnvelope(h.envelope)
		require.NoError(t, err)
		share, err := env.OpenWithKey(h.key)
		require.NoError(t, err)
		secretMaterial = append(secretMaterial, resp, append([]byte(nil), share...), h.envelope)
		_, err = sub.SealBarrier.SubmitHolderShare(ctx, &seal.HolderSubmission{
			Domain: seal.DomainUnseal, Op: seal.OpUnseal, Holder: h.name,
			Proof: seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: resp},
			Share: share,
		})
		require.NoError(t, err)
	}
	select {
	case err := <-done:
		require.NoError(t, err, "the quorum unsealed the barrier and the window closed")
	case <-time.After(readinessTimeout):
		t.Fatal("awaitUnseal did not return after the unseal quorum")
	}

	var submits []audit.AuditEvent
	byAction := map[string][]audit.AuditEvent{}
	for _, ev := range trail.Snapshot()[sealedAt:] {
		byAction[ev.Action] = append(byAction[ev.Action], ev)
		if ev.Action == ceremony.ActionHolderQuorumSubmit {
			submits = append(submits, ev)
		}
	}
	// The barrier's own records of the unseal, on the same trail: the round
	// completing and the barrier opening, once each. A submission is recorded
	// once, by the holder engine: the barrier emits no quorum-share of its own.
	for _, action := range []string{"quorum-complete", "unseal"} {
		require.Len(t, byAction[action], 1, action)
		ev := byAction[action][0]
		assert.Equal(t, tenantid.System, ev.TenantID, action)
		assert.Equal(t, seal.BarrierAuditResource, ev.Resource, action)
		assert.Equal(t, seal.SystemBarrierAuditName, ev.Attributes["barrier"], action)
		assert.Equal(t, audit.OutcomeSuccess, ev.Outcome, action)
	}
	assert.Equal(t, string(seal.OpUnseal), byAction["quorum-complete"][0].Attributes["op"])
	assert.Empty(t, byAction["quorum-share"], "a submission has one emitter")
	require.Len(t, submits, 2, "each holder's unseal submission is recorded")
	for i, ev := range submits {
		assert.Equal(t, tenantid.System, ev.TenantID)
		assert.Equal(t, ceremony.HolderAuditResource, ev.Resource)
		assert.Equal(t, holders[i].name, ev.Attributes["holder"])
		assert.Equal(t, holders[i].name, ev.Actor)
		assert.Equal(t, string(seal.OpUnseal), ev.Attributes["op"])
		assert.Equal(t, "system", ev.Attributes["scope"])
		assert.Equal(t, audit.OutcomeSuccess, ev.Outcome)
	}
	raw, err := json.Marshal(trail.Snapshot())
	require.NoError(t, err)
	for _, s := range secretMaterial {
		for _, enc := range []string{string(s), hex.EncodeToString(s), base64.StdEncoding.EncodeToString(s), base64.RawURLEncoding.EncodeToString(s)} {
			assert.False(t, strings.Contains(string(raw), enc), "the trail carries secret material")
		}
	}
	for _, h := range holders {
		der, err := x509.MarshalPKCS8PrivateKey(h.key)
		require.NoError(t, err)
		assert.False(t, strings.Contains(string(raw), base64.StdEncoding.EncodeToString(der)), "the trail carries a holder key")
	}
}
