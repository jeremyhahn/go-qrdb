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

//go:build qrdb_vector && !integration

package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestVectorCmd_NoShardIDFlag pins OD-14 A: the binding decides the vector
// shard, so no vector command takes a --shard-id flag.
func TestVectorCmd_NoShardIDFlag(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Flags().Lookup("shard-id") != nil {
			t.Errorf("%s defines --shard-id", c.CommandPath())
		}
		if c.PersistentFlags().Lookup("shard-id") != nil {
			t.Errorf("%s defines a persistent --shard-id", c.CommandPath())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newVectorCmd())
}
