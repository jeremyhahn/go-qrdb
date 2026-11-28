package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// clusterIsolationPolicy is the output shape of the cluster-isolation-policy
// verbs: the floor the server stored, as it reports it.
type clusterIsolationPolicy struct {
	Policy string `json:"policy" yaml:"policy"`
}

// newClusterIsolationPolicyCmd returns the `cluster-isolation-policy`
// command: it reads and sets the cluster-wide isolation floor.
func newClusterIsolationPolicyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster-isolation-policy",
		Short: "Read or set the cluster-wide tenant isolation floor",
		Long: `Read or set the cluster-wide tenant isolation floor.

The floor is operator-access (an operator may reach tenant data under the
usual permissions) or strict (no tenant's data is reachable by the operator).
A tenant whose own isolation policy is inherit follows the floor; a strict
tenant stays strict whatever the floor.

Both verbs are refused, and change nothing, until the strict-isolation cluster
feature is active.

Example:
  qrdb cluster-isolation-policy get
  qrdb cluster-isolation-policy set strict`,
	}
	cmd.AddCommand(newClusterIsolationPolicyGetCmd(), newClusterIsolationPolicySetCmd())
	return cmd
}

func newClusterIsolationPolicyGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get",
		Short: "Show the cluster-wide isolation floor",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			policy, err := client.Tenants().GetClusterIsolationPolicy(cmd.Context())
			if err != nil {
				return err
			}
			return renderClusterIsolationPolicy(policy)
		},
	}
}

func newClusterIsolationPolicySetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <operator-access|strict>",
		Short: "Set the cluster-wide isolation floor",
		Long: `Set the cluster-wide isolation floor to operator-access or strict. The
command prints the floor the server stored. Any other value is refused with
an invalid-isolation-policy error and the floor is left unchanged.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			policy, err := client.Tenants().SetClusterIsolationPolicy(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return renderClusterIsolationPolicy(policy)
		},
	}
}

func renderClusterIsolationPolicy(policy string) error {
	out := clusterIsolationPolicy{Policy: policy}
	switch outputFormat {
	case "json":
		return printJSON(out)
	case "yaml":
		return printYAML(out)
	default:
		_, err := fmt.Fprintf(outputWriter, "Cluster isolation policy: %s\n", policy)
		return err
	}
}
