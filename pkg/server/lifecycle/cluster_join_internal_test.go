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

package lifecycle

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/url"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// joinTestLeaf returns a self-signed leaf, carrying nodeURI as its URI SAN
// when it is not empty.
func joinTestLeaf(t *testing.T, nodeURI string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "qrdb-node"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	if nodeURI != "" {
		u, err := url.Parse(nodeURI)
		if err != nil {
			t.Fatal(err)
		}
		tmpl.URIs = []*url.URL{u}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestRecordOwnJoinIdentity_FounderRefusesSubsequentMint is the coordinator-
// required proof: a founder's own leaf (the certificate confirmClusterCAPersisted's
// caller already holds at boot) gets its SPIFFE node identity recorded, and
// MintJoinToken for that SAME node ID is refused afterward -- "existing
// member" now means "the cluster CA has issued this node identity",
// covering a founder that never went through the token path at all.
func TestRecordOwnJoinIdentity_FounderRefusesSubsequentMint(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	const trustDomain = "test.cluster"
	const nodeID = "founder-1"
	certPEM := joinTestLeaf(t, spiffe.NodeURI(trustDomain, nodeID))

	if err := recordOwnJoinIdentity(context.Background(), store, certPEM, trustDomain); err != nil {
		t.Fatalf("recordOwnJoinIdentity: %v", err)
	}

	_, _, err := clusterenroll.MintJoinToken(context.Background(), store, trustDomain, nodeID, time.Hour)
	var existing *clusterenroll.ExistingMemberError
	if !errors.As(err, &existing) {
		t.Fatalf("MintJoinToken for a founder's ID = %v (%T), want *clusterenroll.ExistingMemberError", err, err)
	}
}

// TestRecordOwnJoinIdentity_IdempotentAcrossBoots proves the call is safe
// on every boot, not only the first: recordOwnJoinIdentity is called
// unconditionally after confirmClusterCAPersisted on every start, and a
// restart presenting the SAME leaf must not error just because the
// record already exists (PutIfAbsent's ordinary "already there" outcome).
func TestRecordOwnJoinIdentity_IdempotentAcrossBoots(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	const trustDomain = "test.cluster"
	certPEM := joinTestLeaf(t, spiffe.NodeURI(trustDomain, "founder-1"))

	if err := recordOwnJoinIdentity(context.Background(), store, certPEM, trustDomain); err != nil {
		t.Fatalf("recordOwnJoinIdentity (first boot): %v", err)
	}
	if err := recordOwnJoinIdentity(context.Background(), store, certPEM, trustDomain); err != nil {
		t.Fatalf("recordOwnJoinIdentity (restart, same leaf) = %v, want nil", err)
	}
}

// TestRecordOwnJoinIdentity_FreshIDStillMintable proves recording one
// node's identity does not block minting a token for a DIFFERENT, fresh
// node ID.
func TestRecordOwnJoinIdentity_FreshIDStillMintable(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	const trustDomain = "test.cluster"
	certPEM := joinTestLeaf(t, spiffe.NodeURI(trustDomain, "founder-1"))

	if err := recordOwnJoinIdentity(context.Background(), store, certPEM, trustDomain); err != nil {
		t.Fatalf("recordOwnJoinIdentity: %v", err)
	}

	token, _, err := clusterenroll.MintJoinToken(context.Background(), store, trustDomain, "fresh-node", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken for a fresh node ID = %v, want success", err)
	}
	if token == "" {
		t.Fatal("MintJoinToken returned an empty token")
	}
}

// TestRecordOwnJoinIdentity_NoNodeSANIsNoOp documents the BYO-TLS gap: a
// certificate with no node SPIFFE URI SAN at all (operator-supplied
// --tls-ca/--tls-cert/--tls-key, signed outside the cluster CA) has no
// identity for this function to record. It must not error -- there was
// never an identity to admit -- and must leave the ledger untouched.
func TestRecordOwnJoinIdentity_NoNodeSANIsNoOp(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	certPEM := joinTestLeaf(t, "")

	if err := recordOwnJoinIdentity(context.Background(), store, certPEM, "test.cluster"); err != nil {
		t.Fatalf("recordOwnJoinIdentity (no SAN) = %v, want nil", err)
	}
	keys, err := store.List(context.Background(), "__sys:cluster_join_identities/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected no identity recorded for a cert with no node SPIFFE SAN, got %v", keys)
	}
}

// TestRecordOwnJoinIdentity_MalformedCertIsNoOp proves a certificate that
// fails to decode or parse is treated the same as "no identity to
// record" -- confirmClusterCAPersisted already validated this node's own
// certificate through the cluster CA provider before this function runs,
// so a malformed certPEM here would mean a caller bug, not an admission
// decision; it must not block boot.
func TestRecordOwnJoinIdentity_MalformedCertIsNoOp(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	if err := recordOwnJoinIdentity(context.Background(), store, []byte("not a certificate"), "test.cluster"); err != nil {
		t.Fatalf("recordOwnJoinIdentity (malformed cert) = %v, want nil", err)
	}
}

// refusingStore is a join store that refuses every write.
type refusingStore struct {
	kvstore.AtomicKVStore
}

var errStoreRefused = errors.New("store refused")

func (refusingStore) Put(context.Context, string, []byte) error { return errStoreRefused }

func (refusingStore) PutIfAbsent(context.Context, string, []byte) (bool, error) {
	return false, errStoreRefused
}

func (refusingStore) CompareAndSwap(context.Context, string, []byte, []byte) (bool, error) {
	return false, errStoreRefused
}

// A store that refuses the write fails the record with the typed error, and
// a nil join stops as a no-op.
func TestRecordOwnJoinIdentity_StoreRefusalIsTyped(t *testing.T) {
	certPEM := joinTestLeaf(t, spiffe.NodeURI("test.cluster", "founder-1"))
	err := recordOwnJoinIdentity(context.Background(), refusingStore{kvstore.NewMemoryKVStore()}, certPEM, "test.cluster")
	var recErr *JoinIdentityRecordError
	if !errors.As(err, &recErr) || !errors.Is(err, errStoreRefused) || recErr.Error() == "" {
		t.Fatalf("recordOwnJoinIdentity = %v; want *JoinIdentityRecordError wrapping the refusal", err)
	}
	var j *ClusterJoin
	j.Stop()
}
