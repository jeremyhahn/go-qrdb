//go:build qrdb_vlog

// Copyright 2026 Jeremy Hahn
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

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// vlogCLI runs qrdb vlog verbs against one embedded client over a real,
// in-memory log service, so a sequence of verbs sees one log's state.
type vlogCLI struct {
	t *testing.T
}

// newVlogCLI connects the package client to an embedded transport serving
// servicetest's log service and restores the package globals on cleanup.
func newVlogCLI(t *testing.T) *vlogCLI {
	t.Helper()

	tr, err := embedded.New(newMockKVService(), newMockAdminService(), embedded.WithLogService(servicetest.NewLogService(t)))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := cl.Connect(context.Background()); err != nil {
		t.Fatalf("client.Connect: %v", err)
	}

	oldClient, oldWriter, oldFormat := client, outputWriter, outputFormat
	t.Cleanup(func() {
		_ = cl.Close()
		client, outputWriter, outputFormat = oldClient, oldWriter, oldFormat
	})
	client = cl
	return &vlogCLI{t: t}
}

// run executes one qrdb vlog invocation in the given output format and
// returns what it printed.
func (c *vlogCLI) run(format string, args ...string) (string, error) {
	c.t.Helper()
	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = format

	cmd := newVlogCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return buf.String(), err
}

// must runs a verb in table format and fails the test on an error.
func (c *vlogCLI) must(args ...string) string {
	c.t.Helper()
	out, err := c.run("table", args...)
	if err != nil {
		c.t.Fatalf("qrdb vlog %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// seed creates a signed log named audit holding the given entries.
func (c *vlogCLI) seed(entries ...string) {
	c.t.Helper()
	c.must("create", "--log", "audit", "--origin", servicetest.LogOrigin, "--signing-key-type", "ed25519", "--allow-expire-bodies")
	if len(entries) == 0 {
		return
	}
	args := []string{"append", "--log", "audit"}
	for _, e := range entries {
		args = append(args, "--entry", e)
	}
	c.must(args...)
}

func requireContains(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("output %q does not contain %q", out, w)
		}
	}
}

func TestParseVlogSigningKeyType_AcceptsEveryKnownType(t *testing.T) {
	for name, want := range vlogSigningKeyTypes {
		got, err := parseVlogSigningKeyType(name, false)
		if err != nil || got != want {
			t.Fatalf("parseVlogSigningKeyType(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
	got, err := parseVlogSigningKeyType("", true)
	if err != nil || got != "" {
		t.Fatalf("optional empty = %q, %v; want empty, nil", got, err)
	}
}

func TestParseVlogSigningKeyType_RejectsUnknownAndRequiredEmpty(t *testing.T) {
	for _, name := range []string{"rsa", ""} {
		_, err := parseVlogSigningKeyType(name, false)
		var ife *InvalidFlagValueError
		if !errors.As(err, &ife) || ife.Flag != "signing-key-type" {
			t.Fatalf("parseVlogSigningKeyType(%q) error = %v; want *InvalidFlagValueError", name, err)
		}
	}
}

func TestPrintVlogResult_TableRunsTheTextPrinter(t *testing.T) {
	var buf bytes.Buffer
	oldWriter, oldFormat := outputWriter, outputFormat
	t.Cleanup(func() { outputWriter, outputFormat = oldWriter, oldFormat })
	outputWriter, outputFormat = &buf, "table"

	called := false
	if err := printVlogResult(struct{}{}, func() { called = true }); err != nil || !called {
		t.Fatalf("printVlogResult table: called=%v err=%v", called, err)
	}
}

func TestPrintVlogResult_StructuredFormatsSkipTheTextPrinter(t *testing.T) {
	oldWriter, oldFormat := outputWriter, outputFormat
	t.Cleanup(func() { outputWriter, outputFormat = oldWriter, oldFormat })
	for format, want := range map[string]string{"json": `"expired": 3`, "yaml": "expired: 3"} {
		var buf bytes.Buffer
		outputWriter, outputFormat = &buf, format
		err := printVlogResult(&vlogwire.ExpireResult{Expired: 3}, func() { t.Fatalf("%s ran the text printer", format) })
		if err != nil {
			t.Fatalf("printVlogResult %s: %v", format, err)
		}
		requireContains(t, buf.String(), want)
	}
}

func TestPrintVlogKey_PrintsVersionAndHexKey(t *testing.T) {
	var buf bytes.Buffer
	oldWriter := outputWriter
	t.Cleanup(func() { outputWriter = oldWriter })
	outputWriter = &buf

	printVlogKey(&vlogwire.HeadKey{Version: 2, PublicKey: []byte{0xab, 0xcd}})
	requireContains(t, buf.String(), "Key version: 2", "Public key:  abcd")
}

func TestPrintVlogKey_NilPrintsNothing(t *testing.T) {
	var buf bytes.Buffer
	oldWriter := outputWriter
	t.Cleanup(func() { outputWriter = oldWriter })
	outputWriter = &buf

	printVlogKey(nil)
	if buf.Len() != 0 {
		t.Fatalf("printVlogKey(nil) printed %q", buf.String())
	}
}

func TestPrintVlogHashes_OneHexLinePerHash(t *testing.T) {
	var buf bytes.Buffer
	oldWriter := outputWriter
	t.Cleanup(func() { outputWriter = oldWriter })
	outputWriter = &buf

	printVlogHashes([][]byte{{0x01}, {0xff, 0x00}})
	if got := buf.String(); got != "01\nff00\n" {
		t.Fatalf("printVlogHashes = %q", got)
	}
}

func TestPrintVlogHashes_EmptyPrintsNothing(t *testing.T) {
	var buf bytes.Buffer
	oldWriter := outputWriter
	t.Cleanup(func() { outputWriter = oldWriter })
	outputWriter = &buf

	printVlogHashes(nil)
	if buf.Len() != 0 {
		t.Fatalf("printVlogHashes(nil) printed %q", buf.String())
	}
}

func TestNewVlogCmd_RegistersTheTenVerbs(t *testing.T) {
	cmd := newVlogCmd()
	want := []string{"append", "create", "expire", "get", "head", "list", "prove-consistency", "prove-inclusion", "rotate-key", "sign-head"}
	var got []string
	for _, sub := range cmd.Commands() {
		got = append(got, sub.Name())
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("vlog verbs = %v; want %v", got, want)
	}
}

func TestNewVlogCmd_IsRegisteredOnTheRoot(t *testing.T) {
	sub, _, err := newRootCmd().Find([]string{"vlog", "head"})
	if err != nil || sub.Name() != "head" {
		t.Fatalf("root does not route vlog head: %v", err)
	}
}

func TestVlogCreate_PrintsLogDatabaseAndKey(t *testing.T) {
	cli := newVlogCLI(t)
	out := cli.must("create", "--log", "audit", "--origin", servicetest.LogOrigin, "--signing-key-type", "ed25519")
	requireContains(t, out, "Log:         audit", "Database:    system", "Key version: 1")
}

func TestVlogCreate_RejectsAnUnknownKeyTypeAndADuplicate(t *testing.T) {
	cli := newVlogCLI(t)
	if _, err := cli.run("table", "create", "--log", "audit", "--signing-key-type", "rsa"); err == nil {
		t.Fatal("create with an unknown key type succeeded")
	}
	cli.must("create", "--log", "audit", "--origin", servicetest.LogOrigin)
	_, err := cli.run("table", "create", "--log", "audit", "--origin", servicetest.LogOrigin)
	if exists := (*vlog.LogExistsError)(nil); !errors.As(err, &exists) || exists.ID.Log != "audit" {
		t.Fatalf("second create error = %v (%T); want *vlog.LogExistsError naming audit", err, err)
	}
}

func TestVlogList_PrintsEveryLog(t *testing.T) {
	cli := newVlogCLI(t)
	cli.must("create", "--log", "audit", "--origin", servicetest.LogOrigin)
	cli.must("create", "--log", "events", "--origin", servicetest.LogOrigin)
	requireContains(t, cli.must("list"), "audit\n", "events\n")
}

func TestVlogList_JSONCarriesTheLogs(t *testing.T) {
	cli := newVlogCLI(t)
	cli.must("create", "--log", "audit", "--origin", servicetest.LogOrigin)
	out, err := cli.run("json", "list")
	if err != nil {
		t.Fatalf("list json: %v", err)
	}
	var resp vlogwire.ListResult
	if err := json.Unmarshal([]byte(out), &resp); err != nil || len(resp.Logs) != 1 || resp.Logs[0] != "audit" {
		t.Fatalf("list json = %q (%v)", out, err)
	}
}

func TestVlogAppend_PrintsFirstIndexAndCount(t *testing.T) {
	cli := newVlogCLI(t)
	cli.seed("a", "b")
	requireContains(t, cli.must("append", "--log", "audit", "--entry", "c"), "First index: 2", "Count:       1")
}

func TestVlogAppend_MissingLogIsNotFound(t *testing.T) {
	cli := newVlogCLI(t)
	_, err := cli.run("table", "append", "--log", "absent", "--entry", "x")
	var nf *vlog.LogNotFoundError
	if !errors.As(err, &nf) || nf.ID.Log != "absent" {
		t.Fatalf("append to a missing log error = %v; want *vlog.LogNotFoundError", err)
	}
	if _, err := cli.run("table", "append", "--log", "absent"); err == nil {
		t.Fatal("append without --entry succeeded")
	}
}

func TestVlogGet_PrintsIndexedBodies(t *testing.T) {
	cli := newVlogCLI(t)
	cli.seed("a", "b", "c")
	out := cli.must("get", "--log", "audit", "--from", "1", "--limit", "2")
	if out != "1\tb\n2\tc\n" {
		t.Fatalf("get = %q", out)
	}
}

func TestVlogGet_MarksExpiredEntries(t *testing.T) {
	cli := newVlogCLI(t)
	cli.seed("a", "b")
	cli.must("expire", "--log", "audit", "--cutoff", "1")
	requireContains(t, cli.must("get", "--log", "audit"), "0\t(expired)\n", "1\tb\n")
}

func TestVlogProveInclusion_PrintsTheAuditPath(t *testing.T) {
	cli := newVlogCLI(t)
	cli.seed("a", "b")
	out := cli.must("prove-inclusion", "--log", "audit", "--index", "0", "--size", "2")
	requireContains(t, out, "Index: 0", "Size:  2")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if _, err := hex.DecodeString(lines[len(lines)-1]); err != nil || len(lines) != 3 {
		t.Fatalf("prove-inclusion = %q; want two header lines and one hash", out)
	}
}

func TestVlogProveInclusion_IndexBeyondTheTreeFails(t *testing.T) {
	cli := newVlogCLI(t)
	cli.seed("a")
	if _, err := cli.run("table", "prove-inclusion", "--log", "audit", "--index", "5", "--size", "1"); err == nil {
		t.Fatal("prove-inclusion of an index beyond the tree succeeded")
	}
}

func TestVlogProveConsistency_PrintsTheProof(t *testing.T) {
	cli := newVlogCLI(t)
	cli.seed("a", "b", "c")
	out := cli.must("prove-consistency", "--log", "audit", "--size1", "1", "--size2", "3")
	requireContains(t, out, "Size1: 1", "Size2: 3")
}

func TestVlogProveConsistency_SizeBeyondTheTreeFails(t *testing.T) {
	cli := newVlogCLI(t)
	cli.seed("a")
	if _, err := cli.run("table", "prove-consistency", "--log", "audit", "--size1", "1", "--size2", "9"); err == nil {
		t.Fatal("prove-consistency beyond the tree succeeded")
	}
}

func TestVlogHead_PrintsSizeRootAndCheckpoint(t *testing.T) {
	cli := newVlogCLI(t)
	cli.seed("a", "b")
	cli.must("sign-head", "--log", "audit")
	out := cli.must("head", "--log", "audit")
	requireContains(t, out, "Size:            2", "Checkpoint size: 2", servicetest.LogOrigin)
}

func TestVlogHead_MissingLogIsNotFound(t *testing.T) {
	cli := newVlogCLI(t)
	_, err := cli.run("table", "head", "--log", "absent")
	var nf *vlog.LogNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("head of a missing log error = %v; want *vlog.LogNotFoundError", err)
	}
}

func TestVlogSignHead_PrintsTheCheckpoint(t *testing.T) {
	cli := newVlogCLI(t)
	cli.seed("a")
	out := cli.must("sign-head", "--log", "audit")
	requireContains(t, out, servicetest.LogOrigin+"\n1\n", "— ")
}

func TestVlogSignHead_SecondSignAtTheSameSizeIsRefused(t *testing.T) {
	cli := newVlogCLI(t)
	cli.seed("a")
	cli.must("sign-head", "--log", "audit")
	_, err := cli.run("table", "sign-head", "--log", "audit")
	if exists := (*vlog.CheckpointExistsError)(nil); !errors.As(err, &exists) || exists.Size != 1 {
		t.Fatalf("second sign-head error = %v (%T); want *vlog.CheckpointExistsError at size 1", err, err)
	}
	cli.must("append", "--log", "audit", "--entry", "b")
	cli.must("sign-head", "--log", "audit")
}

func TestVlogRotateKey_AdvancesTheKeyVersion(t *testing.T) {
	cli := newVlogCLI(t)
	cli.seed()
	requireContains(t, cli.must("rotate-key", "--log", "audit", "--signing-key-type", "ecdsa-p256-sha256"), "Key version: 2")
}

func TestVlogRotateKey_RejectsAnUnknownKeyType(t *testing.T) {
	cli := newVlogCLI(t)
	cli.seed()
	if _, err := cli.run("table", "rotate-key", "--log", "audit", "--signing-key-type", "rsa"); err == nil {
		t.Fatal("rotate-key with an unknown key type succeeded")
	}
}

func TestVlogExpire_PrintsTheExpiredCount(t *testing.T) {
	cli := newVlogCLI(t)
	cli.seed("a", "b", "c")
	requireContains(t, cli.must("expire", "--log", "audit", "--cutoff", "2"), "Expired: 2")
}

func TestVlogExpire_RefusedWhenTheLogDoesNotAllowIt(t *testing.T) {
	cli := newVlogCLI(t)
	cli.must("create", "--log", "audit", "--origin", servicetest.LogOrigin)
	cli.must("append", "--log", "audit", "--entry", "a")
	_, err := cli.run("table", "expire", "--log", "audit", "--cutoff", "1")
	if refused := (*vlog.ExpireNotAllowedError)(nil); !errors.As(err, &refused) || refused.ID.Log != "audit" {
		t.Fatalf("expire on a log without --allow-expire-bodies error = %v (%T); want *vlog.ExpireNotAllowedError", err, err)
	}
}
