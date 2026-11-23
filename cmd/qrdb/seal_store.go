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

// seal_store.go implements the "seal-store" command group: the node seal
// store of named secrets (put, get, list, delete, reseal, status), wired
// through the SDK SealStoreClient. A secret is read from stdin, never from
// the command line, so it never shows in a process list.
package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// SealStoreEmptySecretError reports a put whose stdin carried no secret.
type SealStoreEmptySecretError struct {
	Name string
}

// Error implements the error interface.
func (e *SealStoreEmptySecretError) Error() string {
	return fmt.Sprintf("seal-store put %q: empty secret on stdin", e.Name)
}

// sealStoreNameResult is the json/yaml form of put, delete and reseal.
type sealStoreNameResult struct {
	Name string `json:"name" yaml:"name"`
}

// newSealStoreCmd builds the "seal-store" command group.
func newSealStoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "seal-store",
		Short: "Node seal store of named secrets (put, get, list, delete, reseal, status)",
		Long: "Operate the node's seal store of named secrets. The caller must hold\n" +
			"seal:store_manage, a system-tenant Security Officer grant. The store is\n" +
			"node-local and embedded-only (--transport embedded, desktop/GUI build);\n" +
			"every network transport refuses the verbs with an embedded-only error\n" +
			"(ruling 1234). Seal and unseal stay on every transport: qrdb barrier seal|unseal.",
	}
	cmd.AddCommand(
		newSealStorePutCmd(),
		newSealStoreGetCmd(),
		newSealStoreListCmd(),
		newSealStoreDeleteCmd(),
		newSealStoreResealCmd(),
		newSealStoreStatusCmd(),
	)
	return cmd
}

func newSealStorePutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "put <name>",
		Short: "Seal the secret read from stdin under <name>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			secret, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return err
			}
			if len(secret) == 0 {
				return &SealStoreEmptySecretError{Name: args[0]}
			}
			if err := client.SealStore().SealStorePut(cmd.Context(),
				&transport.SealStorePutRequest{Name: args[0], Secret: secret}); err != nil {
				return err
			}
			return printSealStoreName("stored", args[0])
		},
	}
}

func newSealStoreGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Unseal and print the secret stored under <name>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := client.SealStore().SealStoreGet(cmd.Context(), &transport.SealStoreGetRequest{Name: args[0]})
			if err != nil {
				return err
			}
			return printDEKData(resp, string(resp.Secret))
		},
	}
}

func newSealStoreListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the stored secret names",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.SealStore().SealStoreList(cmd.Context())
			if err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			}
			for _, name := range resp.Names {
				_, _ = fmt.Fprintln(outputWriter, name)
			}
			return nil
		},
	}
}

func newSealStoreDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete the secret stored under <name>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.SealStore().SealStoreDelete(cmd.Context(),
				&transport.SealStoreDeleteRequest{Name: args[0]}); err != nil {
				return err
			}
			return printSealStoreName("deleted", args[0])
		},
	}
}

func newSealStoreResealCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reseal <name>",
		Short: "Re-seal the secret stored under <name> with the current sealing key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.SealStore().SealStoreReseal(cmd.Context(),
				&transport.SealStoreResealRequest{Name: args[0]}); err != nil {
				return err
			}
			return printSealStoreName("resealed", args[0])
		},
	}
}

func newSealStoreStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the seal store is available, its sealer and the stored names",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.SealStore().SealStoreStatus(cmd.Context())
			if err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			}
			_, _ = fmt.Fprintf(outputWriter, "available: %t\nsealer: %s\nsecrets: %d\n",
				resp.Available, resp.SealerID, resp.SecretCount)
			rows := make([][]string, len(resp.SecretNames))
			for i, name := range resp.SecretNames {
				rows[i] = []string{name}
			}
			printTable([]string{"NAME"}, rows)
			return nil
		},
	}
}

// printSealStoreName renders the result of put, delete and reseal: a
// one-line confirmation in text mode, {"name": ...} for json and yaml.
func printSealStoreName(verb, name string) error {
	switch outputFormat {
	case "json":
		return printJSON(&sealStoreNameResult{Name: name})
	case "yaml":
		return printYAML(&sealStoreNameResult{Name: name})
	}
	_, _ = fmt.Fprintf(outputWriter, "%s %s\n", verb, name)
	return nil
}
