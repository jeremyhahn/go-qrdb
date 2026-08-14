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
	"sort"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
)

// peersResponse is the JSON payload returned by GET /peers.
type peersResponse struct {
	Shards []shardPeers `json:"shards"`
}

// shardPeers describes the peers for a single shard.
type shardPeers struct {
	ShardID  uint64     `json:"shard_id"`
	LeaderID uint64     `json:"leader_id"`
	Peers    []peerInfo `json:"peers"`
}

// peerInfo describes a single peer in a shard.
type peerInfo struct {
	ReplicaID uint64 `json:"replica_id"`
	Address   string `json:"address"`
	IsLeader  bool   `json:"is_leader"`
}

func newPeersCmd() *cobra.Command {
	var address string
	var tlsCAFile string
	var tlsCertFile string
	var tlsKeyFile string
	cmd := &cobra.Command{
		Use:   "peers",
		Short: "List known peers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			pool, err := loadCACertPool(tlsCAFile)
			if err != nil {
				return err
			}

			url := "https://" + address + "/peers"
			var resp peersResponse
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

			w := cmd.OutOrStdout()
			for _, sp := range resp.Shards {
				_, _ = fmt.Fprintf(w, "Shard %d (leader: replica %d):\n", sp.ShardID, sp.LeaderID)

				// Sort peers by replica ID for stable output.
				sort.Slice(sp.Peers, func(i, j int) bool {
					return sp.Peers[i].ReplicaID < sp.Peers[j].ReplicaID
				})

				for _, p := range sp.Peers {
					marker := "  "
					if p.IsLeader {
						marker = "* "
					}
					_, _ = fmt.Fprintf(w, "  %sreplica %d  %s\n", marker, p.ReplicaID, p.Address)
				}
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
