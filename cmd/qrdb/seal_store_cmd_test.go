//go:build !integration

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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
	grpctransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/grpc"
)

// newCLITestSealStore returns a real seal store over an initialized,
// unsealed in-memory barrier.
func newCLITestSealStore(t *testing.T) *seal.SealStore {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte("test-master-password"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	barrier, err := seal.NewStorageBarrier(logger, seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "test/root-key"}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	t.Cleanup(func() { _ = barrier.Close() })
	if err := barrier.Initialize(ctx, seal.MustCredentials(ctx, []byte("test-password-123"))); err != nil {
		t.Fatalf("barrier Initialize: %v", err)
	}
	store, err := seal.NewSealStore(barrier, "", logger)
	if err != nil {
		t.Fatalf("NewSealStore: %v", err)
	}
	return store
}

// executeSealStoreCmd runs the qrdb root command over the embedded transport
// wired to store, with stdin as the command's input.
func executeSealStoreCmd(t *testing.T, store *seal.SealStore, stdin string, args ...string) (string, error) {
	t.Helper()
	tr, err := embedded.New(newMockKVService(), newMockAdminService(), embedded.WithSealStore(store))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	return executeSealStoreCmdOn(t, tr, true, stdin, args...)
}

// executeSealStoreCmdOn runs the CLI against tr; connect=false leaves a
// network transport unconnected, which proves its refusal needs no I/O.
func executeSealStoreCmdOn(t *testing.T, tr transport.Transport, connect bool, stdin string, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	oldWriter, oldFormat := outputWriter, outputFormat
	t.Cleanup(func() { outputWriter, outputFormat = oldWriter, oldFormat })
	outputWriter = &buf

	root := newRootCmd()
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		format, err := cmd.Root().PersistentFlags().GetString("output")
		if err != nil {
			return err
		}
		if err := validateOutputFormat(format); err != nil {
			return err
		}
		outputFormat = format
		c, err := sdk.NewClient(tr)
		if err != nil {
			return err
		}
		if connect {
			if err := c.Connect(cmd.Context()); err != nil {
				return err
			}
		}
		client = c
		return nil
	}
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return buf.String(), err
}

func TestSealStoreCmd_PutGetListStatusResealDelete(t *testing.T) {
	store := newCLITestSealStore(t)
	const name, secret = "cli-embedded", "s3cret-value"

	if _, err := executeSealStoreCmd(t, store, secret, "seal-store", "put", name); err != nil {
		t.Fatalf("put: %v", err)
	}

	out, err := executeSealStoreCmd(t, store, "", "seal-store", "get", name)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if strings.TrimSpace(out) != secret {
		t.Fatalf("get text = %q, want %q", out, secret)
	}

	out, err = executeSealStoreCmd(t, store, "", "--output", "json", "seal-store", "get", name)
	if err != nil {
		t.Fatalf("get json: %v", err)
	}
	var got transport.SealStoreGetResponse
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode get json %q: %v", out, err)
	}
	if got.Name != name || string(got.Secret) != secret {
		t.Fatalf("get json = %+v, want name %q secret %q", got, name, secret)
	}

	out, err = executeSealStoreCmd(t, store, "", "seal-store", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, name) {
		t.Fatalf("list %q lacks %q", out, name)
	}

	out, err = executeSealStoreCmd(t, store, "", "seal-store", "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "available: true") || !strings.Contains(out, name) {
		t.Fatalf("status %q lacks availability or %q", out, name)
	}

	if _, err := executeSealStoreCmd(t, store, "", "seal-store", "reseal", name); err != nil {
		t.Fatalf("reseal: %v", err)
	}
	out, err = executeSealStoreCmd(t, store, "", "--output", "json", "seal-store", "delete", name)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	var deleted sealStoreNameResult
	if err := json.Unmarshal([]byte(out), &deleted); err != nil || deleted.Name != name {
		t.Fatalf("delete json %q: %v", out, err)
	}
	if _, err := executeSealStoreCmd(t, store, "", "seal-store", "get", name); err == nil {
		t.Fatal("get after delete succeeded")
	}
}

func TestSealStoreCmd_Refusals(t *testing.T) {
	store := newCLITestSealStore(t)

	_, err := executeSealStoreCmd(t, store, "", "seal-store", "put", "empty")
	var emptyErr *SealStoreEmptySecretError
	if !errors.As(err, &emptyErr) {
		t.Fatalf("put empty: err = %v, want *SealStoreEmptySecretError", err)
	}
	out, err := executeSealStoreCmd(t, store, "", "seal-store", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("list after refused put = %q, want empty", out)
	}

	if _, err := executeSealStoreCmd(t, store, "", "seal-store", "get", "unknown"); err == nil {
		t.Fatal("get of an unknown name succeeded")
	}
}

// TestSealStoreCmd_NetworkTransportEmbeddedOnly: over a network transport
// every seal-store verb fails with the embedded-only refusal naming the
// verb (ruling 1234), so the command exits non-zero.
func TestSealStoreCmd_NetworkTransportEmbeddedOnly(t *testing.T) {
	cases := []struct {
		op    string
		stdin string
		args  []string
	}{
		{transport.OpSealStorePut, "secret", []string{"seal-store", "put", "alpha"}},
		{transport.OpSealStoreGet, "", []string{"seal-store", "get", "alpha"}},
		{transport.OpSealStoreList, "", []string{"seal-store", "list"}},
		{transport.OpSealStoreDelete, "", []string{"seal-store", "delete", "alpha"}},
		{transport.OpSealStoreReseal, "", []string{"seal-store", "reseal", "alpha"}},
		{transport.OpSealStoreStatus, "", []string{"seal-store", "status"}},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			_, err := executeSealStoreCmdOn(t, &grpctransport.Transport{}, false, tc.stdin, tc.args...)
			if !errors.Is(err, transport.ErrEmbeddedOnly) {
				t.Fatalf("%v: err = %v, want transport.ErrEmbeddedOnly", tc.args, err)
			}
			var eo *transport.EmbeddedOnlyError
			if !errors.As(err, &eo) || eo.Operation != tc.op {
				t.Fatalf("%v: err = %v, want *EmbeddedOnlyError for %s", tc.args, err, tc.op)
			}
			if !strings.Contains(err.Error(), tc.op) || !strings.Contains(err.Error(), "embedded-only") {
				t.Fatalf("%v: message %q does not name %s as embedded-only", tc.args, err, tc.op)
			}
		})
	}
}
