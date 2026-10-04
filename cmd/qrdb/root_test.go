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
//
// isCompletionCmd is anchored at exactly two levels below the ROOT command
// (root's direct "completion" child, and that child's own direct children)
// rather than walking the whole parent chain, so every tree built here
// attaches "completion" under a real root -- an unparented "completion"
// standalone from any root is not the shape isCompletionCmd is asked about
// in production (root.go always calls it with a command cobra resolved
// from the real rootCmd tree).
package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestIsCompletionCmd_MatchesCompletionAndItsChildren asserts isCompletionCmd
// recognizes the root's direct "completion" child and each of ITS direct
// per-shell children.
func TestIsCompletionCmd_MatchesCompletionAndItsChildren(t *testing.T) {
	root := &cobra.Command{Use: "qrdb"}
	completion := &cobra.Command{Use: "completion"}
	root.AddCommand(completion)
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
// not match a command tree that has nothing to do with completion, does not
// match the root itself, and handles a nil command.
func TestIsCompletionCmd_RejectsUnrelatedCommands(t *testing.T) {
	root := &cobra.Command{Use: "qrdb"}
	tenant := &cobra.Command{Use: "tenant"}
	root.AddCommand(tenant)
	create := &cobra.Command{Use: "create"}
	tenant.AddCommand(create)

	if isCompletionCmd(create) {
		t.Error("isCompletionCmd(tenant create) = true, want false")
	}
	if isCompletionCmd(root) {
		t.Error("isCompletionCmd(root) = true, want false")
	}
	if isCompletionCmd(nil) {
		t.Error("isCompletionCmd(nil) = true, want false")
	}
}

// TestIsCompletionCmd_RejectsNonRootAnchoredCompletion proves the tightened
// semantics the name promises: a command named "completion" (or one of its
// children) that is NOT anchored at the root -- a grandchild of the root
// rather than a direct child, or deeper still -- must NOT match, unlike the
// earlier whole-parent-chain walk this predicate used to do.
func TestIsCompletionCmd_RejectsNonRootAnchoredCompletion(t *testing.T) {
	root := &cobra.Command{Use: "qrdb"}
	tenant := &cobra.Command{Use: "tenant"}
	root.AddCommand(tenant)

	// "completion" two levels below root (a grandchild), not the root's own
	// direct child.
	nestedCompletion := &cobra.Command{Use: "completion"}
	tenant.AddCommand(nestedCompletion)
	if isCompletionCmd(nestedCompletion) {
		t.Error("isCompletionCmd(tenant completion) = true, want false -- not anchored at the root")
	}

	// A child of that nested "completion" (three levels below root) must
	// also be rejected: isCompletionCmd only reaches one level past a
	// ROOT-anchored completion, not past one anchored anywhere else.
	nestedChild := &cobra.Command{Use: "bash"}
	nestedCompletion.AddCommand(nestedChild)
	if isCompletionCmd(nestedChild) {
		t.Error("isCompletionCmd(tenant completion bash) = true, want false -- not anchored at the root")
	}
}
