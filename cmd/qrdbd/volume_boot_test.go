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

//go:build !integration

package main

import (
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// TestApplyConfigToFlags_AllowDegradedVolumeBoot: the config file's
// storage.allow_degraded_volume_boot sets the flag when the command line
// did not, and an explicit --allow-degraded-volume-boot on the command
// line wins over the config file (owner ruling 2026-10-06).
func TestApplyConfigToFlags_AllowDegradedVolumeBoot(t *testing.T) {
	cmd, f := newStartCmdWithFlags()
	if f.allowDegradedVolumeBoot || f.storageConfig.AllowDegradedVolumeBoot {
		t.Fatal("allow-degraded-volume-boot must default to false")
	}

	cfg := &qrdbconfig.Config{Storage: qrdbconfig.StorageConfig{AllowDegradedVolumeBoot: true}}
	applyConfigToFlags(cmd, f, cfg)
	if !f.storageConfig.AllowDegradedVolumeBoot {
		t.Fatal("the config file's allow_degraded_volume_boot must be honored when the flag was not set")
	}

	// An explicit --allow-degraded-volume-boot=false on the command line
	// overrides a config file that turned it on.
	cmd, f = newStartCmdWithFlags()
	if err := cmd.Flags().Set("allow-degraded-volume-boot", "false"); err != nil {
		t.Fatal(err)
	}
	applyConfigToFlags(cmd, f, cfg)
	if f.storageConfig.AllowDegradedVolumeBoot {
		t.Fatal("an explicit command-line flag must win over the config file")
	}
}
