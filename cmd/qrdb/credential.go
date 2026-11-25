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

// credential.go implements the canonical "credential" command group: the
// versioned named-credential operations (submit, store, retrieve, list,
// strategy, delete, rotate, versions, retrieve-version, gc), all wired through
// the SDK CredentialClient.
package main

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newCredentialCmd builds the canonical "credential" command group.
func newCredentialCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "credential",
		Short: "Named-credential operations (submit, store, retrieve, rotate, ...)",
	}
	cmd.AddCommand(
		newCredentialSubmitCmd(),
		newCredentialStoreCmd(),
		newCredentialRetrieveCmd(),
		newCredentialListCmd(),
		newCredentialStrategyCmd(),
		newCredentialDeleteCmd(),
		newCredentialRotateCmd(),
		newCredentialRotationPolicyCmd(),
		newCredentialMigrationConflictsCmd(),
		newCredentialVersionsCmd(),
		newCredentialRetrieveVersionCmd(),
		newCredentialGCCmd(),
	)
	return cmd
}

func newCredentialSubmitCmd() *cobra.Command {
	var value string
	cmd := &cobra.Command{
		Use:   "submit <name>",
		Short: "Submit a credential value (manual strategy)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.Credentials().CredentialSubmit(cmd.Context(),
				&transport.CredentialSubmitRequest{Name: args[0], Value: []byte(value)}); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}
	cmd.Flags().StringVar(&value, "value", "", "Credential value")
	_ = cmd.MarkFlagRequired("value")
	return cmd
}

func newCredentialStoreCmd() *cobra.Command {
	var value string
	cmd := &cobra.Command{
		Use:   "store <name>",
		Short: "Persist a credential value under the given name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.Credentials().CredentialStore(cmd.Context(), args[0], []byte(value)); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}
	cmd.Flags().StringVar(&value, "value", "", "Credential value")
	_ = cmd.MarkFlagRequired("value")
	return cmd
}

func newCredentialRetrieveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "retrieve <name>",
		Short: "Retrieve the current credential value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			buf, err := client.Credentials().CredentialRetrieveBuffer(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return printSecretBuffer(buf)
		},
	}
}

func newCredentialListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List stored credential names",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Credentials().CredentialList(cmd.Context(), &transport.CredentialListRequest{})
			if err != nil {
				return err
			}
			return printList(resp.Names, outputFormat)
		},
	}
}

func newCredentialStrategyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "strategy",
		Short: "Show the active credential strategy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Credentials().CredentialStrategy(cmd.Context())
			if err != nil {
				return err
			}
			printValue(resp.Strategy)
			return nil
		},
	}
}

func newCredentialDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a credential and its non-current versions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.Credentials().CredentialDelete(cmd.Context(), args[0]); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}
}

func newCredentialRotateCmd() *cobra.Command {
	var (
		value        string
		gracePeriodS int64
	)
	cmd := &cobra.Command{
		Use:   "rotate <name>",
		Short: "Rotate a credential to a new value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := client.Credentials().CredentialRotate(cmd.Context(), &transport.CredentialRotateRequest{
				Name:               args[0],
				NewValue:           []byte(value),
				GracePeriodSeconds: gracePeriodS,
			})
			if err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "rotated %s: previous v%d -> new v%d\n",
					resp.Name, resp.PreviousVersion, resp.NewVersion)
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&value, "value", "", "New credential value")
	_ = cmd.MarkFlagRequired("value")
	cmd.Flags().Int64Var(&gracePeriodS, "grace-period", 0, "Seconds the previous version remains retrievable (0 = wipe immediately)")
	return cmd
}

func newCredentialRotationPolicyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rotation-policy",
		Short: "Set or show a credential's automatic rotation policy",
	}
	cmd.AddCommand(
		newCredentialRotationPolicySetCmd(),
		newCredentialRotationPolicyGetCmd(),
	)
	return cmd
}

func newCredentialRotationPolicySetCmd() *cobra.Command {
	var (
		enabled        bool
		interval       string
		retainVersions int
	)
	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Set a credential's automatic rotation policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &transport.CredentialSetRotationPolicyRequest{
				Name:           args[0],
				Enabled:        enabled,
				Interval:       interval,
				RetainVersions: retainVersions,
			}
			if err := client.Credentials().CredentialSetRotationPolicy(cmd.Context(), req); err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(req)
			case "yaml":
				return printYAML(req)
			default:
				_, _ = fmt.Fprintf(outputWriter, "rotation policy for %s: enabled=%t interval=%s retain-versions=%d\n",
					req.Name, req.Enabled, req.Interval, req.RetainVersions)
				return nil
			}
		},
	}
	cmd.Flags().BoolVar(&enabled, "enabled", false, "Rotate the credential automatically on the interval")
	cmd.Flags().StringVar(&interval, "interval", "", "Time between rotations, as a Go duration (for example 720h)")
	cmd.Flags().IntVar(&retainVersions, "retain-versions", 0, "Versions kept after each rotation, the new one included")
	return cmd
}

func newCredentialRotationPolicyGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Show a credential's automatic rotation policy and its next rotation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := client.Credentials().CredentialRotationPolicy(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "rotation policy for %s: enabled=%t interval=%s retain-versions=%d",
					resp.Name, resp.Enabled, resp.Interval, resp.RetainVersions)
				if resp.NextRotation != "" {
					_, _ = fmt.Fprintf(outputWriter, " next-rotation=%s", resp.NextRotation)
				}
				_, _ = fmt.Fprintln(outputWriter)
				return nil
			}
		},
	}
}

func newCredentialMigrationConflictsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migration-conflicts",
		Short: "List or resolve this node's credential migration conflicts",
	}
	cmd.AddCommand(
		newCredentialMigrationConflictsListCmd(),
		newCredentialMigrationConflictsResolveCmd(),
	)
	return cmd
}

func newCredentialMigrationConflictsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the credentials whose node-local copy differs from the replicated record",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Credentials().CredentialMigrationConflicts(cmd.Context())
			if err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				for _, c := range resp.Conflicts {
					_, _ = fmt.Fprintf(outputWriter, "%s: value=%t versions=%t policy=%t\n",
						c.Name, c.Value, c.Versions, c.Policy)
				}
				return nil
			}
		},
	}
}

func newCredentialMigrationConflictsResolveCmd() *cobra.Command {
	var resolution string
	cmd := &cobra.Command{
		Use:   "resolve <name>",
		Short: "Resolve a credential migration conflict by importing or discarding the node-local copy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &transport.CredentialResolveMigrationConflictRequest{
				Name:       args[0],
				Resolution: resolution,
			}
			if err := client.Credentials().CredentialResolveMigrationConflict(cmd.Context(), req); err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(req)
			case "yaml":
				return printYAML(req)
			default:
				_, _ = fmt.Fprintf(outputWriter, "migration conflict for %s resolved: %s\n", req.Name, req.Resolution)
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&resolution, "resolution", "", "import (the node-local value becomes a new version) or discard (keep the replicated record)")
	_ = cmd.MarkFlagRequired("resolution")
	return cmd
}

func newCredentialVersionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "versions <name>",
		Short: "List the version history for a credential",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := client.Credentials().CredentialListVersions(cmd.Context(),
				&transport.CredentialListVersionsRequest{Name: args[0]})
			if err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				headers := []string{"VERSION", "CURRENT", "GRACE_UNTIL"}
				rows := make([][]string, len(resp.Versions))
				for i, v := range resp.Versions {
					rows[i] = []string{
						strconv.FormatUint(v.Version, 10),
						strconv.FormatBool(v.IsCurrent),
						strconv.FormatInt(v.GraceUntilUnix, 10),
					}
				}
				printTable(headers, rows)
				return nil
			}
		},
	}
}

func newCredentialRetrieveVersionCmd() *cobra.Command {
	var version uint64
	cmd := &cobra.Command{
		Use:   "retrieve-version <name>",
		Short: "Retrieve a specific credential version (0 = current)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			buf, err := client.Credentials().CredentialRetrieveVersionBuffer(cmd.Context(),
				&transport.CredentialRetrieveVersionRequest{Name: args[0], Version: version})
			if err != nil {
				return err
			}
			return printSecretBuffer(buf)
		},
	}
	cmd.Flags().Uint64Var(&version, "version", 0, "Version number to retrieve (0 means current)")
	return cmd
}

func newCredentialGCCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gc <name>",
		Short: "Reap expired historical versions of a credential",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := client.Credentials().CredentialGCExpired(cmd.Context(),
				&transport.CredentialGCRequest{Name: args[0]})
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(outputWriter, "reaped %d version(s)\n", resp.Reaped)
			return nil
		},
	}
}
