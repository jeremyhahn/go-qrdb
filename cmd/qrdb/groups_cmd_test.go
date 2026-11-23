// Copyright (c) 2026 Jeremy Hahn
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

package main

import "testing"

// The groups commands' argument validation runs before any client call; the
// server round trips are covered by test/integration/api/cli_groups_test.go
// on every protocol.
func TestGroupsCmd_ArgumentValidation(t *testing.T) {
	cases := map[string][]string{
		"create without --name":       {"groups", "create"},
		"get without id":              {"groups", "get"},
		"get-by-name without name":    {"groups", "get-by-name"},
		"update without id":           {"groups", "update"},
		"delete without id":           {"groups", "delete"},
		"add-member without --group":  {"groups", "add-member", "--user", "1"},
		"add-member without --user":   {"groups", "add-member", "--group", "g1"},
		"remove-member without flags": {"groups", "remove-member"},
		"members without group id":    {"groups", "members"},
		"list with a positional":      {"groups", "list", "extra"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := executeCommand(newTestRootCmdAuth(), args...); err == nil {
				t.Fatalf("%v: expected an error", args)
			}
		})
	}
}
