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

package mcp

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// guardMCPAllowAuthorizer permits everything and counts calls. Carries no
// build constraint: tool_mapping_guard_test.go (codec_json && raft) and
// replica_forward_test.go (unconstrained) both construct it, so its method
// set must not straddle a build tag (see CLAUDE.md "Build constraints on
// test files").
type guardMCPAllowAuthorizer struct {
	calls int
}

func (a *guardMCPAllowAuthorizer) Authorize(context.Context, *auth.Identity, string, string) error {
	a.calls++
	return nil
}
