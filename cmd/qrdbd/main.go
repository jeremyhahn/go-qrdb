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

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
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
// os.Exit call so that every deferred cleanup in run — above all the mlocked
// memory wipe — actually runs before the process leaves.
func main() {
	os.Exit(run())
}

// run is the daemon body and returns the process exit code. It is intentionally
// excluded from unit test coverage: integration tests exercise the full command
// surface via cobra subcommands instead.
//
// The first call is secure.EnablePurge() — it arms the secure subsystem
// so the deferred Purge below can run, WITHOUT installing memguard's
// process signal handler. qrdbd owns its own signal handling: runStart
// uses signal.NotifyContext(SIGINT, SIGTERM) to drive a graceful
// shutdown and then returns nil, so cobra returns nil and run returns 0 —
// the behavior systemd/docker/k8s expect on SIGTERM. (memguard's
// CatchSignal would instead race that path and hard os.Exit(1).) The
// matching deferred secure.Purge() synchronously wipes mlocked memory on
// every orderly exit path.
func run() int {
	secure.EnablePurge()
	defer func() {
		if err := secure.Purge(); err != nil {
			fmt.Fprintf(os.Stderr, "qrdbd: secure.Purge: %v\n", err)
		}
	}()

	rootCmd := &cobra.Command{
		Use:   "qrdbd",
		Short: "QuicRaft DB -- distributed key-value store daemon",
		Long:  "QuicRaft DB daemon is a high-performance distributed key-value store powered by multi-group Raft consensus with QUIC transport.",
	}

	rootCmd.AddCommand(newStartCmd())
	rootCmd.AddCommand(newStatusCmd())
	rootCmd.AddCommand(newPeersCmd())
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
