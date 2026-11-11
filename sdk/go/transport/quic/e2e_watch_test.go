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

package quic

import (
	"context"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestE2E_Watch_HappyPath exercises the single-shot watch round-trip path.
// The harness server returns one StatusOK WatchResponse and then closes the
// stream — the SDK's reader goroutine receives the event, propagates it on
// the channel, then exits when the read EOF arrives.
func TestE2E_Watch_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("watch.Watch", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.WatchResponse{
			WatchId:  1,
			Revision: 5,
			Events: []*pb.Event{
				{
					Type: pb.EventType_EVENT_PUT,
					Kv:   &pb.KeyValue{Key: []byte("k"), Value: []byte("v"), Version: 1},
				},
			},
		})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := tr.Watch(ctx, "k", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			t.Fatalf("channel closed without event")
		}
		if len(resp.Events) != 1 || resp.Revision != 5 {
			t.Fatalf("resp = %+v", resp)
		}
	case <-ctx.Done():
		t.Fatalf("timeout waiting for watch event")
	}
}

// TestE2E_Watch_Canceled exercises the canceled-response branch in
// readWatchResponses where the server reports cancel=true and the reader
// exits the loop.
func TestE2E_Watch_Canceled(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("watch.Watch", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.WatchResponse{
			WatchId: 1, Canceled: true, CancelReason: "shutdown",
		})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := tr.Watch(ctx, "k", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	resp, ok := <-ch
	if !ok {
		t.Fatalf("channel closed without event")
	}
	if !resp.Canceled || resp.CancelReason != "shutdown" {
		t.Fatalf("resp = %+v", resp)
	}
}

// TestE2E_Watch_NonOKStatus exercises the non-OK status branch in
// readWatchResponses where the server returns an error status and the
// reader emits a single Canceled response.
func TestE2E_Watch_NonOKStatus(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("watch.Watch", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("watch failed")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := tr.Watch(ctx, "k", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	resp, ok := <-ch
	if !ok {
		t.Fatalf("channel closed without event")
	}
	if !resp.Canceled || resp.CancelReason != "watch failed" {
		t.Fatalf("resp = %+v", resp)
	}
}

// ---------------------------------------------------------------------------
// start_revision threading
// ---------------------------------------------------------------------------

// TestE2E_Watch_StartRevisionThreaded verifies WatchOptions.StartRevision is
// encoded onto the wire pb.WatchRequest.start_revision field.
func TestE2E_Watch_StartRevisionThreaded(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("watch.Watch", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.WatchResponse{WatchId: 1, Revision: 1})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := tr.Watch(ctx, "k", transport.WatchOptions{StartRevision: 42})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if _, ok := <-ch; !ok {
		t.Fatal("channel closed without event")
	}

	rec := srv.LastRequest("watch.Watch")
	if rec == nil {
		t.Fatal("server never received a watch.Watch request")
	}
	var req pb.WatchRequest
	if err := proto.Unmarshal(rec.Payload, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if req.GetStartRevision() != 42 {
		t.Fatalf("StartRevision = %d, want 42", req.GetStartRevision())
	}
}

// ---------------------------------------------------------------------------
// Context cancellation must unblock a pending read promptly
// ---------------------------------------------------------------------------

// TestE2E_Watch_ContextCancel_ClosesChannelPromptly exercises the fix in
// watchStream that ties the QUIC stream's read side to ctx via
// stream.CancelRead: without it, a caller cancellation while
// readWatchResponses is blocked in wire.ReadResponse (the server holding the
// stream open with no response yet — the ordinary state while waiting for
// the next watch event) would leave the reader goroutine, and the returned
// channel, hanging until the server independently closed the connection.
func TestE2E_Watch_ContextCancel_ClosesChannelPromptly(t *testing.T) {
	srv, tr := startE2EServer(t)

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	received := make(chan struct{})
	var receivedOnce sync.Once
	srv.SetResponder("watch.Watch", func(_ []byte) (uint16, []byte) {
		receivedOnce.Do(func() { close(received) })
		<-block // never respond during the test; simulates an idle watch stream.
		return wire.StatusOK, nil
	})

	ctx, cancel := context.WithCancel(context.Background())

	ch, err := tr.Watch(ctx, "k", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Cancel only once the server holds the watch request, so this
	// exercises unblocking an in-flight read rather than the pre-read
	// ctx.Done() check at the top of the loop.
	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("server never received the watch request")
	}
	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected the channel to close, not deliver a response")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("channel did not close promptly after context cancellation")
	}
}
