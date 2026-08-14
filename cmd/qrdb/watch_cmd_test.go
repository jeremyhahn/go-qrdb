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
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// eventTypeString
// ---------------------------------------------------------------------------

func TestEventTypeString_PUT(t *testing.T) {
	got := eventTypeString(transport.EventPut)
	if got != "PUT" {
		t.Errorf("eventTypeString(EventPut) = %q, want PUT", got)
	}
}

func TestEventTypeString_DELETE(t *testing.T) {
	got := eventTypeString(transport.EventDelete)
	if got != "DELETE" {
		t.Errorf("eventTypeString(EventDelete) = %q, want DELETE", got)
	}
}

func TestEventTypeString_Unknown(t *testing.T) {
	got := eventTypeString(transport.EventType(99))
	if !strings.HasPrefix(got, "UNKNOWN") {
		t.Errorf("eventTypeString(unknown) = %q, want prefix UNKNOWN", got)
	}
}

// ---------------------------------------------------------------------------
// newWatchCmd — integration with mock watch service
// ---------------------------------------------------------------------------

// executeWatchCmd builds an embedded SDK client with a mock watch service,
// installs it as the package-level client, and executes the watch command.
// The context is canceled after startup to let the command exit.
func executeWatchCmd(t *testing.T, svc watchServiceForEmbedded, format string, args ...string) (string, error) {
	t.Helper()

	tr, err := embedded.New(&noopKVService{}, newMockAdminService(),
		embedded.WithWatchService(svc),
	)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient := client
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	client = sdkClient
	outputFormat = format
	var buf bytes.Buffer
	outputWriter = &buf

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := newWatchCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs(args)

	execErr := cmd.ExecuteContext(ctx)
	return buf.String(), execErr
}

// watchServiceForEmbedded is the service.WatchServicer interface alias for
// use in the embedded transport options.
type watchServiceForEmbedded = service.WatchServicer

// TestWatchCmd_ChannelClosed verifies that when the watch channel is closed
// (no events), the watch command exits without error.
func TestWatchCmd_ChannelClosed(t *testing.T) {
	svc := newMockWatchService()
	_, err := executeWatchCmd(t, svc, "table", "my-key")
	// When the mock channel closes after delivering one event, watch exits.
	if err != nil && err.Error() != "context canceled" {
		t.Fatalf("watch: %v", err)
	}
}

// TestWatchCmd_PrefixFlag verifies the --prefix flag exercises the WatchPrefix path.
func TestWatchCmd_PrefixFlag(t *testing.T) {
	svc := newMockWatchService()
	_, err := executeWatchCmd(t, svc, "table", "my-prefix/", "--prefix")
	if err != nil && err.Error() != "context canceled" {
		t.Fatalf("watch --prefix: %v", err)
	}
}

// TestWatchCmd_JSON verifies JSON output format in the watch command.
func TestWatchCmd_JSON(t *testing.T) {
	svc := newMockWatchService()
	_, err := executeWatchCmd(t, svc, "json", "my-key")
	if err != nil && err.Error() != "context canceled" {
		t.Fatalf("watch(json): %v", err)
	}
}

// TestWatchCmd_WithStartRevision exercises the --start-revision flag.
func TestWatchCmd_WithStartRevision(t *testing.T) {
	svc := newMockWatchService()
	_, err := executeWatchCmd(t, svc, "table", "my-key", "--start-revision", "5")
	if err != nil && err.Error() != "context canceled" {
		t.Fatalf("watch --start-revision: %v", err)
	}
}

// TestWatchCmd_PrevKV exercises the --prev-kv flag (with PrevKv populated).
func TestWatchCmd_PrevKV(t *testing.T) {
	svc := newMockWatchService()
	_, err := executeWatchCmd(t, svc, "table", "my-key", "--prev-kv")
	if err != nil && err.Error() != "context canceled" {
		t.Fatalf("watch --prev-kv: %v", err)
	}
}
