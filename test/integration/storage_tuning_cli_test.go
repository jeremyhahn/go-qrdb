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

//go:build integration

package integration_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// TestStorageTuning_CLI_WALDirTakesTheWAL boots a standalone qrdbd on the
// pebble backend with storage.wal_dir, storage.cache_size and
// storage.max_open_files set, writes and reads a key over every transport,
// and shows the configuration took effect: Pebble's write-ahead log is in
// wal_dir and none is under the data directory.
func TestStorageTuning_CLI_WALDirTakesTheWAL(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "wal")
	extra := fmt.Sprintf("storage:\n  wal_dir: %q\n  cache_size: 33554432\n  max_open_files: 256\n", walDir)
	node := startStandaloneModeNodeWithConfig(t, "pebble", extra)

	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			key := "wal-dir-" + proto.name
			if out, err := runCLIProto(t, node, proto, "kv", "put", key, "v-"+proto.name); err != nil {
				t.Fatalf("kv put: %v\n%s", err, out)
			}
			out, err := runCLIProto(t, node, proto, "kv", "get", key)
			if err != nil {
				t.Fatalf("kv get: %v\n%s", err, out)
			}
			if !strings.Contains(out, "v-"+proto.name) {
				t.Fatalf("kv get output %q does not contain the stored value", out)
			}
		})
	}

	inWALDir, err := filepath.Glob(filepath.Join(walDir, "*.log"))
	if err != nil || len(inWALDir) == 0 {
		t.Fatalf("no Pebble WAL file in storage.wal_dir %s (err %v)", walDir, err)
	}
	underData, err := filepath.Glob(filepath.Join(node.dataDir, "pebble", "*.log"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(underData) != 0 {
		t.Fatalf("Pebble WAL files %v under the data directory; storage.wal_dir was not honoured", underData)
	}
}
