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
	"crypto/tls"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
)

// statusResponse is the JSON payload returned by GET /status. Field names
// and types must match the REST server's statusResponse in handlers.go.
type statusResponse struct {
	NodeID      string        `json:"node_id"`
	RaftAddress string        `json:"raft_address"`
	IsLeader    bool          `json:"is_leader"`
	LeaderID    uint64        `json:"leader_id"`
	ShardCount  int           `json:"shard_count"`
	IsDraining  bool          `json:"is_draining"`
	Shards      []shardStatus `json:"shards"`
}

// shardStatus describes a single shard's status in the CLI status response.
type shardStatus struct {
	ShardID     uint64            `json:"shard_id"`
	ReplicaID   uint64            `json:"replica_id"`
	LeaderID    uint64            `json:"leader_id"`
	Term        uint64            `json:"term"`
	IsLeader    bool              `json:"is_leader"`
	Health      healthValue       `json:"health"`
	LastApplied uint64            `json:"last_applied"`
	Nodes       map[uint64]string `json:"nodes"`
}

// healthValue wraps a health status string for display formatting.
type healthValue string

// String returns the health status string.
func (h healthValue) String() string {
	return string(h)
}

func newStatusCmd() *cobra.Command {
	var address string
	var tlsCAFile string
	var tlsCertFile string
	var tlsKeyFile string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show cluster/shard status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			pool, err := loadCACertPool(tlsCAFile)
			if err != nil {
				return err
			}

			url := "https://" + address + "/status"
			var resp statusResponse
			if tlsCertFile != "" && tlsKeyFile != "" {
				cert, cerr := tls.LoadX509KeyPair(tlsCertFile, tlsKeyFile)
				if cerr != nil {
					return cerr
				}
				err = rest.FetchJSONWithMTLS(url, &resp, pool, cert)
			} else {
				err = rest.FetchJSON(url, &resp, pool)
			}
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Node ID:      %s\n", resp.NodeID)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Raft Address: %s\n", resp.RaftAddress)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Shard Count:  %d\n", resp.ShardCount)

			for _, si := range resp.Shards {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nShard %d:\n", si.ShardID)
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Replica ID:   %d\n", si.ReplicaID)
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Leader ID:    %d\n", si.LeaderID)
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Term:         %d\n", si.Term)
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Is Leader:    %t\n", si.IsLeader)
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Health:       %s\n", si.Health.String())
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Last Applied: %d\n", si.LastApplied)
			}

			return nil
		},
	}
	cmd.Flags().StringVar(&address, "address", "127.0.0.1:63002", "Admin server address to query")
	cmd.Flags().StringVar(&tlsCAFile, "tls-ca-file", "", "Path to CA certificate PEM file for server verification")
	cmd.Flags().StringVar(&tlsCertFile, "tls-cert-file", "", "Path to client TLS certificate PEM file for mTLS authentication")
	cmd.Flags().StringVar(&tlsKeyFile, "tls-key-file", "", "Path to client TLS private key PEM file for mTLS authentication")
	return cmd
}
