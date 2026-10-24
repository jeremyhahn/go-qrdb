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
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func newABACCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "abac",
		Short: "Attribute-based access control (policy)",
	}
	cmd.AddCommand(newABACPolicyCmd())
	return cmd
}

func newABACPolicyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Stored ABAC policy documents (create, get, list, update, delete)",
		Long: "Manage the caller's tenant's stored ABAC policy documents. Every command acts on\n" +
			"the caller's own tenant; --tenant may only name that tenant.",
	}
	cmd.PersistentFlags().String("tenant", "", "Tenant ID (must be the caller's own; default: the caller's tenant)")
	cmd.AddCommand(
		newABACPolicyCreateCmd(),
		newABACPolicyGetCmd(),
		newABACPolicyListCmd(),
		newABACPolicyUpdateCmd(),
		newABACPolicyDeleteCmd(),
	)
	return cmd
}

func newABACPolicyCreateCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a policy from a YAML or JSON document (--file)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			policy, err := loadABACPolicy(cmd, file)
			if err != nil {
				return err
			}
			created, err := client.ABACPolicies().CreateABACPolicy(cmd.Context(), policy)
			if err != nil {
				return err
			}
			return printABACPolicy(created)
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to the policy document (one YAML or JSON document)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func newABACPolicyGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Show a policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			policy, err := client.ABACPolicies().GetABACPolicy(cmd.Context(),
				&transport.ABACPolicyRequest{TenantID: abacPolicyTenant(cmd), Name: args[0]})
			if err != nil {
				return err
			}
			return printABACPolicy(policy)
		},
	}
}

func newABACPolicyListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the tenant's policies",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.ABACPolicies().ListABACPolicies(cmd.Context(),
				&transport.ABACPolicyListRequest{TenantID: abacPolicyTenant(cmd)})
			if err != nil {
				return err
			}
			return printABACPolicyList(resp.Policies)
		},
	}
}

func newABACPolicyUpdateCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Replace a policy with a YAML or JSON document (--file); the name selects it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			policy, err := loadABACPolicy(cmd, file)
			if err != nil {
				return err
			}
			updated, err := client.ABACPolicies().UpdateABACPolicy(cmd.Context(), policy)
			if err != nil {
				return err
			}
			return printABACPolicy(updated)
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to the policy document (one YAML or JSON document)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func newABACPolicyDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.ABACPolicies().DeleteABACPolicy(cmd.Context(),
				&transport.ABACPolicyRequest{TenantID: abacPolicyTenant(cmd), Name: args[0]}); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(outputWriter, "Deleted policy %s\n", args[0])
			return nil
		},
	}
}

// abacPolicyTenant returns the --tenant flag; empty selects the caller's
// tenant on the server.
func abacPolicyTenant(cmd *cobra.Command) string {
	tenantID, err := cmd.Flags().GetString("tenant")
	if err != nil {
		return ""
	}
	return tenantID
}

// loadABACPolicy decodes the one policy document at path. YAML is a
// superset of JSON, so either form reads. The document is not validated
// here: the server fills metadata.tenant with the caller's tenant before
// it validates, so a document that omits the tenant is complete. --tenant
// fills an empty metadata.tenant and is refused when the document names
// another.
func loadABACPolicy(cmd *cobra.Command, path string) (*sdk.ABACPolicy, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-supplied --file path
	if err != nil {
		return nil, &ABACPolicyFileError{Path: path, Err: err}
	}
	var policies []*sdk.ABACPolicy
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var policy sdk.ABACPolicy
		if err := dec.Decode(&policy); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, &ABACPolicyFileError{Path: path, Err: err}
		}
		policies = append(policies, &policy)
	}
	if len(policies) != 1 {
		return nil, &InvalidArgError{Arg: "file", Reason: "must hold exactly one policy document, found " + strconv.Itoa(len(policies))}
	}
	policy := policies[0]
	tenantID := abacPolicyTenant(cmd)
	switch {
	case tenantID == "":
	case policy.Metadata.Tenant == "":
		policy.Metadata.Tenant = tenantID
	case policy.Metadata.Tenant != tenantID:
		return nil, &InvalidArgError{Arg: "tenant", Reason: "the document names tenant " + policy.Metadata.Tenant}
	}
	return policy, nil
}

// ABACPolicyFileError is returned when the --file policy document cannot
// be read or decoded.
type ABACPolicyFileError struct {
	Path string
	Err  error
}

func (e *ABACPolicyFileError) Error() string {
	return "abac policy file " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

func (e *ABACPolicyFileError) Unwrap() error { return e.Err }

// printABACPolicy prints one document whole as JSON or YAML, or its
// summary row as a table.
func printABACPolicy(policy *sdk.ABACPolicy) error {
	switch outputFormat {
	case "json":
		return printJSON(policy)
	case "yaml":
		return printYAML(policy)
	}
	printABACPolicyTable([]*sdk.ABACPolicy{policy})
	return nil
}

// printABACPolicyList prints the documents as one JSON or YAML array --
// empty, never null, when the tenant has none -- or a summary table.
func printABACPolicyList(policies []*sdk.ABACPolicy) error {
	if policies == nil {
		policies = []*sdk.ABACPolicy{}
	}
	switch outputFormat {
	case "json":
		return printJSON(policies)
	case "yaml":
		return printYAML(policies)
	}
	printABACPolicyTable(policies)
	return nil
}

func printABACPolicyTable(policies []*sdk.ABACPolicy) {
	rows := make([][]string, len(policies))
	for i, p := range policies {
		rows[i] = []string{
			p.Metadata.Name, p.Metadata.Tenant,
			strconv.FormatBool(p.Spec.IsEnabled()),
			strconv.Itoa(p.Metadata.Priority),
			strconv.Itoa(len(p.Spec.Rules)),
			p.Metadata.Updated.UTC().Format("2006-01-02T15:04:05Z"),
		}
	}
	printTable([]string{"NAME", "TENANT", "ENABLED", "PRIORITY", "RULES", "UPDATED"}, rows)
}
