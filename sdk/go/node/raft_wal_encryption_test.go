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

//go:build !integration

package node

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// sealClustered turns a clustered config into a passphrase-sealed one.
func sealClustered(cfg *qrdbconfig.Config) *qrdbconfig.Config {
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "passphrase"
	cfg.Seal.RootKeyPath = "root-key"
	cfg.Seal.CredentialStrategy = "barrier"
	return cfg
}

// canary returns a value no WAL could contain by accident.
func canary(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return []byte("wal-plaintext-canary-" + hex.EncodeToString(b))
}

// walContains reports whether any file under dataDir/wal holds needle.
func walContains(t *testing.T, dataDir string, needle []byte) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(filepath.Join(dataDir, "wal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || found {
			return err
		}
		data, rerr := os.ReadFile(path) // #nosec G122 -- path comes from WalkDir over the test's own temp WAL directory; no symlink can race into it
		if rerr != nil {
			return rerr
		}
		found = bytes.Contains(data, needle)
		return nil
	})
	if err != nil {
		t.Fatalf("scan WAL: %v", err)
	}
	return found
}

// singleMemberClustered is a one-replica clustered node (no transport).
func singleMemberClustered(t *testing.T, sealed bool) (*Node, *qrdbconfig.Config) {
	t.Helper()
	ports, err := qrdbdharness.ReserveUDPPorts(1)
	if err != nil {
		t.Fatalf("ReserveUDPPorts: %v", err)
	}
	r := formationReplica{id: 1, addr: "127.0.0.1:" + strconv.Itoa(ports[0])}
	cfg := formationConfig(t, r, "")
	if sealed {
		sealClustered(cfg)
	}
	n, err := NewClusteredNode(context.Background(), cfg, map[uint64]string{1: r.addr}, nil)
	if err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	if sealed {
		if err := n.Unseal(context.Background(), seal.MustCredentials(context.Background(), []byte("wal-encryption-test"))); err != nil {
			t.Fatalf("Unseal: %v", err)
		}
	}
	return n, cfg
}

// TestClusteredNode_EncryptingBarrierKeepsKVOutOfTheWAL: a value written
// through a sealed SDK clustered node never reaches its Raft WAL in plaintext.
// The node used to build its host WithoutEncryption whatever the seal, so every
// committed key and value sat in cleartext in the WAL until compaction. The
// seal-disabled control proves the scan sees a plaintext WAL when there is
// one.
func TestClusteredNode_EncryptingBarrierKeepsKVOutOfTheWAL(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name      string
		sealed    bool
		wantFound bool
	}{
		{"sealed: encrypted under the barrier", true, false},
		{"seal disabled: plaintext control", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			n, cfg := singleMemberClustered(t, c.sealed)
			value := canary(t)
			ctx, cancel := context.WithTimeout(ctx, clusterFormationBudget)
			defer cancel()
			if err := putUntilCommitted(ctx, n.Client().KV(), "wal/probe", value); err != nil {
				t.Fatalf("Put: %v", err)
			}
			got, err := n.Client().KV().Get(ctx, "wal/probe")
			if err != nil || !bytes.Equal(got, value) {
				t.Fatalf("Get = %q, %v", got, err)
			}
			if found := walContains(t, cfg.Raft.DataDir, value); found != c.wantFound {
				t.Fatalf("plaintext value in WAL = %v, want %v", found, c.wantFound)
			}
		})
	}
}

// TestClusteredNode_ThreeSealedReplicasFormAndKeepKVOutOfTheWAL boots a
// three-replica sealed SDK cluster -- the multiplex path, a clustered host
// whose system barrier encrypts -- forms it, replicates a write to every
// replica, and finds the value in no replica's WAL.
func TestClusteredNode_ThreeSealedReplicasFormAndKeepKVOutOfTheWAL(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	caPEM := mustReadCAPEM(t, ca)
	members := make(map[uint64]string, len(replicas))
	for _, r := range replicas {
		members[r.id] = r.addr
	}

	nodes := make([]*Node, len(replicas))
	cfgs := make([]*qrdbconfig.Config, len(replicas))
	for i, r := range replicas {
		cfgs[i] = sealClustered(formationConfig(t, r, peerIdentitySpecs(t, r.id, replicas)))
		mtls := &RaftMTLSConfig{CACert: caPEM, Cert: r.cert.CertPEM, Key: r.cert.KeyPEM}
		n, err := NewClusteredNode(context.Background(), cfgs[i], members, nil, WithRaftMTLS(mtls))
		if err != nil {
			t.Fatalf("NewClusteredNode(%d): %v", r.id, err)
		}
		nodes[i] = n
		t.Cleanup(func() { _ = n.Close() })
	}

	// Unsealed together: each boot waits for the system shard's leader,
	// which needs a quorum of the others.
	errs := make([]error, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = n.Unseal(context.Background(), seal.MustCredentials(context.Background(), []byte("wal-encryption-test")))
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	leader := awaitAgreedLeader(t, nodes)
	ctx, cancel := context.WithTimeout(context.Background(), clusterFormationBudget)
	defer cancel()
	value := canary(t)
	if err := putUntilCommitted(ctx, nodes[leader-1].Client().KV(), "wal/probe", value); err != nil {
		t.Fatalf("write through leader %d: %v", leader, err)
	}
	for i, n := range nodes {
		got, err := n.Client().KV().Get(ctx, "wal/probe")
		if err != nil || !bytes.Equal(got, value) {
			t.Fatalf("replica %d read %q, %v", replicas[i].id, got, err)
		}
		if walContains(t, cfgs[i].Raft.DataDir, value) {
			t.Fatalf("replica %d's WAL holds the value in plaintext", replicas[i].id)
		}
	}
}

// TestClusteredNode_PlaintextEraWALIsRefusedUnderABarrier: a WAL directory
// written before this node encrypted its WAL (unmarked, with content) is not
// replayed under the barrier and not converted; the boot refuses with
// *database.RaftWALFormatError naming both formats.
func TestClusteredNode_PlaintextEraWALIsRefusedUnderABarrier(t *testing.T) {
	ports, err := qrdbdharness.ReserveUDPPorts(1)
	if err != nil {
		t.Fatalf("ReserveUDPPorts: %v", err)
	}
	r := formationReplica{id: 1, addr: "127.0.0.1:" + strconv.Itoa(ports[0])}
	cfg := sealClustered(formationConfig(t, r, ""))
	walShard := filepath.Join(cfg.Raft.DataDir, "wal", "shard-0")
	if err := os.MkdirAll(walShard, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(walShard, "00000000000000000001.log"), []byte("pre-0.25.1 plaintext segment"), 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := NewClusteredNode(context.Background(), cfg, map[uint64]string{1: r.addr}, nil)
	if err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	err = n.Unseal(context.Background(), seal.MustCredentials(context.Background(), []byte("wal-encryption-test")))
	var formatErr *database.RaftWALFormatError
	if !errors.As(err, &formatErr) {
		t.Fatalf("Unseal err = %T (%v), want *database.RaftWALFormatError", err, err)
	}
	if formatErr.Found != database.WALFormatPlaintext || formatErr.Want != database.WALFormatBarrier {
		t.Fatalf("refusal = %+v, want found plaintext, want barrier", formatErr)
	}
}
