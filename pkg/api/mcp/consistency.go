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
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// readOptsFromArg parses the optional "consistency" string arg from a tool
// request. An absent or empty string returns (zero, false, nil), meaning the
// caller should use the plain (default) service method. A non-empty but
// unrecognised value returns a non-nil error. A recognised value returns the
// corresponding ReadOptions with AllowStale set when the level is Eventual.
func readOptsFromArg(req mcp.CallToolRequest) (consistency.ReadOptions, bool, error) {
	s := mcp.ParseString(req, "consistency", "")
	if s == "" {
		return consistency.ReadOptions{}, false, nil
	}
	level, err := consistency.ParseLevel(s)
	if err != nil {
		return consistency.ReadOptions{}, false, err
	}
	return consistency.ReadOptions{
		Consistency: level,
		AllowStale:  level == consistency.Eventual,
	}, true, nil
}

// writeModeFromArg parses the optional "write_mode" string arg from a tool
// request. An absent or empty string returns (zero, false, nil), meaning the
// caller should use the plain (default) service method. A non-empty but
// unrecognised value returns a non-nil error.
func writeModeFromArg(req mcp.CallToolRequest) (writemode.Mode, bool, error) {
	s := mcp.ParseString(req, "write_mode", "")
	if s == "" {
		return writemode.Sync, false, nil
	}
	mode, err := writemode.FromString(s)
	if err != nil {
		return writemode.Sync, false, err
	}
	return mode, true, nil
}
