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

package main

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/clockrate"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/ledger"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/linearizability"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/soak"
)

func TestConfigureAddsTheLinearizabilityChecker(t *testing.T) {
	var o soak.Options
	if err := configure(&o); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if len(o.Checkers) != 1 {
		t.Fatalf("Checkers = %v, want the linearizability checker alone", o.Checkers)
	}
	if _, ok := o.Checkers[0].(linearizability.Checker); !ok {
		t.Fatalf("Checkers[0] = %T, want linearizability.Checker", o.Checkers[0])
	}
}

func TestConfigureKeepsConfiguredCheckers(t *testing.T) {
	o := soak.Options{Checkers: []history.Checker{ledger.Checker{}}}
	if err := configure(&o); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if len(o.Checkers) != 2 {
		t.Fatalf("Checkers = %v, want the configured checker and the linearizability checker", o.Checkers)
	}
	if _, ok := o.Checkers[0].(ledger.Checker); !ok {
		t.Fatalf("Checkers[0] = %T, want the checker already configured", o.Checkers[0])
	}
}

func TestCommands(t *testing.T) {
	for _, name := range []string{"run", "check", "clock-rate"} {
		if commands[name] == nil {
			t.Errorf("no %q subcommand", name)
		}
	}
}

func TestProofCommandsRefuseArguments(t *testing.T) {
	ran := false
	cmd := proofCommand(func(context.Context, soak.Env) int { ran = true; return 0 })
	if code := cmd(context.Background(), []string{"extra"}); code != soak.ExitInvalid || ran {
		t.Fatalf("exit %d, ran %v", code, ran)
	}
	if code := cmd(context.Background(), nil); code != 0 || !ran {
		t.Fatalf("exit %d, ran %v", code, ran)
	}
}

func TestClockRateRefusesBeforeTouchingTheKernel(t *testing.T) {
	// Neither -check nor -ppm, and no harness boot ID: refused as usage
	// before the kernel is read, let alone changed.
	for _, args := range [][]string{nil, {"-ppm", "-5000"}} {
		if code := commands["clock-rate"](context.Background(), args); code != clockrate.ExitUsage {
			t.Errorf("%q: exit %d, want %d", args, code, clockrate.ExitUsage)
		}
	}
}
