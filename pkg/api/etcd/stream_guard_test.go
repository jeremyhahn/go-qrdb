//go:build qrdb_etcd

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

package etcd

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// guardingAuthorizer admits every request and guards streams with unserved.
type guardingAuthorizer struct {
	unserved chan struct{}
	err      error
}

func (g *guardingAuthorizer) AuthorizeRequest(ctx context.Context, _ permission.Permission) (context.Context, error) {
	return ctx, nil
}

func (g *guardingAuthorizer) GuardStream(context.Context) (<-chan struct{}, func() error, error) {
	if g.err != nil {
		return nil, nil, g.err
	}
	return g.unserved, func() error { return status.Error(codes.Unavailable, "barrier sealed for database: db") }, nil
}

// idleStream is a client that opened a stream and sends nothing more: its
// RecvMsg blocks until the stream's context ends.
type idleStream struct {
	fakeStream
	sent int
}

func (s *idleStream) RecvMsg(any) error {
	<-s.ctx.Done()
	return s.ctx.Err()
}

func (s *idleStream) SendMsg(any) error {
	s.sent++
	return nil
}

// A Watch or LeaseKeepAlive stream already open when its database stops
// serving ends with the database's refusal, even from a client that sends
// nothing more. The interceptor used to authorize the stream once and let it
// run on over a database every new request was refused.
func TestStreamAuthorize_EndsWhenTheDatabaseStopsServing(t *testing.T) {
	for _, method := range []string{"/etcdserverpb.Watch/Watch", "/etcdserverpb.Lease/LeaseKeepAlive"} {
		t.Run(method, func(t *testing.T) {
			guard := &guardingAuthorizer{unserved: make(chan struct{})}
			s := NewServer("db", ":0", WithRequestAuthorizer(guard))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream := &idleStream{fakeStream: fakeStream{ctx: ctx}}

			// The handler does what both etcd stream handlers do: receive
			// until the stream fails.
			done := make(chan error, 1)
			go func() {
				done <- s.streamAuthorize(nil, stream, &grpc.StreamServerInfo{FullMethod: method}, func(_ any, ss grpc.ServerStream) error {
					for {
						if err := ss.RecvMsg(new(struct{})); err != nil {
							return err
						}
					}
				})
			}()

			select {
			case err := <-done:
				t.Fatalf("the stream ended while its database was serving: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			close(guard.unserved)
			select {
			case err := <-done:
				if status.Code(err) != codes.Unavailable {
					t.Fatalf("stream ended with %v, want the database's Unavailable refusal", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the stream is still open after its database stopped serving")
			}
		})
	}
}

// A send after the database stopped serving is refused rather than sent,
// and a database that cannot be guarded refuses the stream outright.
func TestStreamAuthorize_GuardedSendAndGuardFailure(t *testing.T) {
	guard := &guardingAuthorizer{unserved: make(chan struct{})}
	s := NewServer("db", ":0", WithRequestAuthorizer(guard))
	stream := &idleStream{fakeStream: fakeStream{ctx: context.Background()}}
	info := &grpc.StreamServerInfo{FullMethod: "/etcdserverpb.Watch/Watch"}

	err := s.streamAuthorize(nil, stream, info, func(_ any, ss grpc.ServerStream) error {
		if err := ss.SendMsg(struct{}{}); err != nil {
			return err
		}
		close(guard.unserved)
		return ss.SendMsg(struct{}{})
	})
	if status.Code(err) != codes.Unavailable || stream.sent != 1 {
		t.Fatalf("send after the database stopped serving: %v (sent %d), want Unavailable after one send", err, stream.sent)
	}

	guard.err = status.Error(codes.NotFound, "database not found")
	ran := false
	err = s.streamAuthorize(nil, stream, info, func(any, grpc.ServerStream) error { ran = true; return nil })
	if status.Code(err) != codes.NotFound || ran {
		t.Fatalf("unguardable stream: %v (handler ran %v), want NotFound before the handler", err, ran)
	}
}

// chattyStream is a client with one message waiting: RecvMsg delivers it and
// then reports the end of the stream.
type chattyStream struct {
	fakeStream
	pending bool
}

func (s *chattyStream) RecvMsg(m any) error {
	if !s.pending {
		return context.Canceled
	}
	s.pending = false
	*m.(*string) = "request"
	return nil
}

// While the database serves, a stream receives its client's messages
// unchanged -- guarded or not -- and the receive's own error ends it.
func TestStreamAuthorize_ReceivesWhileTheDatabaseServes(t *testing.T) {
	authorizers := map[string]RequestAuthorizer{
		"guarded":   &guardingAuthorizer{unserved: make(chan struct{})},
		"unguarded": &recordingAuthorizer{},
	}
	for name, authz := range authorizers {
		t.Run(name, func(t *testing.T) {
			s := NewServer("db", ":0", WithRequestAuthorizer(authz))
			stream := &chattyStream{fakeStream: fakeStream{ctx: context.Background()}, pending: true}
			var got string
			err := s.streamAuthorize(nil, stream, &grpc.StreamServerInfo{FullMethod: "/etcdserverpb.Watch/Watch"}, func(_ any, ss grpc.ServerStream) error {
				if err := ss.RecvMsg(&got); err != nil {
					return err
				}
				return ss.RecvMsg(&got)
			})
			if got != "request" || !errors.Is(err, context.Canceled) {
				t.Fatalf("received %q, ended with %v; want the client's message, then its end", got, err)
			}
		})
	}
}
