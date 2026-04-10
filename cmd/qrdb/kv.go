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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/spf13/cobra"
)

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

	return kvCmd
}

// newKVPutCmd creates the "kv put" subcommand.
func newKVPutCmd() *cobra.Command {
	var leaseID int64

	cmd := &cobra.Command{
		Use:   "put <key> <value>",
		Short: "Store a key-value pair",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]

			if leaseID != 0 {
				kv, err := client.MVCC().PutWithLease(cmd.Context(), key, []byte(value), leaseID)
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

			if err := client.KV().Put(cmd.Context(), key, []byte(value)); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}

	cmd.Flags().Int64Var(&leaseID, "lease-id", 0, "Attach a lease to the key")

	return cmd
}

// newKVGetCmd creates the "kv get" subcommand.
func newKVGetCmd() *cobra.Command {
	var (
		showRevision bool
		atRevision   int64
	)

	cmd := &cobra.Command{
		Use:   "get <key>",
		Short: "Retrieve a value by key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]

			// Get at a specific historical revision.
			if atRevision != 0 {
				kv, err := client.MVCC().GetAtRevision(cmd.Context(), key, atRevision)
				if err != nil {
					return err
				}
				return printMVCCKeyValue(kv)
			}

			// Get with full revision metadata.
			if showRevision {
				kv, err := client.MVCC().GetRevision(cmd.Context(), key)
				if err != nil {
					return err
				}
				return printMVCCKeyValue(kv)
			}

			// Simple KV get.
			data, err := client.KV().Get(cmd.Context(), key)
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

	return cmd
}

// newKVDelCmd creates the "kv del" subcommand.
func newKVDelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "del <key>",
		Short: "Delete a key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if err := client.KV().Delete(cmd.Context(), key); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}
}

// newKVListCmd creates the "kv list" subcommand.
func newKVListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [prefix]",
		Short: "List keys, optionally filtered by prefix",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prefix := ""
			if len(args) > 0 {
				prefix = args[0]
			}

			keys, err := client.KV().List(cmd.Context(), prefix)
			if err != nil {
				return err
			}

			return printList(keys, outputFormat)
		},
	}
}

// newKVExistsCmd creates the "kv exists" subcommand.
func newKVExistsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "exists <key>",
		Short: "Check if a key exists",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			_, err := client.KV().Get(cmd.Context(), key)
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
}

// newKVRangeCmd creates the "kv range" subcommand for MVCC range queries.
func newKVRangeCmd() *cobra.Command {
	var (
		limit     int64
		revision  int64
		countOnly bool
		keysOnly  bool
		sortOrder string
		sortBy    string
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

			resp, err := client.MVCC().Range(cmd.Context(), start, end, opts)
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

	return cmd
}

// newKVCompactCmd creates the "kv compact" subcommand.
func newKVCompactCmd() *cobra.Command {
	var revision int64

	cmd := &cobra.Command{
		Use:   "compact",
		Short: "Compact MVCC revisions",
		Long:  "Discards all MVCC revisions older than the specified revision.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := client.MVCC().Compact(cmd.Context(), revision); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}

	cmd.Flags().Int64Var(&revision, "revision", 0, "Compact revisions older than this value")
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
