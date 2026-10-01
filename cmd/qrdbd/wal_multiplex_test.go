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

package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// systemBarrier returns the epoch barrier of a system barrier initialized
// with strategy, the way qrdbd holds one.
func systemBarrier(t *testing.T, strategy seal.SealingStrategy) *seal.EpochBarrier {
	t.Helper()
	b, err := seal.NewBarrier(slog.New(slog.NewTextHandler(io.Discard, nil)), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "test/root-key"}, strategy)
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}
	if err := b.Initialize(context.Background(), seal.MustCredentials([]byte("wal-multiplex-test"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return b.EpochBarrier()
}

// plaintextSystemBarrier is a system barrier under protection "none".
func plaintextSystemBarrier(t *testing.T) *seal.EpochBarrier {
	t.Helper()
	eb := systemBarrier(t, seal.NewNoneStrategy())
	if !eb.IsPlaintextMode() {
		t.Fatal("a none-strategy barrier is not in plaintext mode; the fixture is wrong")
	}
	return eb
}

// encryptingSystemBarrier is a system barrier under a passphrase.
func encryptingSystemBarrier(t *testing.T) *seal.EpochBarrier {
	t.Helper()
	strategy, err := seal.NewSoftwareStrategy([]byte("wal-multiplex-test"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	eb := systemBarrier(t, strategy)
	if eb.IsPlaintextMode() {
		t.Fatal("a software-strategy barrier reports plaintext mode; the fixture is wrong")
	}
	return eb
}

func clusterFlags(b *seal.EpochBarrier) *startFlags {
	return &startFlags{discoveryMethod: "static", peers: []string{"1=127.0.0.1:19001", "2=127.0.0.1:19002", "3=127.0.0.1:19003"}, walSystemBarrier: b}
}

// A clustered node whose system barrier encrypts uses the multiplex, so
// dedicated-WAL shards get their own tenant-barrier WAL.
func TestUseWALMultiplex_ClusterWithEncryptingBarrier(t *testing.T) {
	if !useWALMultiplex(clusterFlags(encryptingSystemBarrier(t))) {
		t.Fatal("an encrypting clustered node did not use the multiplex")
	}
}

// A clustered node whose system barrier is in plaintext mode (protection
// "none", before EnableEncryption) must not: its shared waldb honestly reports
// unprotected, and go-quicraft refuses that LogDB beside WithBarrier, so the
// node would never start (TestClusterEnableEncryption_*_RF3). A single node,
// and a node with no system barrier, never use it.
func TestUseWALMultiplex_RefusedWhereQuicraftWouldRefuse(t *testing.T) {
	plain := plaintextSystemBarrier(t)
	single := &startFlags{discoveryMethod: "static", peers: []string{"1=127.0.0.1:19001"}, walSystemBarrier: encryptingSystemBarrier(t)}
	for name, f := range map[string]*startFlags{
		"plaintext-mode cluster": clusterFlags(plain),
		"single node":            single,
		"no system barrier":      clusterFlags(nil),
	} {
		if useWALMultiplex(f) {
			t.Errorf("%s: useWALMultiplex = true", name)
		}
	}
}
