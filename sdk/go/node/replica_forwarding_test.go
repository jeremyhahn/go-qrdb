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

//go:build !integration && codec_json && raft

package node

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// TestReplicaForwardingOption: a clustered node composes forwarding for its
// listeners by default; turned off, standalone, or without a placement table
// it composes nothing; a bad timeout is the config's validation error.
func TestReplicaForwardingOption(t *testing.T) {
	ctx := context.Background()
	cfg := newRaftIsolationConfig(t)
	n, err := NewClusteredNode(ctx, cfg, raftMembers(), nil, WithNoSyncWrites())
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	placements := placementChangesOf(n.svcNode.Load())
	client := n.featureClient.Load()
	require.NotNil(t, placements)
	require.NotNil(t, client)

	opt, err := n.replicaForwardingOption(cfg, false, placements, client)
	require.NoError(t, err)
	require.NotNil(t, opt, "a clustered node with a placement table forwards by default")

	for name, tc := range map[string]struct {
		cfg        *qrdbconfig.Config
		standalone bool
		withTable  bool
	}{
		"standalone":   {cfg: cfg, standalone: true, withTable: true},
		"no placement": {cfg: cfg},
		"no config":    {withTable: true},
		"turned off":   {cfg: withForwarding(cfg, false, ""), withTable: true},
	} {
		table := placements
		if !tc.withTable {
			table = nil
		}
		require.Nil(t, placementChangesOf(nil))
		opt, err := n.replicaForwardingOption(tc.cfg, tc.standalone, table, client)
		require.NoError(t, err, name)
		require.Nil(t, opt, name)
	}

	_, err = n.replicaForwardingOption(withForwarding(cfg, true, "never"), false, placements, client)
	var ve *qrdbconfig.ValidationError
	require.True(t, errors.As(err, &ve), "a bad timeout = %v; want the config validation error", err)
}

// withForwarding is cfg with replica_forwarding set.
func withForwarding(cfg *qrdbconfig.Config, on bool, timeout string) *qrdbconfig.Config {
	c := *cfg
	c.ReplicaForwarding = qrdbconfig.ReplicaForwardingConfig{Enabled: &on, Timeout: timeout}
	return &c
}
