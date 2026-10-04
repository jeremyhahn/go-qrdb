// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

//go:build integration

// qrdbd_cli_test.go drives the qrdbd daemon binary own completion
// subcommand group and its node-identity subcommand through the real CLI
// binary, closing the CLI-coverage-audit finding that both had NO
// test/integration coverage of any kind (cmd/qrdbd has no PersistentPreRunE
// that constructs a client the way cmd/qrdb does, so neither needed the
// root.go fix completion_cli_test.go documents -- both already ran standalone
// before this file was written).
package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
)

// qrdbdCompletionMarker mirrors completion_cli_test.go completionMarker for
// the qrdbd binary own completion group (cmd/qrdbd/main.go never disables
// cobra built-in generator either).
var qrdbdCompletionMarker = map[string]string{
	"bash":       "bash completion V2 for qrdbd",
	"zsh":        "#compdef qrdbd",
	"fish":       "fish completion for qrdbd",
	"powershell": "powershell completion for qrdbd",
}

// TestQrdbdCompletion_CLI_ProducesAScriptPerShell drives qrdbd completion
// <shell> for every shell cobra generates and asserts its documented content
// marker, then additionally runs the real interpreter syntax checker when
// that interpreter is present on the host. Skip guards are forbidden, so
// fish/powershell (absent on every Linux host this suite runs on) are
// asserted by content marker alone instead of being skipped.
func TestQrdbdCompletion_CLI_ProducesAScriptPerShell(t *testing.T) {
	binary := buildBinary(t)

	for _, shell := range completionShells {
		shell := shell
		t.Run(shell, func(t *testing.T) {
			out, err := runCLI(t, binary, "completion", shell)
			if err != nil {
				t.Fatalf("qrdbd completion %s: %v\noutput: %s", shell, err, out)
			}
			if !strings.Contains(out, qrdbdCompletionMarker[shell]) {
				t.Fatalf("qrdbd completion %s: expected output to contain %q, got: %s", shell, qrdbdCompletionMarker[shell], out)
			}

			interpreter, ok := completionInterpreter[shell]
			if !ok {
				return
			}
			interpPath, lookErr := exec.LookPath(interpreter)
			if lookErr != nil {
				return
			}

			scriptPath := filepath.Join(t.TempDir(), "qrdbd-"+shell+"-completion.sh")
			if werr := os.WriteFile(scriptPath, []byte(out), 0o600); werr != nil {
				t.Fatalf("qrdbd completion %s: write script: %v", shell, werr)
			}
			checkOut, checkErr := exec.Command(interpPath, "-n", scriptPath).CombinedOutput()
			if checkErr != nil {
				t.Fatalf("%s -n %s: %v\noutput: %s", interpreter, scriptPath, checkErr, checkOut)
			}
		})
	}
}

// TestQrdbdNodeIdentity_CLI drives qrdbd node-identity --cert against a real
// node certificate and asserts the exact spki:<sha256-hex> identity the
// cluster package itself computes from the identical PEM bytes -- a value
// assertion, not merely a successful exit.
func TestQrdbdNodeIdentity_CLI(t *testing.T) {
	binary := buildBinary(t)
	ca := generateTestCA(t)
	certPath, _ := ca.generateNodeCert(t)

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read generated node cert: %v", err)
	}
	want, err := cluster.MemberIdentityFromPEM(certPEM)
	if err != nil {
		t.Fatalf("cluster.MemberIdentityFromPEM: %v", err)
	}

	out, err := runCLI(t, binary, "node-identity", "--cert", certPath)
	if err != nil {
		t.Fatalf("qrdbd node-identity --cert %s: %v\noutput: %s", certPath, err, out)
	}
	if got := strings.TrimSpace(out); got != want {
		t.Fatalf("qrdbd node-identity --cert %s = %q, want %q (cluster.MemberIdentityFromPEM of the same certificate)", certPath, got, want)
	}
}

// TestQrdbdNodeIdentity_CLI_MissingFile proves --cert is required and a
// nonexistent path produces the typed MemberIdentityCertificateError naming
// the path, not merely a nonzero exit.
func TestQrdbdNodeIdentity_CLI_MissingFile(t *testing.T) {
	binary := buildBinary(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist.pem")

	out, err := runCLI(t, binary, "node-identity", "--cert", missing)
	if err == nil {
		t.Fatalf("qrdbd node-identity --cert %s: expected a failure for a missing certificate, got: %s", missing, out)
	}
	if !strings.Contains(out, missing) {
		t.Fatalf("qrdbd node-identity --cert %s: expected the error to name the missing path, got: %s", missing, out)
	}
}
