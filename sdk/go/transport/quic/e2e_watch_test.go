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
	"testing"
	"time"

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

func TestE2E_WatchPrefix_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("watch.WatchPrefix", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.WatchResponse{
			WatchId: 2,
			Events: []*pb.Event{
				{Type: pb.EventType_EVENT_DELETE, Kv: &pb.KeyValue{Key: []byte("p/x")}},
			},
		})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := tr.WatchPrefix(ctx, "p/", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("WatchPrefix: %v", err)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			t.Fatalf("channel closed without event")
		}
		if len(resp.Events) != 1 || resp.Events[0].Type != transport.EventDelete {
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
