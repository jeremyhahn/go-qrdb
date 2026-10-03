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
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newBarrierClusterUnsealCmd returns the `barrier cluster-unseal` subcommand:
// it clears a cluster-wide seal of the node barrier. It unseals nothing.
func newBarrierClusterUnsealCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cluster-unseal",
		Short: "Clear a cluster-wide seal of the node barrier",
		Long: `Clear a cluster-wide seal of the node barrier.

Run it, as the platform operator, on a node you have already unsealed. After a
cluster seal every node is restarted and unsealed at boot, and comes back held
-- keys loaded, requests refused -- until this command writes the clear record.
It unseals nothing by itself: every node still unseals with its own credential.

Tenant and database barriers have no such verb: unsealing them on a node clears
their record.

Example:
  qrdb barrier unseal --passphrase '<passphrase>'
  qrdb barrier cluster-unseal`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cs, err := client.Barrier().BarrierClusterUnseal(cmd.Context())
			if err != nil {
				return err
			}
			return renderClusterSealResult(cs)
		},
	}
}

// renderClusterSealResult prints a cluster seal state on its own, in the
// configured output format.
func renderClusterSealResult(cs *transport.ClusterSeal) error {
	switch outputFormat {
	case "json":
		return printJSON(cs)
	case "yaml":
		return printYAML(cs)
	default:
		printClusterSealLines(cs, "")
		return nil
	}
}

// printClusterSeal appends a barrier's cluster seal to a status table: one
// summary line, then the record's details when one exists.
func printClusterSeal(cs *transport.ClusterSeal) {
	printClusterSealLines(cs, "ClusterSeal:     ")
}

func printClusterSealLines(cs *transport.ClusterSeal, label string) {
	if cs == nil || !cs.Active {
		_, _ = fmt.Fprintf(outputWriter, "%snot active (seals are node-local)\n", label)
		return
	}
	state := "clear"
	if cs.Sealed {
		state = "sealed"
	}
	_, _ = fmt.Fprintf(outputWriter, "%s%s, epoch %d, this node: %s\n", label, state, cs.Epoch, cs.Local)
	if cs.Epoch == 0 {
		return
	}
	_, _ = fmt.Fprintf(outputWriter, "  Epoch:         %d\n", cs.Epoch)
	_, _ = fmt.Fprintf(outputWriter, "  By:            %s (%s)\n", cs.Principal, cs.PrincipalTenant)
	_, _ = fmt.Fprintf(outputWriter, "  Through:       %s\n", cs.Node)
	_, _ = fmt.Fprintf(outputWriter, "  At:            %s\n", cs.At)
	_, _ = fmt.Fprintf(outputWriter, "  This node:     %s\n", cs.Local)
}
