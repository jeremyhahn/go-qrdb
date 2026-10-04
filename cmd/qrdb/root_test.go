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

// root_test.go unit-tests isCompletionCmd, the predicate root.go
// PersistentPreRunE uses to skip SDK client construction for cobra built-in
// completion command group -- without it, qrdb completion bash (and its
// zsh/fish/powershell siblings) failed with a connection error on a host
// with no reachable daemon instead of ever printing its script. See
// test/integration/completion_cli_test.go for the same fix proven through
// the real binary.
package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestIsCompletionCmd_MatchesCompletionAndItsChildren asserts isCompletionCmd
// recognizes the completion command itself and each of its per-shell
// children, walking up through the parent chain cobra builds.
func TestIsCompletionCmd_MatchesCompletionAndItsChildren(t *testing.T) {
	completion := &cobra.Command{Use: "completion"}
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		child := &cobra.Command{Use: shell}
		completion.AddCommand(child)
		if !isCompletionCmd(child) {
			t.Errorf("isCompletionCmd(%s) = false, want true", shell)
		}
	}
	if !isCompletionCmd(completion) {
		t.Error("isCompletionCmd(completion) = false, want true")
	}
}

// TestIsCompletionCmd_RejectsUnrelatedCommands asserts isCompletionCmd does
// not match a command tree that has nothing to do with completion, and
// handles a nil command.
func TestIsCompletionCmd_RejectsUnrelatedCommands(t *testing.T) {
	root := &cobra.Command{Use: "qrdb"}
	tenant := &cobra.Command{Use: "tenant"}
	root.AddCommand(tenant)
	create := &cobra.Command{Use: "create"}
	tenant.AddCommand(create)

	if isCompletionCmd(create) {
		t.Error("isCompletionCmd(tenant create) = true, want false")
	}
	if isCompletionCmd(nil) {
		t.Error("isCompletionCmd(nil) = true, want false")
	}
}
