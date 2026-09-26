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

// Package main implements qrdbd, the QuicRaft DB server daemon for
// running distributed key-value store nodes backed by Raft consensus.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version, gitCommit, and buildDate are set at build time via ldflags:
//
//	go build -ldflags "-X main.version=... -X main.gitCommit=... -X main.buildDate=..."
var (
	version   = "dev"
	gitCommit = "unknown"
	buildDate = "unknown"
)

// main is the entry point for the qrdbd daemon. It holds nothing but the
// os.Exit call so that every deferred cleanup along the way — including
// runStart's mlocked-memory wipe on the `start` path — actually runs before
// the process leaves.
func main() {
	os.Exit(run())
}

// run is the daemon body and returns the process exit code. It is intentionally
// excluded from unit test coverage: integration tests exercise the full command
// surface via cobra subcommands instead.
//
// The process-wide locked-memory budget (secure.SetAllocator) and its
// matching secure.WipeAll on the way out belong to `start` alone (see
// runStart) — that is the one subcommand that allocates protected memory at
// all. `status`, `peers` and `version` are read-only client queries against a
// running node with no secret material of their own; requiring them to
// satisfy an mlock budget, or to hold RLIMIT_MEMLOCK headroom they never use,
// would refuse a plain `qrdbd version` on a host sized only for what `start`
// needs (go-qrdb#114 review).
func run() int {
	rootCmd := &cobra.Command{
		Use:   "qrdbd",
		Short: "QuicRaft DB -- distributed key-value store daemon",
		Long:  "QuicRaft DB daemon is a high-performance distributed key-value store powered by multi-group Raft consensus with QUIC transport.",
	}

	rootCmd.AddCommand(newStartCmd())
	rootCmd.AddCommand(newStatusCmd())
	rootCmd.AddCommand(newPeersCmd())
	rootCmd.AddCommand(newNodeIdentityCmd())
	rootCmd.AddCommand(newVersionCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Printf("QuicRaft DB v%s\n", version)
			fmt.Printf("  commit: %s\n", gitCommit)
			fmt.Printf("  built:  %s\n", buildDate)
		},
	}
}
