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

// Command historycheck runs the cluster proof's linearizability checker over
// a JSON-lines operation history (the history package's format) and prints
// its history.Result as JSON. It lets a test outside this module -- the root
// module's rolling-upgrade CLI test, which must not import porcupine -- check
// the history it recorded.
//
// Usage: historycheck <history.jsonl>
//
// It exits 0 when the history is linearizable, 1 when the checker found an
// anomaly, and 2 on any error (an unreadable or invalid history, a search
// that ran out of time).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/linearizability"
)

// Exit codes.
const (
	exitValid     = 0
	exitAnomalies = 1
	exitError     = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run checks the history named by args[0], writes the verdict to stdout and
// any error to stderr, and returns the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: historycheck <history.jsonl>")
		return exitError
	}
	res, err := check(args[0])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "historycheck: %v\n", err)
		return exitError
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		_, _ = fmt.Fprintf(stderr, "historycheck: %v\n", err)
		return exitError
	}
	if !res.Valid {
		return exitAnomalies
	}
	return exitValid
}

// check decodes the history at path and runs the linearizability checker.
func check(path string) (history.Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return history.Result{}, err
	}
	defer func() { _ = f.Close() }()
	h, err := history.Decode(f)
	if err != nil {
		return history.Result{}, err
	}
	return linearizability.Checker{}.Check(h)
}
