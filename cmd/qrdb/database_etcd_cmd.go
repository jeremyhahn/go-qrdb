//go:build qrdb_etcd

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
	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// addDatabaseUpdateEtcdCmd adds the "database update-etcd" subcommand to
// parent. Built only under qrdb_etcd. See database_etcd_cmd_stub.go for
// the !qrdb_etcd counterpart.
func addDatabaseUpdateEtcdCmd(parent *cobra.Command) {
	parent.AddCommand(newDatabaseUpdateEtcdCmd())
}

// newDatabaseUpdateEtcdCmd creates the "database update-etcd" subcommand.
func newDatabaseUpdateEtcdCmd() *cobra.Command {
	var (
		enabled       bool
		disabled      bool
		listenAddr    string
		advertiseURLs []string
	)

	cmd := &cobra.Command{
		Use:   "update-etcd <name>",
		Short: "Update etcd protocol configuration",
		Long:  "Update the etcd wire protocol configuration for a database.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			req := transport.UpdateDatabaseEtcdRequest{
				Name:                name,
				Enabled:             enabled,
				ListenAddr:          listenAddr,
				AdvertiseClientURLs: advertiseURLs,
			}

			info, err := client.Database().UpdateDatabaseEtcd(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printDatabaseInfo(info)
		},
	}

	cmd.Flags().BoolVar(&enabled, "enabled", false, "Enable etcd wire protocol")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "Disable etcd wire protocol")
	cmd.Flags().StringVar(&listenAddr, "listen-addr", "", "Etcd listen address")
	cmd.Flags().StringSliceVar(&advertiseURLs, "advertise-urls", nil, "Etcd advertise client URLs")
	cmd.MarkFlagsMutuallyExclusive("enabled", "disabled")

	return cmd
}

// addDatabaseCreateEtcdFlags registers the etcd flags on the "database
// create" command and returns a function that applies them to a
// CreateDatabaseRequest. Built only under qrdb_etcd. See
// database_etcd_cmd_stub.go for the !qrdb_etcd counterpart.
func addDatabaseCreateEtcdFlags(cmd *cobra.Command) func(*transport.CreateDatabaseRequest) {
	var (
		etcd              bool
		etcdListenAddr    string
		etcdAdvertiseURLs []string
	)

	cmd.Flags().BoolVar(&etcd, "etcd", false, "Enable etcd wire protocol compatibility")
	cmd.Flags().StringVar(&etcdListenAddr, "etcd-listen-addr", "localhost:2379", "Etcd listen address")
	cmd.Flags().StringSliceVar(&etcdAdvertiseURLs, "etcd-advertise-urls", nil, "Etcd advertise client URLs")

	return func(req *transport.CreateDatabaseRequest) {
		if etcd {
			req.Protocol.Etcd = &transport.EtcdProtocolConfig{
				Enabled:             true,
				ListenAddr:          etcdListenAddr,
				AdvertiseClientURLs: etcdAdvertiseURLs,
			}
		}
	}
}
