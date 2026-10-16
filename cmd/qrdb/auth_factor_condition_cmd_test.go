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

package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// factorConditionCLI runs CLI invocations against the real factor service
// over a store with guarded writes, so the condition is decided by the code
// that decides it in production rather than by a test double. Every run
// builds a fresh root: cobra keeps flag values across executions of one.
type factorConditionCLI struct {
	t  *testing.T
	tr *embedded.Transport
}

func newFactorConditionCLI(t *testing.T) *factorConditionCLI {
	t.Helper()
	svc, err := service.NewUserAuthFactorService(service.UserAuthFactorServiceConfig{KV: kvstore.NewMemoryKVStore()})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	tr, err := embedded.New(newMockKVService(), newMockAdminService(), embedded.WithUserAuthFactorService(svc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	return &factorConditionCLI{t: t, tr: tr}
}

func (c *factorConditionCLI) run(args ...string) (string, error) {
	c.t.Helper()
	root := newRootCmd()
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		format, _ := cmd.Root().PersistentFlags().GetString("output")
		if err := validateOutputFormat(format); err != nil {
			return err
		}
		outputFormat = format
		cl, err := sdk.NewClient(c.tr)
		if err != nil {
			return err
		}
		if err := cl.Connect(cmd.Context()); err != nil {
			return err
		}
		client = cl
		return nil
	}
	return executeCommand(root, args...)
}

// add enrols a fido2 factor for user 42 with credential cred plus extra
// flags, and decodes the JSON the add prints.
func (c *factorConditionCLI) add(cred string, extra ...string) (map[string]any, error) {
	c.t.Helper()
	args := append([]string{"--output", "json", "auth", "factor", "add",
		"--user", "42", "--type", "fido2", "--label", "key-" + cred, "--credential-hex", cred}, extra...)
	out, err := c.run(args...)
	if err != nil {
		return nil, err
	}
	var rec map[string]any
	if uerr := json.Unmarshal([]byte(out), &rec); uerr != nil {
		c.t.Fatalf("decode add output %q: %v", out, uerr)
	}
	return rec, nil
}

func TestAuthFactorAdd_ifNoActiveTypeRefusesASecond(t *testing.T) {
	cli := newFactorConditionCLI(t)

	rec, err := cli.add("01", "--if-no-active-type")
	if err != nil {
		t.Fatalf("first conditional add: %v", err)
	}
	if rec["id"] == "" || rec["factor_set_version"] != float64(1) {
		t.Fatalf("add output = %v, want the record at factor_set_version 1", rec)
	}

	_, err = cli.add("02", "--if-no-active-type")
	if !errors.Is(err, auth.ErrFactorConditionFailed) {
		t.Fatalf("second conditional add err = %v, want ErrFactorConditionFailed", err)
	}
	refusal, ok := errors.AsType[*auth.FactorConditionFailedError](err)
	if !ok || refusal.ExistingFactorID != rec["id"] {
		t.Fatalf("refusal = %+v, want it to name %v", refusal, rec["id"])
	}
}

func TestAuthFactorAdd_expectedVersionAndList(t *testing.T) {
	cli := newFactorConditionCLI(t)

	if _, err := cli.add("01", "--expected-version", "0"); err != nil {
		t.Fatalf("add at version 0: %v", err)
	}
	if _, err := cli.add("02", "--expected-version", "0"); !errors.Is(err, auth.ErrFactorConditionFailed) {
		t.Fatalf("stale add err = %v, want ErrFactorConditionFailed", err)
	}

	out, err := cli.run("auth", "factor", "list", "--user", "42")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "Factor set version: 1") {
		t.Fatalf("list output %q does not report version 1", out)
	}

	rec, err := cli.add("03", "--expected-version", "1")
	if err != nil || rec["factor_set_version"] != float64(2) {
		t.Fatalf("add at version 1 = %v, %v; want factor_set_version 2", rec, err)
	}
}

func TestAuthFactorAdd_unconditionalKeepsTheRecordShape(t *testing.T) {
	cli := newFactorConditionCLI(t)

	rec, err := cli.add("01")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, ok := rec["factor_set_version"]; ok {
		t.Fatalf("unconditional add output %v reports a factor-set version", rec)
	}
	if rec["id"] == "" || rec["type"] != "fido2" {
		t.Fatalf("add output %v is not the factor record", rec)
	}
}

func TestAuthFactorAdd_conditionalTextAndYAMLReportTheVersion(t *testing.T) {
	cli := newFactorConditionCLI(t)

	out, err := cli.run("auth", "factor", "add", "--user", "42", "--type", "fido2",
		"--label", "text", "--credential-hex", "01", "--if-no-active-type")
	if err != nil || !strings.Contains(out, "Factor Set:    version 1") {
		t.Fatalf("text add = %q, %v; want the version line", out, err)
	}

	out, err = cli.run("--output", "yaml", "auth", "factor", "add", "--user", "42", "--type", "fido2",
		"--label", "yaml", "--credential-hex", "02", "--expected-version", "1")
	if err != nil || !strings.Contains(out, "factorsetversion: 2") || !strings.Contains(out, "id: ") {
		t.Fatalf("yaml add = %q, %v; want the record inline with factorsetversion 2", out, err)
	}
}

func TestAuthFactorRotate_takesNoAddCondition(t *testing.T) {
	cli := newFactorConditionCLI(t)

	for _, flag := range []string{"--if-no-active-type", "--expected-version=0"} {
		_, err := cli.run("auth", "factor", "rotate", "factor-1", "--type", "fido2",
			"--label", "next", "--credential-hex", "02", flag)
		if err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Fatalf("rotate %s err = %v, want an unknown-flag error", flag, err)
		}
	}
}
