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

//go:build clusterproof

// Command clusterproof runs the cluster soak (make cluster-proof) and checks
// an evidence bundle with no cluster (make cluster-proof-check). It exits 0
// when the run is proven, 1 when a check found a violation and 2 when the
// configuration, the run or the bundle is invalid. Its clock-rate subcommand
// runs on a replica's own kernel, where the clock-rate nemesis uses it to
// change the kernel's clock rate; it exits as clockrate.Main documents.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/clockrate"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/linearizability"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/soak"
)

// Set by the Makefile's -ldflags.
var (
	version   = "dev"
	gitCommit = "unknown"
)

const usage = `usage: clusterproof run|check  (configured by the PROOF_* environment variables)
       clusterproof clock-rate -not-kernel <boot-id> -check|-ppm <n>`

// commands are the subcommands, each given the arguments after its name.
var commands = map[string]func(ctx context.Context, args []string) int{
	"run":        proofCommand(soak.Main),
	"check":      proofCommand(soak.CheckMain),
	"clock-rate": clockRate,
}

// proofCommand adapts a soak entry point, which takes no arguments and is
// configured by the environment.
func proofCommand(cmd func(context.Context, soak.Env) int) func(context.Context, []string) int {
	return func(ctx context.Context, args []string) int {
		if len(args) != 0 {
			fmt.Fprintln(os.Stderr, usage)
			return soak.ExitInvalid
		}
		return cmd(ctx, soak.Env{
			Getenv:    os.Getenv,
			Stdout:    os.Stdout,
			Stderr:    os.Stderr,
			Harness:   soak.Harness{Version: version, Commit: gitCommit, Go: runtime.Version()},
			Configure: configure,
		})
	}
}

// clockRate changes the clock rate of the kernel it runs on: the clock-rate
// nemesis runs it on a replica through the target's exec template.
func clockRate(_ context.Context, args []string) int {
	return clockrate.Main(args, clockrate.System{}, os.Stdout, os.Stderr)
}

// configure adds the linearizability check, which decides E2, beside the
// ledger. A run and a check of its bundle configure the same checkers, so the
// check re-derives the run's verdict.
func configure(o *soak.Options) error {
	o.Checkers = append(o.Checkers, linearizability.Checker{})
	return nil
}

func main() {
	if len(os.Args) < 2 || commands[os.Args[1]] == nil {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(soak.ExitInvalid)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := commands[os.Args[1]](ctx, os.Args[2:])
	stop()
	os.Exit(code)
}
