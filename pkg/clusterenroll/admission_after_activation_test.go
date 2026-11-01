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

// This file proves the fix for the admission bug end to end, across package
// boundaries: a node CSR-joins through clusterenroll.HandleJoinRequest with
// joinadvertise.Advertiser wired in, and pkg/cluster/featuregate.Gate --
// which never imports either package -- reads the record that produced and
// admits the joiner's address once a feature has activated. Without the
// advertisement, this exact scenario is the bug: AdmitReplica refuses with
// *featuregate.UnsupportedReplicaError because the node has no live record
// and cannot publish one for itself before it is a member.
package clusterenroll_test

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterca"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/joinadvertise"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// testBarrier is a minimal real-AES seal.EncryptionBarrier, standing in for
// "the replicated store, already unsealed" every admitted member reads the
// cluster CA (Key A) through.
type testBarrier struct{ key []byte }

func newTestBarrier(t *testing.T) *testBarrier {
	t.Helper()
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("generate barrier key: %v", err)
	}
	return &testBarrier{key: key}
}

func (b *testBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	// #nosec G407 -- NewGCMWithRandomNonce samples its own nonce; nil is required here.
	return gcm.Seal(nil, nil, plaintext, nil), nil
}

func (b *testBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nil, ciphertext, nil)
}

func (b *testBarrier) IsSealed() bool { return false }

// mvccAdvertisementStore implements cluster.MembershipStore by reading the
// SAME raw MVCC-protocol keyspace joinadvertise.Advertiser writes to --
// ListMembers via ListAll (which, like Advertiser's own write, is not
// filtered as an ordinary user key would be) plus a read per key, rather
// than through a second, parallel store a real deployment would never have.
// PutMember/DeleteMember are not used by this test and are refused.
type mvccAdvertisementStore struct {
	mvcc service.MVCCKVServicer
}

func (s *mvccAdvertisementStore) PutMember(context.Context, uint64, []byte, time.Duration) error {
	return errors.New("mvccAdvertisementStore: PutMember not supported; this store only reads back joinadvertise's writes")
}

func (s *mvccAdvertisementStore) DeleteMember(context.Context, uint64) error {
	return errors.New("mvccAdvertisementStore: DeleteMember not supported")
}

func (s *mvccAdvertisementStore) WatchMembers(context.Context) (<-chan cluster.MemberEvent, error) {
	// Gate.Start tolerates a watch failure (it logs and relies on periodic
	// refresh instead), which is all this test needs: it reads the ListMembers
	// seed taken at Start, written before Start ever runs.
	return nil, errors.New("mvccAdvertisementStore: watch not supported in this test")
}

func (s *mvccAdvertisementStore) ListMembers(ctx context.Context) ([]cluster.Member, error) {
	keys, err := s.mvcc.ListAll(ctx, featuregate.AdvertisementPrefix)
	if err != nil {
		return nil, err
	}
	members := make([]cluster.Member, 0, len(keys))
	for _, key := range keys {
		kv, err := s.mvcc.GetRevision(ctx, key)
		if err != nil {
			continue
		}
		var m cluster.Member
		if json.Unmarshal(kv.Value, &m) == nil {
			members = append(members, m)
		}
	}
	return members, nil
}

var _ cluster.MembershipStore = (*mvccAdvertisementStore)(nil)

// noGroupMembership is a featuregate.MembershipSource that knows no group:
// every GroupMembership call fails closed, which is exactly what decide()
// needs to see for the test shard so that only the pre-activated feature --
// not an incidental per-group agreement -- is what AdmitReplica's refusal or
// admission turns on.
type noGroupMembership struct{}

func (noGroupMembership) GroupMembership(uint64) (featuregate.GroupMembership, error) {
	return featuregate.GroupMembership{}, errors.New("no group membership known in this test")
}

// TestAdmission_JoinerIsAdmittedWhileAFeatureGateIsActive is the end-to-end
// proof: GuardedWrites is already activated cluster-wide (an activation
// record pre-exists, as it would on a running cluster), a brand-new node
// CSR-joins through HandleJoinRequest with joinadvertise.Advertiser wired
// in, and featuregate.Gate.AdmitReplica -- built independently, reading the
// SAME store -- admits the joiner's address. Flip the advertiser to nil (or
// comment out the activation record) and this test demonstrates the bug this
// change fixes instead: AdmitReplica refuses with
// *featuregate.UnsupportedReplicaError.
func TestAdmission_JoinerIsAdmittedWhileAFeatureGateIsActive(t *testing.T) {
	ctx := context.Background()
	const (
		systemShard = uint64(1)
		testShard   = uint64(42)
		joinerAddr  = "10.0.0.4:63001"
		joinerID    = uint64(4)
	)

	// --- Build one real, raft-free store (lease + MVCC) everything shares. ---
	stg, pdb, err := lifecycle.NewLocalStorage(database.BackendMemory, t.TempDir(), nil, qrdbconfig.StorageConfig{})
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	t.Cleanup(func() { _ = stg.Close() })
	lr := lease.NewLessor()
	eng, err := lifecycle.NewLocalEngine(database.BackendMemory, stg, pdb, nil, lr)
	if err != nil {
		t.Fatalf("NewLocalEngine: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	leaseSvc := service.NewLeaseService(host, 1)
	mvccSvc := service.NewMVCCKVService(host, 1)

	// --- The cluster already activated GuardedWrites (a pre-existing record,
	// as CheckActivated reads on every Gate.Start). ---
	activation, err := json.Marshal(struct {
		Feature   string `json:"feature"`
		ReplicaID uint64 `json:"activated_by"`
	}{Feature: string(featuregate.GuardedWrites), ReplicaID: 1})
	if err != nil {
		t.Fatalf("marshal activation record: %v", err)
	}
	if _, err := mvccSvc.PutIfAbsent(ctx, featuregate.ActivationPrefix+string(featuregate.GuardedWrites), activation); err != nil {
		t.Fatalf("PutIfAbsent activation record: %v", err)
	}

	// --- The joiner CSR-joins through the signing member's HandleJoinRequest,
	// with joinadvertise.Advertiser wired exactly as pkg/server/lifecycle and
	// pkg/server wire it in production. ---
	provider := clusterca.NewProvider(kvstoretest.NewGuardedMemoryStore(), newTestBarrier(t))
	if err := provider.Initialize(ctx); err != nil {
		t.Fatalf("Initialize cluster CA: %v", err)
	}
	tokenStore := kvstoretest.NewGuardedMemoryStore()
	const trustDomain = "test.cluster"
	token, _, err := clusterenroll.MintJoinToken(ctx, tokenStore, trustDomain, joinerAddr, time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate node key: %v", err)
	}
	// The CSR's sole URI SAN must match the request's self-reported NodeURI
	// (clusterenroll.validateJoinCSRIdentity), the same pairing a real
	// joiner's buildJoinCSR/Join always produces -- the joiner's actual
	// bound identity still comes from the token, derived from joinerAddr.
	claimedNodeURI := spiffe.NodeURI(trustDomain, joinerAddr)
	claimedURI, err := url.Parse(claimedNodeURI)
	if err != nil {
		t.Fatalf("parse claimed node URI: %v", err)
	}
	csrTemplate := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "qrdb-node"},
		URIs:    []*url.URL{claimedURI},
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, nodeKey)
	if err != nil {
		t.Fatalf("build CSR: %v", err)
	}
	advertiser := joinadvertise.New(leaseSvc, mvccSvc)
	if advertiser == nil {
		t.Fatal("joinadvertise.New returned nil with both services configured")
	}
	resp := clusterenroll.HandleJoinRequest(ctx, provider, tokenStore, trustDomain, &clusterenroll.JoinRequest{
		Token:     token,
		CSRDER:    csr,
		NodeURI:   claimedNodeURI,
		ReplicaID: joinerID,
		Features:  featuregate.SupportedNames(),
	}, advertiser)
	if resp.Error != "" {
		t.Fatalf("HandleJoinRequest refused: %s", resp.Error)
	}
	if len(resp.CertPEM) == 0 {
		t.Fatal("expected a certificate")
	}

	// --- A Gate, built independently -- it never imports clusterenroll or
	// joinadvertise -- reading the SAME store. ---
	gate, err := featuregate.New(featuregate.Config{
		Self:          featuregate.Self{ReplicaID: 1, Address: "10.0.0.1:63001"},
		SystemShardID: systemShard,
		Members:       noGroupMembership{},
	})
	if err != nil {
		t.Fatalf("featuregate.New: %v", err)
	}
	t.Cleanup(func() { _ = gate.Close() })
	if err := gate.Start(ctx, mvccSvc, &mvccAdvertisementStore{mvcc: mvccSvc}); err != nil {
		t.Fatalf("gate.Start: %v", err)
	}

	if !gate.Activated(featuregate.GuardedWrites) {
		t.Fatal("GuardedWrites should be activated: the activation record pre-existed")
	}

	// --- The proof: AdmitReplica, for the joiner's address, succeeds. ---
	if err := gate.AdmitReplica(testShard, joinerAddr); err != nil {
		t.Fatalf("AdmitReplica(%q) = %v, want nil (admitted) -- the pre-membership "+
			"advertisement HandleJoinRequest published should have made this node "+
			"admissible", joinerAddr, err)
	}

	// A DIFFERENT, never-advertised address is still refused: the fix makes
	// the joiner admissible, not every address.
	if err := gate.AdmitReplica(testShard, "10.0.0.99:63001"); err == nil {
		t.Fatal("AdmitReplica for an address nobody advertised should still refuse")
	}
}
