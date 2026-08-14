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
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newLockCmd returns the lock parent command with subcommands for
// distributed lock management: acquire, trylock, release, extend, status,
// and list.
func newLockCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lock",
		Short: "Distributed lock management",
		Long:  "Acquire, release, extend, inspect, and list distributed locks with fencing tokens.",
	}

	cmd.AddCommand(
		newLockAcquireCmd(),
		newLockTryLockCmd(),
		newLockReleaseCmd(),
		newLockExtendCmd(),
		newLockStatusCmd(),
		newLockListCmd(),
	)

	return cmd
}

// newLockAcquireCmd returns the lock acquire subcommand. It blocks until
// the lock is acquired or the context is cancelled.
func newLockAcquireCmd() *cobra.Command {
	var (
		ttl       int64
		holderID  string
		reentrant bool
		timeout   int64
	)

	cmd := &cobra.Command{
		Use:   "acquire <key>",
		Short: "Acquire a distributed lock, blocking until available",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
				defer cancel()
			}

			info, err := client.Lock().LockAcquire(ctx, &transport.LockAcquireRequest{
				Key:       args[0],
				HolderID:  holderID,
				TTL:       ttl,
				Reentrant: reentrant,
			})
			if err != nil {
				return err
			}

			return printLockInfo(info)
		},
	}

	cmd.Flags().Int64Var(&ttl, "ttl", 0, "Lock TTL in seconds")
	_ = cmd.MarkFlagRequired("ttl")
	cmd.Flags().StringVar(&holderID, "holder", "", "Holder identifier (default: auto-generated UUID)")
	cmd.Flags().BoolVar(&reentrant, "reentrant", false, "Allow re-acquisition by the same holder")
	cmd.Flags().Int64Var(&timeout, "wait", 0, "Acquisition wait timeout in seconds (0 = use request timeout)")

	return cmd
}

// newLockTryLockCmd returns the lock trylock subcommand. It attempts to
// acquire the lock immediately without blocking.
func newLockTryLockCmd() *cobra.Command {
	var (
		ttl       int64
		holderID  string
		reentrant bool
	)

	cmd := &cobra.Command{
		Use:   "trylock <key>",
		Short: "Attempt to acquire a lock without blocking",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := client.Lock().LockTryLock(cmd.Context(), &transport.LockAcquireRequest{
				Key:       args[0],
				HolderID:  holderID,
				TTL:       ttl,
				Reentrant: reentrant,
			})
			if err != nil {
				return err
			}

			return printLockInfo(info)
		},
	}

	cmd.Flags().Int64Var(&ttl, "ttl", 0, "Lock TTL in seconds")
	_ = cmd.MarkFlagRequired("ttl")
	cmd.Flags().StringVar(&holderID, "holder", "", "Holder identifier (default: auto-generated UUID)")
	cmd.Flags().BoolVar(&reentrant, "reentrant", false, "Allow re-acquisition by the same holder")

	return cmd
}

// newLockReleaseCmd returns the lock release subcommand. It releases the
// lock identified by key and fencing token.
func newLockReleaseCmd() *cobra.Command {
	var token int64

	cmd := &cobra.Command{
		Use:   "release <key>",
		Short: "Release a held distributed lock",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.Lock().LockRelease(cmd.Context(), &transport.LockReleaseRequest{
				Key:          args[0],
				FencingToken: token,
			}); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}

	cmd.Flags().Int64Var(&token, "token", 0, "Fencing token returned at acquisition")
	_ = cmd.MarkFlagRequired("token")

	return cmd
}

// newLockExtendCmd returns the lock extend subcommand. It refreshes the
// TTL on a held lock without releasing it.
func newLockExtendCmd() *cobra.Command {
	var token int64

	cmd := &cobra.Command{
		Use:   "extend <key>",
		Short: "Refresh the TTL on a held distributed lock",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := client.Lock().LockExtend(cmd.Context(), &transport.LockExtendRequest{
				Key:          args[0],
				FencingToken: token,
			})
			if err != nil {
				return err
			}

			return printLockInfo(info)
		},
	}

	cmd.Flags().Int64Var(&token, "token", 0, "Fencing token returned at acquisition")
	_ = cmd.MarkFlagRequired("token")

	return cmd
}

// newLockStatusCmd returns the lock status subcommand. It shows the
// current state of a lock.
func newLockStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <key>",
		Short: "Show the current state of a distributed lock",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := client.Lock().LockStatus(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if info == nil {
				_, _ = fmt.Fprintln(outputWriter, "not locked")
				return nil
			}

			return printLockInfo(info)
		},
	}
}

// newLockListCmd returns the lock list subcommand. It lists all active locks.
func newLockListCmd() *cobra.Command {
	var prefix string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all active distributed locks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Lock().LockList(cmd.Context(), prefix)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				headers := []string{"KEY", "FENCING_TOKEN", "HOLDER_ID", "TTL", "REENTRANT", "ACQUIRE_COUNT"}
				var rows [][]string
				for _, l := range resp.Locks {
					rows = append(rows, lockInfoRow(l))
				}
				printTable(headers, rows)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&prefix, "prefix", "", "Filter locks by key prefix")

	return cmd
}

// printLockInfo prints a single LockInfo in the current output format.
func printLockInfo(info *transport.LockInfo) error {
	switch outputFormat {
	case "json":
		return printJSON(info)
	case "yaml":
		return printYAML(info)
	default:
		_, _ = fmt.Fprintf(outputWriter, "Key:          %s\n", info.Key)
		_, _ = fmt.Fprintf(outputWriter, "FencingToken: %s\n", strconv.FormatInt(info.FencingToken, 10))
		_, _ = fmt.Fprintf(outputWriter, "LeaseID:      %s\n", strconv.FormatInt(info.LeaseID, 10))
		_, _ = fmt.Fprintf(outputWriter, "HolderID:     %s\n", info.HolderID)
		_, _ = fmt.Fprintf(outputWriter, "Reentrant:    %v\n", info.Reentrant)
		_, _ = fmt.Fprintf(outputWriter, "AcquireCount: %s\n", strconv.FormatInt(int64(info.AcquireCount), 10))
		_, _ = fmt.Fprintf(outputWriter, "TTL:          %s\n", strconv.FormatInt(info.TTL, 10))
		_, _ = fmt.Fprintf(outputWriter, "AcquiredAt:   %s\n", strconv.FormatInt(info.AcquiredAt, 10))
		return nil
	}
}

// lockInfoRow converts a LockInfo into a table row.
func lockInfoRow(info *transport.LockInfo) []string {
	return []string{
		info.Key,
		strconv.FormatInt(info.FencingToken, 10),
		info.HolderID,
		strconv.FormatInt(info.TTL, 10),
		strconv.FormatBool(info.Reentrant),
		strconv.FormatInt(int64(info.AcquireCount), 10),
	}
}
