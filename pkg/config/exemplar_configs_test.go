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

package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/config"
)

// TestExemplarConfigsLoadAndValidate pins every YAML file under configs/
// against the real loader and validator, so the documented exemplars (the
// run-mode x state-machine matrix in configs/README.md) cannot silently
// drift from the config schema as it evolves. A new exemplar is covered
// automatically by being added to the directory.
func TestExemplarConfigsLoadAndValidate(t *testing.T) {
	dir := filepath.Join("..", "..", "configs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read configs dir: %v", err)
	}

	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
			continue
		}
		path := filepath.Join(dir, name)
		t.Run(name, func(t *testing.T) {
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("validate: %v", err)
			}
		})
		checked++
	}
	if checked == 0 {
		t.Fatal("no exemplar configs found — configs/ moved or emptied?")
	}
}

// TestExemplarClusterConfigsAgreeOnMembership verifies the 3-node cluster
// exemplars declare the identical founding member set with distinct replica
// IDs — the property the per-file comments promise and a copy-paste
// operator depends on.
func TestExemplarClusterConfigsAgreeOnMembership(t *testing.T) {
	var peers []string
	seen := map[uint64]bool{}
	for i := 1; i <= 3; i++ {
		path := filepath.Join("..", "..", "configs",
			"qrdb-cluster-node"+string(rune('0'+i))+".yaml")
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("%s: load: %v", path, err)
		}
		if seen[cfg.Raft.ReplicaID] {
			t.Fatalf("%s: duplicate replica_id %d", path, cfg.Raft.ReplicaID)
		}
		seen[cfg.Raft.ReplicaID] = true
		peers = append(peers, cfg.Raft.Peers)
	}
	if peers[0] != peers[1] || peers[1] != peers[2] {
		t.Fatalf("cluster exemplars disagree on the founding member set: %q / %q / %q",
			peers[0], peers[1], peers[2])
	}
}
