//go:build qrdb_vlog

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/hex"
	"fmt"

	"github.com/spf13/cobra"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// vlogSigningKeyTypes are the head-signing key types the CLI accepts, the
// ones pkg/vlog signs with.
var vlogSigningKeyTypes = map[string]sdk.HeadSignatureType{
	string(sdk.HeadSignatureEd25519):         sdk.HeadSignatureEd25519,
	string(sdk.HeadSignatureECDSAP256SHA256): sdk.HeadSignatureECDSAP256SHA256,
}

// parseVlogSigningKeyType returns the signature type named by name. An empty
// name is the empty type when optional allows it: a log created without a
// head-signing key.
func parseVlogSigningKeyType(name string, optional bool) (sdk.HeadSignatureType, error) {
	if name == "" && optional {
		return "", nil
	}
	t, ok := vlogSigningKeyTypes[name]
	if !ok {
		return "", &InvalidFlagValueError{Flag: "signing-key-type", Value: name, Want: "ed25519 or ecdsa-p256-sha256"}
	}
	return t, nil
}

// printVlogResult writes v in the selected output format; text writes the
// table form.
func printVlogResult(v any, text func()) error {
	switch outputFormat {
	case "json":
		return printJSON(v)
	case "yaml":
		return printYAML(v)
	default:
		text()
		return nil
	}
}

// printVlogKey writes a head-signing key's version and public key, PKIX DER
// in hex.
func printVlogKey(key *vlogwire.HeadKey) {
	if key == nil {
		return
	}
	_, _ = fmt.Fprintf(outputWriter, "Key version: %d\n", key.Version)
	_, _ = fmt.Fprintf(outputWriter, "Public key:  %s\n", hex.EncodeToString(key.PublicKey))
}

// printVlogHashes writes a proof's hashes, one per line, in hex.
func printVlogHashes(hashes [][]byte) {
	for _, h := range hashes {
		_, _ = fmt.Fprintln(outputWriter, hex.EncodeToString(h))
	}
}

func newVlogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vlog",
		Short: "Verifiable log operations",
		Long:  "Create append-only verifiable logs, append and read entries, prove inclusion and consistency (RFC 9162), and sign tree heads as C2SP checkpoints.",
	}
	cmd.AddCommand(
		newVlogCreateCmd(),
		newVlogListCmd(),
		newVlogAppendCmd(),
		newVlogGetCmd(),
		newVlogProveInclusionCmd(),
		newVlogProveConsistencyCmd(),
		newVlogHeadCmd(),
		newVlogSignHeadCmd(),
		newVlogRotateKeyCmd(),
		newVlogExpireCmd(),
	)
	return cmd
}

func newVlogCreateCmd() *cobra.Command {
	var (
		log               string
		origin            string
		verifierKeys      []string
		allowExpireBodies bool
		signingKeyType    string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a verifiable log",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			keyType, err := parseVlogSigningKeyType(signingKeyType, true)
			if err != nil {
				return err
			}
			resp, err := client.Vlog().VlogCreate(cmd.Context(), &vlogwire.CreateRequest{
				Log:               log,
				Origin:            origin,
				VerifierKeys:      verifierKeys,
				AllowExpireBodies: allowExpireBodies,
				SigningKeyType:    keyType,
			})
			if err != nil {
				return err
			}
			return printVlogResult(resp, func() {
				_, _ = fmt.Fprintf(outputWriter, "Log:         %s\n", resp.Log)
				_, _ = fmt.Fprintf(outputWriter, "Database:    %s\n", resp.Database)
				printVlogKey(resp.Key)
			})
		},
	}

	cmd.Flags().StringVar(&log, "log", "", "Log name (required)")
	cmd.Flags().StringVar(&origin, "origin", "", "Checkpoint origin line, required with --signing-key-type")
	cmd.Flags().StringArrayVar(&verifierKeys, "verifier-key", nil, "C2SP note verifier key a checkpoint must also verify under (repeatable)")
	cmd.Flags().BoolVar(&allowExpireBodies, "allow-expire-bodies", false, "Allow the bodies of old entries to be expired")
	cmd.Flags().StringVar(&signingKeyType, "signing-key-type", "", "Head-signing key type: ed25519 or ecdsa-p256-sha256 (none when empty)")
	_ = cmd.MarkFlagRequired("log")

	return cmd
}

func newVlogListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the verifiable logs of the database",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Vlog().VlogList(cmd.Context(), &vlogwire.ListRequest{})
			if err != nil {
				return err
			}
			return printVlogResult(resp, func() {
				for _, name := range resp.Logs {
					_, _ = fmt.Fprintln(outputWriter, name)
				}
			})
		},
	}
}

func newVlogAppendCmd() *cobra.Command {
	var (
		log     string
		entries []string
	)

	cmd := &cobra.Command{
		Use:   "append",
		Short: "Append entries to a verifiable log",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &vlogwire.AppendRequest{Log: log, Entries: make([][]byte, len(entries))}
			for i, e := range entries {
				req.Entries[i] = []byte(e)
			}
			resp, err := client.Vlog().VlogAppend(cmd.Context(), req)
			if err != nil {
				return err
			}
			return printVlogResult(resp, func() {
				_, _ = fmt.Fprintf(outputWriter, "First index: %d\n", resp.FirstIndex)
				_, _ = fmt.Fprintf(outputWriter, "Count:       %d\n", resp.Count)
			})
		},
	}

	cmd.Flags().StringVar(&log, "log", "", "Log name (required)")
	cmd.Flags().StringArrayVar(&entries, "entry", nil, "Entry body (repeatable, in order; required)")
	_ = cmd.MarkFlagRequired("log")
	_ = cmd.MarkFlagRequired("entry")

	return cmd
}

func newVlogGetCmd() *cobra.Command {
	var (
		log   string
		from  uint64
		limit int
	)

	cmd := &cobra.Command{
		Use:   "get",
		Short: "Read a run of a verifiable log's entries",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Vlog().VlogGet(cmd.Context(), &vlogwire.GetRequest{Log: log, From: from, Limit: limit})
			if err != nil {
				return err
			}
			return printVlogResult(resp, func() {
				for _, e := range resp.Entries {
					if e.Expired {
						_, _ = fmt.Fprintf(outputWriter, "%d\t(expired)\n", e.Index)
						continue
					}
					_, _ = fmt.Fprintf(outputWriter, "%d\t%s\n", e.Index, e.Body)
				}
			})
		},
	}

	cmd.Flags().StringVar(&log, "log", "", "Log name (required)")
	cmd.Flags().Uint64Var(&from, "from", 0, "Index of the first entry")
	cmd.Flags().IntVar(&limit, "limit", 0, "Most entries to read (server default when 0)")
	_ = cmd.MarkFlagRequired("log")

	return cmd
}

func newVlogProveInclusionCmd() *cobra.Command {
	var (
		log   string
		index uint64
		size  uint64
	)

	cmd := &cobra.Command{
		Use:   "prove-inclusion",
		Short: "Prove a leaf is in a tree of the log (RFC 9162)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Vlog().VlogProveInclusion(cmd.Context(), &vlogwire.InclusionRequest{Log: log, Index: index, Size: size})
			if err != nil {
				return err
			}
			return printVlogResult(resp, func() {
				_, _ = fmt.Fprintf(outputWriter, "Index: %d\n", resp.Index)
				_, _ = fmt.Fprintf(outputWriter, "Size:  %d\n", resp.Size)
				printVlogHashes(resp.Hashes)
			})
		},
	}

	cmd.Flags().StringVar(&log, "log", "", "Log name (required)")
	cmd.Flags().Uint64Var(&index, "index", 0, "Index of the leaf")
	cmd.Flags().Uint64Var(&size, "size", 0, "Size of the tree (required)")
	_ = cmd.MarkFlagRequired("log")
	_ = cmd.MarkFlagRequired("size")

	return cmd
}

func newVlogProveConsistencyCmd() *cobra.Command {
	var (
		log   string
		size1 uint64
		size2 uint64
	)

	cmd := &cobra.Command{
		Use:   "prove-consistency",
		Short: "Prove one tree of the log is a prefix of another (RFC 9162)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Vlog().VlogProveConsistency(cmd.Context(), &vlogwire.ConsistencyRequest{Log: log, Size1: size1, Size2: size2})
			if err != nil {
				return err
			}
			return printVlogResult(resp, func() {
				_, _ = fmt.Fprintf(outputWriter, "Size1: %d\n", resp.Size1)
				_, _ = fmt.Fprintf(outputWriter, "Size2: %d\n", resp.Size2)
				printVlogHashes(resp.Hashes)
			})
		},
	}

	cmd.Flags().StringVar(&log, "log", "", "Log name (required)")
	cmd.Flags().Uint64Var(&size1, "size1", 0, "Size of the earlier tree (required)")
	cmd.Flags().Uint64Var(&size2, "size2", 0, "Size of the later tree (required)")
	_ = cmd.MarkFlagRequired("log")
	_ = cmd.MarkFlagRequired("size1")
	_ = cmd.MarkFlagRequired("size2")

	return cmd
}

func newVlogHeadCmd() *cobra.Command {
	var log string

	cmd := &cobra.Command{
		Use:   "head",
		Short: "Show a verifiable log's tree head and latest checkpoint",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Vlog().VlogHead(cmd.Context(), &vlogwire.LogRequest{Log: log})
			if err != nil {
				return err
			}
			return printVlogResult(resp, func() {
				_, _ = fmt.Fprintf(outputWriter, "Size:            %d\n", resp.Size)
				_, _ = fmt.Fprintf(outputWriter, "Root:            %s\n", hex.EncodeToString(resp.Root))
				_, _ = fmt.Fprintf(outputWriter, "Bodies from:     %d\n", resp.BodiesFrom)
				_, _ = fmt.Fprintf(outputWriter, "Checkpoint size: %d\n", resp.CheckpointSize)
				if len(resp.Checkpoint) > 0 {
					_, _ = fmt.Fprintf(outputWriter, "%s", resp.Checkpoint)
				}
			})
		},
	}

	cmd.Flags().StringVar(&log, "log", "", "Log name (required)")
	_ = cmd.MarkFlagRequired("log")

	return cmd
}

func newVlogSignHeadCmd() *cobra.Command {
	var log string

	cmd := &cobra.Command{
		Use:   "sign-head",
		Short: "Sign a verifiable log's current head as a C2SP checkpoint",
		Long:  "Sign the log's current tree head and record it as the log's checkpoint, a C2SP signed note. A head already signed at the same tree size is refused: append first.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Vlog().VlogSignHead(cmd.Context(), &vlogwire.LogRequest{Log: log})
			if err != nil {
				return err
			}
			return printVlogResult(resp, func() {
				_, _ = fmt.Fprintf(outputWriter, "%s", resp.Checkpoint)
			})
		},
	}

	cmd.Flags().StringVar(&log, "log", "", "Log name (required)")
	_ = cmd.MarkFlagRequired("log")

	return cmd
}

func newVlogRotateKeyCmd() *cobra.Command {
	var (
		log            string
		signingKeyType string
	)

	cmd := &cobra.Command{
		Use:   "rotate-key",
		Short: "Rotate a verifiable log's head-signing key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			keyType, err := parseVlogSigningKeyType(signingKeyType, false)
			if err != nil {
				return err
			}
			resp, err := client.Vlog().VlogRotateKey(cmd.Context(), &vlogwire.RotateKeyRequest{Log: log, SigningKeyType: keyType})
			if err != nil {
				return err
			}
			return printVlogResult(resp, func() { printVlogKey(&resp.Key) })
		},
	}

	cmd.Flags().StringVar(&log, "log", "", "Log name (required)")
	cmd.Flags().StringVar(&signingKeyType, "signing-key-type", string(sdk.HeadSignatureEd25519), "New key type: ed25519 or ecdsa-p256-sha256")
	_ = cmd.MarkFlagRequired("log")

	return cmd
}

func newVlogExpireCmd() *cobra.Command {
	var (
		log    string
		cutoff uint64
	)

	cmd := &cobra.Command{
		Use:   "expire",
		Short: "Expire the bodies of the entries before a cutoff index",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Vlog().VlogExpireBodies(cmd.Context(), &vlogwire.ExpireRequest{Log: log, Cutoff: cutoff})
			if err != nil {
				return err
			}
			return printVlogResult(resp, func() {
				_, _ = fmt.Fprintf(outputWriter, "Expired: %d\n", resp.Expired)
			})
		},
	}

	cmd.Flags().StringVar(&log, "log", "", "Log name (required)")
	cmd.Flags().Uint64Var(&cutoff, "cutoff", 0, "Entries before this index lose their bodies (required)")
	_ = cmd.MarkFlagRequired("log")
	_ = cmd.MarkFlagRequired("cutoff")

	return cmd
}
