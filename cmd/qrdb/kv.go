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
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// readConsistencyOpts converts a --consistency flag string into SDK read
// options. An empty string yields no option, letting the server apply its
// default (linearizable). Accepted values: strong, linearizable, eventual
// (alias: stale).
func readConsistencyOpts(level string) ([]transport.ReadOption, error) {
	if level == "" {
		return nil, nil
	}
	lvl, err := transport.ParseConsistencyLevel(level)
	if err != nil {
		return nil, err
	}
	return []transport.ReadOption{transport.WithConsistency(lvl)}, nil
}

// writeModeOpts converts a --write-mode flag string into SDK write options.
// An empty string yields no option, letting the server apply its default
// (sync). Accepted values: sync, wal_durable, batched.
func writeModeOpts(mode string) ([]transport.WriteOption, error) {
	if mode == "" {
		return nil, nil
	}
	m, err := transport.ParseWriteMode(mode)
	if err != nil {
		return nil, err
	}
	return []transport.WriteOption{transport.WithWriteMode(m)}, nil
}

// newKVCmd creates the "kv" command group with all KV subcommands.
func newKVCmd() *cobra.Command {
	kvCmd := &cobra.Command{
		Use:   "kv",
		Short: "Key-value data operations",
		Long:  "Perform key-value data-plane operations: put, get, delete, list, and exists.",
	}

	kvCmd.AddCommand(newKVPutCmd())
	kvCmd.AddCommand(newKVGetCmd())
	kvCmd.AddCommand(newKVDelCmd())
	kvCmd.AddCommand(newKVListCmd())
	kvCmd.AddCommand(newKVExistsCmd())
	kvCmd.AddCommand(newKVRangeCmd())
	kvCmd.AddCommand(newKVCompactCmd())
	kvCmd.AddCommand(newKVRevisionCmd())
	kvCmd.AddCommand(newKVRestoreCmd())

	return kvCmd
}

// newKVPutCmd creates the "kv put" subcommand.
func newKVPutCmd() *cobra.Command {
	var (
		leaseID   int64
		writeMode string
	)

	cmd := &cobra.Command{
		Use:   "put <key> <value>",
		Short: "Store a key-value pair",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]

			wopts, err := writeModeOpts(writeMode)
			if err != nil {
				return err
			}

			if leaseID != 0 {
				kv, err := client.MVCC().PutWithLease(cmd.Context(), key, []byte(value), leaseID, wopts...)
				if err != nil {
					return err
				}
				switch outputFormat {
				case "json":
					return printJSON(kv)
				case "yaml":
					return printYAML(kv)
				default:
					printOK()
					return nil
				}
			}

			if err := client.KV().Put(cmd.Context(), key, []byte(value), wopts...); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}

	cmd.Flags().Int64Var(&leaseID, "lease-id", 0, "Attach a lease to the key")
	cmd.Flags().StringVar(&writeMode, "write-mode", "", "Write durability: sync, wal_durable, or batched")

	return cmd
}

// newKVGetCmd creates the "kv get" subcommand.
func newKVGetCmd() *cobra.Command {
	var (
		showRevision bool
		atRevision   int64
		consistency  string
	)

	cmd := &cobra.Command{
		Use:   "get <key>",
		Short: "Retrieve a value by key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]

			ropts, err := readConsistencyOpts(consistency)
			if err != nil {
				return err
			}

			// Get at a specific historical revision.
			if atRevision != 0 {
				kv, err := client.MVCC().GetAtRevision(cmd.Context(), key, atRevision, ropts...)
				if err != nil {
					return err
				}
				return printMVCCKeyValue(kv)
			}

			// Get with full revision metadata.
			if showRevision {
				kv, err := client.MVCC().GetRevision(cmd.Context(), key, ropts...)
				if err != nil {
					return err
				}
				return printMVCCKeyValue(kv)
			}

			// Simple KV get.
			data, err := client.KV().Get(cmd.Context(), key, ropts...)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(string(data))
			case "yaml":
				return printYAML(string(data))
			default:
				printValue(string(data))
				return nil
			}
		},
	}

	cmd.Flags().BoolVar(&showRevision, "revision", false, "Include MVCC revision metadata")
	cmd.Flags().Int64Var(&atRevision, "at-revision", 0, "Retrieve key at a specific historical revision")
	cmd.Flags().StringVar(&consistency, "consistency", "", "Read consistency: strong, linearizable, or eventual")

	return cmd
}

// newKVDelCmd creates the "kv del" subcommand.
func newKVDelCmd() *cobra.Command {
	var writeMode string

	cmd := &cobra.Command{
		Use:   "del <key>",
		Short: "Delete a key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			wopts, err := writeModeOpts(writeMode)
			if err != nil {
				return err
			}
			if err := client.KV().Delete(cmd.Context(), key, wopts...); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&writeMode, "write-mode", "", "Write durability: sync, wal_durable, or batched")

	return cmd
}

// newKVListCmd creates the "kv list" subcommand.
func newKVListCmd() *cobra.Command {
	var consistency string

	cmd := &cobra.Command{
		Use:   "list [prefix]",
		Short: "List keys, optionally filtered by prefix",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prefix := ""
			if len(args) > 0 {
				prefix = args[0]
			}

			ropts, err := readConsistencyOpts(consistency)
			if err != nil {
				return err
			}

			keys, err := client.KV().List(cmd.Context(), prefix, ropts...)
			if err != nil {
				return err
			}

			return printList(keys, outputFormat)
		},
	}

	cmd.Flags().StringVar(&consistency, "consistency", "", "Read consistency: strong, linearizable, or eventual")

	return cmd
}

// newKVExistsCmd creates the "kv exists" subcommand.
func newKVExistsCmd() *cobra.Command {
	var consistency string

	cmd := &cobra.Command{
		Use:   "exists <key>",
		Short: "Check if a key exists",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			ropts, err := readConsistencyOpts(consistency)
			if err != nil {
				return err
			}
			_, err = client.KV().Get(cmd.Context(), key, ropts...)
			if err != nil {
				var knf *transport.KeyNotFoundError
				if errors.As(err, &knf) {
					printValue("false")
					return nil
				}
				return err
			}
			printValue("true")
			return nil
		},
	}

	cmd.Flags().StringVar(&consistency, "consistency", "", "Read consistency: strong, linearizable, or eventual")

	return cmd
}

// newKVRangeCmd creates the "kv range" subcommand for MVCC range queries.
func newKVRangeCmd() *cobra.Command {
	var (
		limit       int64
		revision    int64
		countOnly   bool
		keysOnly    bool
		sortOrder   string
		sortBy      string
		consistency string
	)

	cmd := &cobra.Command{
		Use:   "range <start> [end]",
		Short: "Query a key range with MVCC metadata",
		Long:  "Returns key-value pairs in the half-open interval [start, end). If end is omitted, returns all keys >= start.",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			start := []byte(args[0])
			var end []byte
			if len(args) > 1 {
				end = []byte(args[1])
			} else {
				// No end key: generate prefix-end to return all keys >= start.
				end = prefixEnd(start)
			}

			opts := transport.RangeOptions{
				Limit:     limit,
				Revision:  revision,
				CountOnly: countOnly,
				KeysOnly:  keysOnly,
			}

			so, err := parseSortOrder(sortOrder)
			if err != nil {
				return err
			}
			opts.SortOrder = so

			st, err := parseSortTarget(sortBy)
			if err != nil {
				return err
			}
			opts.SortTarget = st

			ropts, err := readConsistencyOpts(consistency)
			if err != nil {
				return err
			}

			resp, err := client.MVCC().Range(cmd.Context(), start, end, opts, ropts...)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(rangeDisplayResponse(resp))
			case "yaml":
				return printYAML(rangeDisplayResponse(resp))
			default:
				if countOnly {
					_, _ = fmt.Fprintf(outputWriter, "Count: %d\n", resp.Count)
					return nil
				}
				headers := []string{"KEY", "VALUE", "CREATE_REV", "MOD_REV", "VERSION", "LEASE_ID"}
				var rows [][]string
				for _, kv := range resp.Kvs {
					rows = append(rows, []string{
						string(kv.Key),
						string(kv.Value),
						strconv.FormatInt(kv.CreateRevision, 10),
						strconv.FormatInt(kv.ModRevision, 10),
						strconv.FormatInt(kv.Version, 10),
						strconv.FormatInt(kv.LeaseID, 10),
					})
				}
				printTable(headers, rows)
				return nil
			}
		},
	}

	cmd.Flags().Int64Var(&limit, "limit", 0, "Maximum number of keys to return")
	cmd.Flags().Int64Var(&revision, "revision", 0, "Query at a specific MVCC revision")
	cmd.Flags().BoolVar(&countOnly, "count-only", false, "Return only the count of matching keys")
	cmd.Flags().BoolVar(&keysOnly, "keys-only", false, "Return only keys without values")
	cmd.Flags().StringVar(&sortOrder, "sort-order", "", "Sort order: asc, desc")
	cmd.Flags().StringVar(&sortBy, "sort-by", "", "Sort target: key, value, version, create, mod")
	cmd.Flags().StringVar(&consistency, "consistency", "", "Read consistency: strong, linearizable, or eventual")

	return cmd
}

// newKVCompactCmd creates the "kv compact" subcommand.
func newKVCompactCmd() *cobra.Command {
	var revision int64

	cmd := &cobra.Command{
		Use:   "compact",
		Short: "Compact this database's MVCC history",
		Long: "Discards the MVCC history of the database the command is scoped to (--database, or the\n" +
			"server default) at or below --revision. Only that database is compacted: another\n" +
			"database's history, and another tenant's, is untouched.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := client.MVCC().Compact(cmd.Context(), revision); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}

	cmd.Flags().Int64Var(&revision, "revision", 0, "Compact this database's history at or below this store revision")
	_ = cmd.MarkFlagRequired("revision")

	return cmd
}

// newKVRevisionCmd creates the "kv revision" subcommand.
func newKVRevisionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revision",
		Short: "Show the current MVCC revision",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rev, err := client.MVCC().CurrentRevision(cmd.Context())
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(map[string]int64{"revision": rev})
			case "yaml":
				return printYAML(map[string]int64{"revision": rev})
			default:
				_, _ = fmt.Fprintf(outputWriter, "%d\n", rev)
				return nil
			}
		},
	}
}

// newKVRestoreCmd creates the "kv restore" subcommand for selective
// point-in-time restore.
func newKVRestoreCmd() *cobra.Command {
	var (
		revision int64
		prefix   string
		keys     []string
		dryRun   bool
		force    bool
	)

	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore keys to the value they held at an earlier MVCC revision",
		Long: `Restore returns a bounded set of keys to the value they held at an earlier
MVCC revision by proposing NEW writes. It is a logical undo, never a Raft
rewind: committed history is never discarded, and every restored key is
written forward as an ordinary new revision.

This command requires the target daemon to run raft.sm_type: mvcc. The
default "pebble" state machine keeps no revision history and refuses this
command.

Scope is exactly one of --prefix or --keys; an unscoped restore (neither
flag) or an ambiguous one (both flags) is refused.

Use --dry-run to preview the plan (ACTION, KEY, FROM-REV, TO-REV) without
writing anything. Without --dry-run, each key is applied as a
compare-and-swap against the revision the plan observed: a key modified
since the plan was computed is left untouched and reported as a conflict
instead of being overwritten. Pass --force to overwrite unconditionally.

Exit code: when one or more keys conflict and --force was not set, this
command exits non-zero even though the request itself succeeded, because a
partially applied restore that reports exit 0 reads as a complete undo. The
conflicting keys are listed in the output; re-run with --force, or re-run
"kv restore" again once the conflicting writers have quiesced.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.RestoreRevisionRequest{
				Revision: revision,
				DryRun:   dryRun,
				Force:    force,
			}
			if prefix != "" {
				req.Prefix = []byte(prefix)
			}
			if len(keys) > 0 {
				req.Keys = make([][]byte, len(keys))
				for i, k := range keys {
					req.Keys[i] = []byte(k)
				}
			}

			result, err := client.MVCC().RestoreToRevision(cmd.Context(), req)
			if err != nil {
				return err
			}

			if printErr := printRestoreRevisionResult(result); printErr != nil {
				return printErr
			}

			if !dryRun && !force && result.Conflicted > 0 {
				// The restore itself succeeded and per-key detail was
				// already printed above; a non-nil error here only serves
				// to give scripted/automation callers a non-zero exit code
				// for a partially-applied restore.
				return &RestoreConflictsError{Revision: result.Revision, Conflicted: result.Conflicted}
			}
			return nil
		},
	}

	cmd.Flags().Int64Var(&revision, "revision", 0, "Target MVCC revision to restore keys to")
	_ = cmd.MarkFlagRequired("revision")
	cmd.Flags().StringVar(&prefix, "prefix", "", "Restore every key beginning with this prefix (mutually exclusive with --keys)")
	cmd.Flags().StringSliceVar(&keys, "keys", nil, "Restore this explicit list of keys, comma-separated or repeatable (mutually exclusive with --prefix)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Compute and print the restore plan without writing anything")
	cmd.Flags().BoolVar(&force, "force", false, "Apply the plan unconditionally, overwriting keys modified since the plan was computed")
	cmd.MarkFlagsOneRequired("prefix", "keys")
	cmd.MarkFlagsMutuallyExclusive("prefix", "keys")

	return cmd
}

// restoreKeyChangeDisplay is a string-typed display wrapper for
// transport.RestoreKeyChange that prevents Go's JSON/YAML encoders from
// base64-encoding the Key field.
type restoreKeyChangeDisplay struct {
	Key          string                  `json:"key"`
	Action       transport.RestoreAction `json:"action"`
	FromRevision int64                   `json:"from_revision,omitempty"`
	ToRevision   int64                   `json:"to_revision,omitempty"`
	ValueBytes   int64                   `json:"value_bytes,omitempty"`
}

// restoreRevisionResultDisplay is a string-typed display wrapper for
// transport.RestoreRevisionResult.
type restoreRevisionResultDisplay struct {
	Revision   int64                     `json:"revision"`
	Changes    []restoreKeyChangeDisplay `json:"changes"`
	Conflicts  []restoreKeyChangeDisplay `json:"conflicts,omitempty"`
	Applied    int                       `json:"applied"`
	Skipped    int                       `json:"skipped"`
	Conflicted int                       `json:"conflicted"`
	DryRun     bool                      `json:"dry_run"`
}

// restoreKeyChangeDisplayFrom converts a single RestoreKeyChange to display form.
func restoreKeyChangeDisplayFrom(c *transport.RestoreKeyChange) restoreKeyChangeDisplay {
	return restoreKeyChangeDisplay{
		Key:          string(c.Key),
		Action:       c.Action,
		FromRevision: c.FromRevision,
		ToRevision:   c.ToRevision,
		ValueBytes:   c.ValueBytes,
	}
}

// restoreKeyChangeDisplaysFrom converts a slice of RestoreKeyChange to display form.
func restoreKeyChangeDisplaysFrom(changes []*transport.RestoreKeyChange) []restoreKeyChangeDisplay {
	out := make([]restoreKeyChangeDisplay, len(changes))
	for i, c := range changes {
		out[i] = restoreKeyChangeDisplayFrom(c)
	}
	return out
}

// restoreRevisionResultDisplayFrom converts a RestoreRevisionResult to display form.
func restoreRevisionResultDisplayFrom(result *transport.RestoreRevisionResult) restoreRevisionResultDisplay {
	return restoreRevisionResultDisplay{
		Revision:   result.Revision,
		Changes:    restoreKeyChangeDisplaysFrom(result.Changes),
		Conflicts:  restoreKeyChangeDisplaysFrom(result.Conflicts),
		Applied:    result.Applied,
		Skipped:    result.Skipped,
		Conflicted: result.Conflicted,
		DryRun:     result.DryRun,
	}
}

// restoreKeyChangeRow converts a RestoreKeyChange to a table row of
// {ACTION, KEY, FROM-REV, TO-REV}.
func restoreKeyChangeRow(c *transport.RestoreKeyChange) []string {
	return []string{
		string(c.Action),
		string(c.Key),
		strconv.FormatInt(c.FromRevision, 10),
		strconv.FormatInt(c.ToRevision, 10),
	}
}

// printRestoreRevisionResult prints a RestoreRevisionResult in the current
// output format. Table output lists the full plan, then a one-line summary,
// then any conflicts distinctly so an operator can see exactly what was NOT
// restored and why.
func printRestoreRevisionResult(result *transport.RestoreRevisionResult) error {
	switch outputFormat {
	case "json":
		return printJSON(restoreRevisionResultDisplayFrom(result))
	case "yaml":
		return printYAML(restoreRevisionResultDisplayFrom(result))
	default:
		headers := []string{"ACTION", "KEY", "FROM-REV", "TO-REV"}
		rows := make([][]string, 0, len(result.Changes))
		for _, c := range result.Changes {
			rows = append(rows, restoreKeyChangeRow(c))
		}
		printTable(headers, rows)

		mode := "apply"
		if result.DryRun {
			mode = "dry-run"
		}
		_, _ = fmt.Fprintf(outputWriter, "\nRevision: %d  Mode: %s  Applied: %d  Skipped: %d  Conflicted: %d\n",
			result.Revision, mode, result.Applied, result.Skipped, result.Conflicted)

		if len(result.Conflicts) > 0 {
			_, _ = fmt.Fprintf(outputWriter, "\nCONFLICTS (not applied -- key changed since the plan was computed; retry with --force to override):\n")
			crows := make([][]string, 0, len(result.Conflicts))
			for _, c := range result.Conflicts {
				crows = append(crows, restoreKeyChangeRow(c))
			}
			printTable(headers, crows)
		}
		return nil
	}
}

// mvccKeyValueDisplay is a string-typed display wrapper for MVCCKeyValue
// that prevents Go's JSON encoder from base64-encoding []byte fields.
type mvccKeyValueDisplay struct {
	Key            string `json:"key"`
	Value          string `json:"value"`
	CreateRevision int64  `json:"create_revision"`
	ModRevision    int64  `json:"mod_revision"`
	Version        int64  `json:"version"`
	LeaseID        int64  `json:"lease_id"`
}

// rangeResponseDisplay is a string-typed display wrapper for RangeResponse.
type rangeResponseDisplay struct {
	Kvs   []mvccKeyValueDisplay `json:"kvs"`
	Count int64                 `json:"count"`
}

// rangeDisplayResponse converts a RangeResponse to its display form with
// string keys and values instead of []byte to avoid base64 encoding in
// JSON and YAML output.
func rangeDisplayResponse(resp *transport.RangeResponse) rangeResponseDisplay {
	kvs := make([]mvccKeyValueDisplay, len(resp.Kvs))
	for i, kv := range resp.Kvs {
		kvs[i] = mvccKeyValueDisplay{
			Key:            string(kv.Key),
			Value:          string(kv.Value),
			CreateRevision: kv.CreateRevision,
			ModRevision:    kv.ModRevision,
			Version:        kv.Version,
			LeaseID:        kv.LeaseID,
		}
	}
	return rangeResponseDisplay{Kvs: kvs, Count: resp.Count}
}

// mvccKeyValueDisplayFrom converts a single MVCCKeyValue to display form.
func mvccKeyValueDisplayFrom(kv *transport.MVCCKeyValue) mvccKeyValueDisplay {
	return mvccKeyValueDisplay{
		Key:            string(kv.Key),
		Value:          string(kv.Value),
		CreateRevision: kv.CreateRevision,
		ModRevision:    kv.ModRevision,
		Version:        kv.Version,
		LeaseID:        kv.LeaseID,
	}
}

// printMVCCKeyValue prints an MVCCKeyValue in the current output format.
func printMVCCKeyValue(kv *transport.MVCCKeyValue) error {
	switch outputFormat {
	case "json":
		return printJSON(mvccKeyValueDisplayFrom(kv))
	case "yaml":
		return printYAML(mvccKeyValueDisplayFrom(kv))
	default:
		_, _ = fmt.Fprintf(outputWriter, "Key:            %s\n", kv.Key)
		_, _ = fmt.Fprintf(outputWriter, "Value:          %s\n", string(kv.Value))
		_, _ = fmt.Fprintf(outputWriter, "CreateRevision: %d\n", kv.CreateRevision)
		_, _ = fmt.Fprintf(outputWriter, "ModRevision:    %d\n", kv.ModRevision)
		_, _ = fmt.Fprintf(outputWriter, "Version:        %d\n", kv.Version)
		_, _ = fmt.Fprintf(outputWriter, "LeaseID:        %d\n", kv.LeaseID)
		return nil
	}
}

// sortOrderMap maps CLI sort order strings to transport SortOrder values.
var sortOrderMap = map[string]transport.SortOrder{
	"":     transport.SortNone,
	"asc":  transport.SortAscend,
	"desc": transport.SortDescend,
}

// parseSortOrder converts a string to a transport.SortOrder.
func parseSortOrder(s string) (transport.SortOrder, error) {
	so, ok := sortOrderMap[s]
	if !ok {
		return 0, &InvalidArgError{Arg: "sort-order", Reason: "must be one of: asc, desc"}
	}
	return so, nil
}

// sortTargetMap maps CLI sort target strings to transport SortTarget values.
var sortTargetMap = map[string]transport.SortTarget{
	"":        transport.SortByKey,
	"key":     transport.SortByKey,
	"value":   transport.SortByValue,
	"version": transport.SortByVersion,
	"create":  transport.SortByCreateRevision,
	"mod":     transport.SortByModRevision,
}

// prefixEnd returns the key that is the next prefix after the given prefix,
// used to create a half-open range [prefix, prefixEnd) that covers all keys
// starting with the prefix.
func prefixEnd(prefix []byte) []byte {
	end := make([]byte, len(prefix))
	copy(end, prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xFF {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}

// parseSortTarget converts a string to a transport.SortTarget.
func parseSortTarget(s string) (transport.SortTarget, error) {
	st, ok := sortTargetMap[s]
	if !ok {
		return 0, &InvalidArgError{Arg: "sort-by", Reason: "must be one of: key, value, version, create, mod"}
	}
	return st, nil
}
