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

// dek.go implements the "dek" command group: the caller tenant's data
// encryption keyring (status, rotate, retire, encrypt, decrypt, rewrap), all
// wired through the SDK DEKClient. Keys never leave the server; ciphertexts
// and associated data travel base64-encoded on the command line.
package main

import (
	"encoding/base64"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newDEKCmd builds the "dek" command group.
func newDEKCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dek",
		Short: "Tenant data encryption keyring (status, rotate, retire, encrypt, decrypt, rewrap)",
		Long: "Operate the caller's tenant data encryption keyring. Every command acts on the\n" +
			"caller's own tenant; --tenant may only name that tenant.",
	}
	cmd.PersistentFlags().String("tenant", "", "Tenant ID (must be the caller's own; default: the caller's tenant)")
	cmd.AddCommand(
		newDEKStatusCmd(),
		newDEKRotateCmd(),
		newDEKRetireCmd(),
		newDEKEncryptCmd(),
		newDEKDecryptCmd(),
		newDEKRewrapCmd(),
	)
	return cmd
}

func newDEKStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the keyring's versions (never key material)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.DEK().TenantDEKStatus(cmd.Context(), &transport.TenantDEKRequest{TenantID: dekTenant(cmd)})
			if err != nil {
				return err
			}
			return printDEKStatus(resp)
		},
	}
}

func newDEKRotateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate",
		Short: "Add a key version and make it current; earlier versions keep decrypting",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.DEK().TenantDEKRotate(cmd.Context(), &transport.TenantDEKRequest{TenantID: dekTenant(cmd)})
			if err != nil {
				return err
			}
			return printDEKStatus(resp)
		},
	}
}

func newDEKRetireCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "retire <version>",
		Short: "Destroy an old key version (the current version is refused; rewrap first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			version, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return &InvalidArgError{Arg: "version", Reason: err.Error()}
			}
			resp, err := client.DEK().TenantDEKRetire(cmd.Context(),
				&transport.TenantDEKRetireRequest{TenantID: dekTenant(cmd), Version: version})
			if err != nil {
				return err
			}
			return printDEKStatus(resp)
		},
	}
}

func newDEKEncryptCmd() *cobra.Command {
	var plaintext, aad string
	cmd := &cobra.Command{
		Use:   "encrypt",
		Short: "Encrypt --plaintext under the current key version; prints the base64 ciphertext",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			aadBytes, err := decodeDEKArg("aad", aad)
			if err != nil {
				return err
			}
			resp, err := client.DEK().TenantDEKEncrypt(cmd.Context(), &transport.TenantDEKEncryptRequest{
				TenantID:  dekTenant(cmd),
				Plaintext: []byte(plaintext),
				AAD:       aadBytes,
			})
			if err != nil {
				return err
			}
			return printDEKData(resp, base64.StdEncoding.EncodeToString(resp.Ciphertext))
		},
	}
	cmd.Flags().StringVar(&plaintext, "plaintext", "", "Text to encrypt")
	_ = cmd.MarkFlagRequired("plaintext")
	cmd.Flags().StringVar(&aad, "aad", "", "Associated data, base64-encoded")
	return cmd
}

func newDEKDecryptCmd() *cobra.Command {
	var aad string
	cmd := &cobra.Command{
		Use:   "decrypt <ciphertext-base64>",
		Short: "Decrypt a ciphertext; prints the plaintext",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ciphertext, err := decodeDEKArg("ciphertext", args[0])
			if err != nil {
				return err
			}
			aadBytes, err := decodeDEKArg("aad", aad)
			if err != nil {
				return err
			}
			resp, err := client.DEK().TenantDEKDecrypt(cmd.Context(), &transport.TenantDEKDecryptRequest{
				TenantID:   dekTenant(cmd),
				Ciphertext: ciphertext,
				AAD:        aadBytes,
			})
			if err != nil {
				return err
			}
			return printDEKData(resp, string(resp.Plaintext))
		},
	}
	cmd.Flags().StringVar(&aad, "aad", "", "Associated data given at encryption, base64-encoded")
	return cmd
}

func newDEKRewrapCmd() *cobra.Command {
	var aad string
	cmd := &cobra.Command{
		Use:   "rewrap <ciphertext-base64>",
		Short: "Re-encrypt a ciphertext under the current key version; prints the new ciphertext",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ciphertext, err := decodeDEKArg("ciphertext", args[0])
			if err != nil {
				return err
			}
			aadBytes, err := decodeDEKArg("aad", aad)
			if err != nil {
				return err
			}
			resp, err := client.DEK().TenantDEKRewrap(cmd.Context(), &transport.TenantDEKRewrapRequest{
				TenantID:   dekTenant(cmd),
				Ciphertext: ciphertext,
				AAD:        aadBytes,
			})
			if err != nil {
				return err
			}
			return printDEKData(resp, base64.StdEncoding.EncodeToString(resp.Ciphertext))
		},
	}
	cmd.Flags().StringVar(&aad, "aad", "", "Associated data given at encryption, base64-encoded")
	return cmd
}

// dekTenant reads the group's --tenant flag.
func dekTenant(cmd *cobra.Command) string {
	tenantID, err := cmd.Flags().GetString("tenant")
	if err != nil {
		return ""
	}
	return tenantID
}

// decodeDEKArg decodes a base64 argument; empty decodes to nil.
func decodeDEKArg(name, value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	out, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, &Base64DecodeError{Field: name, Err: err}
	}
	return out, nil
}

// printDEKStatus renders keyring status in the selected output format.
func printDEKStatus(resp *transport.TenantDEKStatusResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(resp)
	case "yaml":
		return printYAML(resp)
	}
	_, _ = fmt.Fprintf(outputWriter, "tenant %s: %s, current v%d\n", resp.TenantID, resp.Algorithm, resp.CurrentVersion)
	rows := make([][]string, len(resp.Versions))
	for i, v := range resp.Versions {
		state, retired := "active", ""
		if v.Version == resp.CurrentVersion {
			state = "current"
		}
		if v.RetiredAt != nil {
			state, retired = "retired", v.RetiredAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		rows[i] = []string{
			strconv.FormatUint(v.Version, 10), state,
			v.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"), retired,
			v.ProviderID, v.KeyRef,
		}
	}
	printTable([]string{"VERSION", "STATE", "CREATED", "RETIRED", "PROVIDER", "KEY REF"}, rows)
	return nil
}

// printDEKData renders a data-carrying result: the whole response for json
// and yaml, the bare value otherwise.
func printDEKData(resp any, value string) error {
	switch outputFormat {
	case "json":
		return printJSON(resp)
	case "yaml":
		return printYAML(resp)
	}
	printValue(value)
	return nil
}
