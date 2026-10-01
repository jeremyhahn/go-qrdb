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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
)

// TestInitializeNode_RaftPortZeroRefused pins the daemon's half of the Raft
// port-0 refusal: a multi-node node naming port 0 in --raft-address or
// --listen is refused before any host directory is created.
func TestInitializeNode_RaftPortZeroRefused(t *testing.T) {
	for _, tc := range []struct {
		field  string
		mutate func(f *startFlags)
	}{
		{field: "--raft-address", mutate: func(f *startFlags) { f.raftAddress = "127.0.0.1:0" }},
		{field: "--listen", mutate: func(f *startFlags) { f.listen = "127.0.0.1:0" }},
	} {
		t.Run(tc.field, func(t *testing.T) {
			f := &startFlags{
				smType:          "memory",
				discoveryMethod: "static",
				peers:           []string{"1=127.0.0.1:63911", "2=127.0.0.2:63001"},
				dataDir:         t.TempDir(),
				listen:          "127.0.0.1:63911",
				raftAddress:     "127.0.0.1:63911",
				shardID:         1,
				replicaID:       1,
				deploymentID:    1,
				logFormat:       "text",
				logLevel:        "error",
				host:            testBindHost,
			}
			tc.mutate(f)
			_, err := initializeNode(context.Background(), f, nil)
			var zero *cluster.RaftPortZeroError
			require.ErrorAs(t, err, &zero)
			require.Equal(t, tc.field, zero.Field)
			_, statErr := os.Stat(filepath.Join(f.dataDir, "wal"))
			require.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
}
