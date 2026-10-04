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
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// BackupTransportUnsupportedError is returned when the active transport
// does not implement transport.BackupClient.
type BackupTransportUnsupportedError struct{}

// Error returns a human-readable description.
func (e *BackupTransportUnsupportedError) Error() string {
	return "backup: active transport does not implement the backup service; use a network transport (rest, grpc, quic, mcp, unix)"
}

// backupClient resolves a BackupClient from c. Returns
// BackupTransportUnsupportedError if c is nil or its active transport does
// not satisfy transport.BackupClient, mirroring the userAuthFactorClient
// nil-guard pattern so the CLI and GUI bindings never panic on an
// interface conversion.
func backupClient(c *sdk.Client) (transport.BackupClient, error) {
	if c == nil {
		return nil, &BackupTransportUnsupportedError{}
	}
	bc := c.Backup()
	if bc == nil {
		return nil, &BackupTransportUnsupportedError{}
	}
	return bc, nil
}

// newBackupCmd creates the "backup" command group with all backup and
// restore management subcommands.
func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Backup and restore management",
		Long:  "Create, list, inspect, delete, restore, and verify backups of qrdb data.",
	}

	cmd.AddCommand(
		newBackupCreateCmd(),
		newBackupListCmd(),
		newBackupGetCmd(),
		newBackupDeleteCmd(),
		newBackupRestoreCmd(),
		newBackupVerifyCmd(),
	)

	return cmd
}

// newBackupCreateCmd creates the "backup create" subcommand.
func newBackupCreateCmd() *cobra.Command {
	var (
		destination      string
		compression      string
		encryption       string
		encryptionKeyRef string
		keyProvider      string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new backup",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			comp, err := parseCompressionType(compression)
			if err != nil {
				return err
			}

			enc, err := parseEncryptionType(encryption)
			if err != nil {
				return err
			}

			if enc == transport.EncryptionNone {
				warnPlaintextBackupCreate(cmd, destination)
			}

			req := &transport.BackupRequest{
				Destination:   destination,
				Compression:   comp,
				Encryption:    enc,
				KeyProviderID: keyProvider,
				KeyRef:        encryptionKeyRef,
			}

			bc, err := backupClient(client)
			if err != nil {
				return err
			}
			info, err := bc.CreateBackup(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printBackupInfo(info)
		},
	}

	cmd.Flags().StringVar(&destination, "destination", "", "Storage path or URI for the backup (e.g. /backups/daily or s3://bucket/prefix)")
	_ = cmd.MarkFlagRequired("destination")
	cmd.Flags().StringVar(&compression, "compression", "none", "Compression algorithm: none, zstd, snappy")
	cmd.Flags().StringVar(&encryption, "encryption", "", "Encryption algorithm: aes256 (default; requires --encryption-key-file), or none to explicitly write the backup in plaintext")
	cmd.Flags().StringVar(&encryptionKeyRef, "encryption-key-file", "", "Path to the 32-byte key file the key-wrap provider resolves (required unless --encryption is none)")
	cmd.Flags().StringVar(&keyProvider, "key-provider", "", "The registered keywrap.Provider to wrap this backup's DEK with (default: software)")

	return cmd
}

// warnPlaintextBackupCreate prints a warning to cmd's error stream that the
// backup about to be created is explicit plaintext. destination is named so
// an operator scripting many backups can tell which one the warning is
// about. The server independently logs and, where configured, audits the
// same choice (service.BackupService.warnPlaintextBackup); this is the
// CLI's own copy so the warning is visible at the point of the command too,
// not only in server logs the operator may not be tailing.
func warnPlaintextBackupCreate(cmd *cobra.Command, destination string) {
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
		"Warning: creating backup %q with encryption explicitly disabled (--encryption none). "+
			"The backup is plaintext. If it covers the system shard (the default when --shards "+
			"is not given), it carries the cluster CA and every tenant's login CA key material "+
			"in the clear.\n", destination)
}

// newBackupListCmd creates the "backup list" subcommand.
func newBackupListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available backups",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			bc, err := backupClient(client)
			if err != nil {
				return err
			}
			backups, err := bc.ListBackups(cmd.Context())
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(backups)
			case "yaml":
				return printYAML(backups)
			default:
				headers := []string{"ID", "TIMESTAMP", "SIZE", "SHARDS", "STATUS", "DESTINATION"}
				rows := make([][]string, len(backups))
				for i, b := range backups {
					rows[i] = backupListRow(b)
				}
				printTable(headers, rows)
				_, _ = fmt.Fprintf(outputWriter, "\nTotal: %d\n", len(backups))
				return nil
			}
		},
	}
}

// newBackupGetCmd creates the "backup get" subcommand.
func newBackupGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Retrieve backup details by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bc, err := backupClient(client)
			if err != nil {
				return err
			}
			info, err := bc.GetBackup(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return printBackupInfo(info)
		},
	}
}

// newBackupDeleteCmd creates the "backup delete" subcommand.
func newBackupDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a backup by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bc, err := backupClient(client)
			if err != nil {
				return err
			}
			if err := bc.DeleteBackup(cmd.Context(), args[0]); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}
}

// newBackupRestoreCmd creates the "backup restore" subcommand.
func newBackupRestoreCmd() *cobra.Command {
	var (
		backupID         string
		validate         bool
		decryptionKeyRef string
		targetShards     string
	)

	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore data from a backup",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.RestoreRequest{
				BackupID:         backupID,
				ValidateFirst:    validate,
				DecryptionKeyRef: decryptionKeyRef,
			}

			if targetShards != "" {
				shards, err := parseShardIDs(targetShards)
				if err != nil {
					return err
				}
				req.TargetShards = shards
			}

			bc, err := backupClient(client)
			if err != nil {
				return err
			}
			result, err := bc.RestoreBackup(cmd.Context(), req)
			if err != nil {
				return err
			}

			if printErr := printRestoreResult(result); printErr != nil {
				return printErr
			}
			if len(result.Errors) > 0 {
				// The proposal itself succeeded and per-shard detail was
				// already printed above; a non-nil error here only serves
				// to give scripted/automation callers a non-zero exit code
				// for a partial restore.
				return &RestorePartialFailureError{BackupID: result.BackupID, FailedCount: len(result.Errors)}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&backupID, "id", "", "Backup ID to restore from")
	_ = cmd.MarkFlagRequired("id")
	cmd.Flags().BoolVar(&validate, "validate", false, "Run integrity verification before restoring")
	cmd.Flags().StringVar(&decryptionKeyRef, "decryption-key-file", "", "Overrides the key reference stored in the backup's own envelope (e.g. the key file moved, or restore runs on a different node); left unset, the stored reference is used")
	cmd.Flags().StringVar(&targetShards, "shards", "", "Comma-separated shard IDs to restore (default: all shards)")

	return cmd
}

// newBackupVerifyCmd creates the "backup verify" subcommand.
func newBackupVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify <id>",
		Short: "Verify the integrity of a backup",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bc, err := backupClient(client)
			if err != nil {
				return err
			}
			result, err := bc.VerifyBackup(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return printVerifyResult(result)
		},
	}
}

// ---------------------------------------------------------------------------
// Output helpers
// ---------------------------------------------------------------------------

// printBackupInfo prints a single BackupInfo in the current output format.
func printBackupInfo(info *transport.BackupInfo) error {
	switch outputFormat {
	case "json":
		return printJSON(info)
	case "yaml":
		return printYAML(info)
	default:
		_, _ = fmt.Fprintf(outputWriter, "ID:          %s\n", info.ID)
		_, _ = fmt.Fprintf(outputWriter, "Timestamp:   %s\n", info.Timestamp.Format("2006-01-02T15:04:05Z"))
		_, _ = fmt.Fprintf(outputWriter, "Status:      %s\n", info.Status)
		_, _ = fmt.Fprintf(outputWriter, "Destination: %s\n", info.Destination)
		_, _ = fmt.Fprintf(outputWriter, "Size:        %s\n", formatBytes(info.Size))
		_, _ = fmt.Fprintf(outputWriter, "Shards:      %d\n", info.ShardCount)
		_, _ = fmt.Fprintf(outputWriter, "Compression: %s\n", info.Compression)
		_, _ = fmt.Fprintf(outputWriter, "Encryption:  %s\n", info.Encryption)
		if info.KeyProviderID != "" {
			_, _ = fmt.Fprintf(outputWriter, "KeyProvider: %s\n", info.KeyProviderID)
		}
		_, _ = fmt.Fprintf(outputWriter, "Checksum:    %s\n", info.Checksum)
		_, _ = fmt.Fprintf(outputWriter, "Duration:    %s\n", info.Duration)
		if info.ErrorMessage != "" {
			_, _ = fmt.Fprintf(outputWriter, "Error:       %s\n", info.ErrorMessage)
		}
		if len(info.Metadata) > 0 {
			_, _ = fmt.Fprintf(outputWriter, "Metadata:\n")
			for k, v := range info.Metadata {
				_, _ = fmt.Fprintf(outputWriter, "  %s: %s\n", k, v)
			}
		}
		return nil
	}
}

// backupListRow converts a BackupInfo into a table row for tabular output.
func backupListRow(b transport.BackupInfo) []string {
	return []string{
		b.ID,
		b.Timestamp.Format("2006-01-02T15:04:05Z"),
		formatBytes(b.Size),
		strconv.Itoa(b.ShardCount),
		string(b.Status),
		b.Destination,
	}
}

// printRestoreResult prints a RestoreResult in the current output format.
func printRestoreResult(result *transport.RestoreResult) error {
	switch outputFormat {
	case "json":
		return printJSON(result)
	case "yaml":
		return printYAML(result)
	default:
		_, _ = fmt.Fprintf(outputWriter, "BackupID:        %s\n", result.BackupID)
		_, _ = fmt.Fprintf(outputWriter, "ShardsRestored:  %d\n", result.ShardsRestored)
		_, _ = fmt.Fprintf(outputWriter, "Duration:        %s\n", result.Duration)
		if len(result.Errors) > 0 {
			_, _ = fmt.Fprintf(outputWriter, "Errors:\n")
			for _, e := range result.Errors {
				_, _ = fmt.Fprintf(outputWriter, "  shard %d: %s\n", e.ShardID, e.Message)
			}
		}
		return nil
	}
}

// printVerifyResult prints a VerifyResult in the current output format.
func printVerifyResult(result *transport.VerifyResult) error {
	switch outputFormat {
	case "json":
		return printJSON(result)
	case "yaml":
		return printYAML(result)
	default:
		_, _ = fmt.Fprintf(outputWriter, "Valid:            %s\n", strconv.FormatBool(result.Valid))
		_, _ = fmt.Fprintf(outputWriter, "ChecksumMatch:    %s\n", strconv.FormatBool(result.ChecksumMatch))
		_, _ = fmt.Fprintf(outputWriter, "ExpectedChecksum: %s\n", result.ExpectedChecksum)
		_, _ = fmt.Fprintf(outputWriter, "ActualChecksum:   %s\n", result.ActualChecksum)
		if len(result.Errors) > 0 {
			_, _ = fmt.Fprintf(outputWriter, "Errors:\n")
			for _, e := range result.Errors {
				_, _ = fmt.Fprintf(outputWriter, "  %s\n", e)
			}
		}
		return nil
	}
}

// ---------------------------------------------------------------------------
// Argument parsing helpers
// ---------------------------------------------------------------------------

// validCompressionTypes is the set of supported compression type values.
var validCompressionTypes = map[string]transport.CompressionType{
	"none":   transport.CompressionNone,
	"zstd":   transport.CompressionZstd,
	"snappy": transport.CompressionSnappy,
}

// parseCompressionType converts a string flag value to a CompressionType.
func parseCompressionType(s string) (transport.CompressionType, error) {
	if c, ok := validCompressionTypes[strings.ToLower(s)]; ok {
		return c, nil
	}
	return "", &InvalidArgError{Arg: "--compression", Reason: "must be one of none, zstd, snappy"}
}

// validEncryptionTypes is the set of supported encryption type values.
var validEncryptionTypes = map[string]transport.EncryptionType{
	"none":   transport.EncryptionNone,
	"aes256": transport.EncryptionAES256,
}

// parseEncryptionType converts a string flag value to an EncryptionType. An
// unset flag (empty string) passes through as EncryptionType(""): the server
// applies its own default (aes256, requiring a key) rather than the CLI
// picking one, so --encryption left off is not the same as --encryption none.
func parseEncryptionType(s string) (transport.EncryptionType, error) {
	if s == "" {
		return "", nil
	}
	if e, ok := validEncryptionTypes[strings.ToLower(s)]; ok {
		return e, nil
	}
	return "", &InvalidArgError{Arg: "--encryption", Reason: "must be one of none, aes256"}
}

// parseShardIDs parses a comma-separated list of shard IDs into []uint64.
func parseShardIDs(s string) ([]uint64, error) {
	parts := splitAndTrim(s)
	ids := make([]uint64, 0, len(parts))
	for _, p := range parts {
		id, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return nil, &InvalidArgError{Arg: "--shards", Reason: "shard ID " + p + " is not a valid uint64"}
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// formatBytes formats a byte count as a human-readable string.
func formatBytes(n int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.2f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.2f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
