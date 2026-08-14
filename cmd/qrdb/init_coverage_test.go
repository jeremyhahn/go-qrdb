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

// init_coverage_test.go covers the runInit success path branches via a mock
// ceremony service that returns a successful CeremonyInitializeResponse.
package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// newTestRootCmdInit creates a root command that uses the ceremony service
// mock for CeremonyInitialize, allowing runInit success paths to be tested.
func newTestRootCmdInit() *cobra.Command {
	rootCmd := newRootCmd()

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Name() == "version" || cmd.Name() == "help" {
			return nil
		}

		format, _ := cmd.Root().PersistentFlags().GetString("output")
		if err := validateOutputFormat(format); err != nil {
			return err
		}
		outputFormat = format

		svc := &mockCeremonyService{}
		tr, err := embedded.New(
			newMockKVService(),
			newMockAdminService(),
			embedded.WithCeremonyService(svc),
			embedded.WithCeremonyCA(&mockCeremonyCA{}),
		)
		if err != nil {
			return err
		}

		c, err := sdk.NewClient(tr)
		if err != nil {
			return err
		}

		if err := c.Connect(cmd.Context()); err != nil {
			return err
		}

		client = c
		return nil
	}

	return rootCmd
}

// ---------------------------------------------------------------------------
// runInit – success path coverage (json/yaml/table output)
// ---------------------------------------------------------------------------

func TestRunInit_SuccessPath_Table(t *testing.T) {
	root := newTestRootCmdInit()
	out, err := executeCommand(root, "init",
		"--so-pin", "sopin",
		"--user-pin", "userpin",
	)
	if err != nil {
		t.Fatalf("runInit table: %v", err)
	}
	if !strings.Contains(out, "State:") {
		t.Errorf("expected State: in output, got: %q", out)
	}
}

func TestRunInit_SuccessPath_JSON(t *testing.T) {
	root := newTestRootCmdInit()
	_, err := executeCommand(root, "--output", "json", "init",
		"--so-pin", "sopin",
		"--user-pin", "userpin",
	)
	if err != nil {
		t.Fatalf("runInit json: %v", err)
	}
}

func TestRunInit_SuccessPath_YAML(t *testing.T) {
	root := newTestRootCmdInit()
	_, err := executeCommand(root, "--output", "yaml", "init",
		"--so-pin", "sopin",
		"--user-pin", "userpin",
	)
	if err != nil {
		t.Fatalf("runInit yaml: %v", err)
	}
}

func TestRunInit_WithSANs_Success(t *testing.T) {
	root := newTestRootCmdInit()
	_, err := executeCommand(root, "init",
		"--so-pin", "sopin",
		"--user-pin", "userpin",
		"--sans", "host1.example.com,host2.example.com",
	)
	if err != nil {
		t.Fatalf("runInit with SANs: %v", err)
	}
}
