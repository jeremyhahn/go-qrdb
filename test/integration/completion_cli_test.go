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

// completion_cli_test.go drives qrdb completion bash/zsh/fish/powershell
// through the real CLI binary, closing the CLI-coverage-audit finding that
// cobra built-in completion script generator had NO test/integration
// coverage of any kind -- and, in the course of writing this test, exposed a
// real defect: root.go PersistentPreRunE tried to build an SDK client
// before any subcommand RunE ran, which is fine for every "real" command
// but meant the completion subcommand could never run at all without a
// reachable daemon. Fixed by root.go isCompletionCmd, which makes
// completion (like the deprecation-redirect stubs) skip client
// construction -- see that commit for the before/after.
//
// No node is started: completion only prints a static script and must work
// with no daemon reachable at all, which this file proves directly by never
// passing --server/--ca-file/etc to the CLI invocations below.
//
// Skip guards are forbidden in this codebase. bash is always present (every
// devcontainer and every host this suite runs on), so its script is
// additionally syntax-checked with `bash -n` UNCONDITIONALLY -- never
// LookPath-guarded, which would silently skip the check the one time bash
// is genuinely missing instead of failing loudly. zsh/fish/powershell are
// NOT guaranteed present, so only their documented content marker is
// asserted for them; that is an assertion every run makes, not a skip.
package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// completionShells is the canonical set of qrdb completion subcommands per
// cobra built-in generator (cmd/qrdb root command never disables it).
var completionShells = []string{"bash", "zsh", "fish", "powershell"}

// completionMarker is the documented first content line of each shell
// generated script (cobra completions package), asserted for every shell
// regardless of whether its interpreter is installed on the host.
var completionMarker = map[string]string{
	"bash":       "bash completion V2 for qrdb",
	"zsh":        "#compdef qrdb",
	"fish":       "fish completion for qrdb",
	"powershell": "powershell completion for qrdb",
}

// TestCompletion_CLI_ProducesAScriptPerShell drives qrdb completion <shell>
// for every shell cobra generates and asserts its documented content
// marker; bash additionally gets its script run through the real `bash -n`
// syntax checker, unconditionally (bash is always present here -- see the
// package doc comment above).
func TestCompletion_CLI_ProducesAScriptPerShell(t *testing.T) {
	cli := buildCLIBinary(t)

	for _, shell := range completionShells {
		shell := shell
		t.Run(shell, func(t *testing.T) {
			out, err := runCLI(t, cli, "completion", shell)
			if err != nil {
				t.Fatalf("qrdb completion %s: %v\noutput: %s", shell, err, out)
			}
			if !strings.Contains(out, completionMarker[shell]) {
				t.Fatalf("qrdb completion %s: expected output to contain %q, got: %s", shell, completionMarker[shell], out)
			}

			if shell != "bash" {
				return
			}

			scriptPath := filepath.Join(t.TempDir(), shell+"-completion.sh")
			if werr := os.WriteFile(scriptPath, []byte(out), 0o600); werr != nil {
				t.Fatalf("qrdb completion %s: write script: %v", shell, werr)
			}
			checkOut, checkErr := exec.Command("bash", "-n", scriptPath).CombinedOutput()
			if checkErr != nil {
				t.Fatalf("bash -n %s: %v\noutput: %s", scriptPath, checkErr, checkOut)
			}
		})
	}
}
