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

package grpc

import (
	"context"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
)

// sockoptObserver is a connection-observing module; it only counts.
type sockoptObserver struct {
	apimodule.BaseModule
	opened, closed atomic.Int64
}

func (*sockoptObserver) Name() string                     { return "sockopt-observer" }
func (o *sockoptObserver) ConnOpened(apimodule.Transport) { o.opened.Add(1) }
func (o *sockoptObserver) ConnClosed(apimodule.Transport) { o.closed.Add(1) }

var _ apimodule.ConnObserver = (*sockoptObserver)(nil)

// serverSideTCPUserTimeout finds, among this process's own file descriptors,
// the ESTABLISHED socket whose local port is port — the server's end of an
// accepted connection — and returns its TCP_USER_TIMEOUT in milliseconds. It
// reads the kernel's view of the socket, so it cannot be fooled by what the
// Go-side connection wrapper claims.
func serverSideTCPUserTimeout(t *testing.T, port int) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read /proc/self/fd: %v", err)
	}
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		local, err := syscall.Getsockname(fd)
		if err != nil {
			continue
		}
		in4, ok := local.(*syscall.SockaddrInet4)
		if !ok || in4.Port != port {
			continue
		}
		if _, err := syscall.Getpeername(fd); err != nil {
			continue // the listening socket has no peer
		}
		ms, err := unix.GetsockoptInt(fd, unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT)
		if err != nil {
			t.Fatalf("getsockopt TCP_USER_TIMEOUT: %v", err)
		}
		return ms
	}
	t.Fatalf("no accepted server socket on port %d", port)
	return 0
}

// TestConnObserver_KeepsGRPCTCPUserTimeout is the regression test for the
// review finding: observing connections by wrapping the gRPC LISTENER hid the
// *net.TCPConn from grpc-go, which then silently skipped TCP_USER_TIMEOUT
// (20000ms by default, 0 with the wrapper). The observer must not change the
// accepted socket: with one registered, the kernel still reports grpc-go's
// default, and the observer still sees the connection.
func TestConnObserver_KeepsGRPCTCPUserTimeout(t *testing.T) {
	obs := &sockoptObserver{}
	tlsCfg := testServerTLSConfig(t)
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithTLS(tlsCfg), WithServiceModules([]apimodule.ServiceModule{obs}))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	conn, err := grpc.NewClient(srv.Addr(), grpc.WithTransportCredentials(testClientCreds(t, tlsCfg)))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := pb.NewQRDBServiceClient(conn).Health(ctx, &emptypb.Empty{}); err != nil {
		t.Fatalf("Health: %v", err)
	}

	_, portStr, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("split %q: %v", srv.Addr(), err)
	}
	port, _ := strconv.Atoi(portStr)
	if got := serverSideTCPUserTimeout(t, port); got != 20000 {
		t.Fatalf("TCP_USER_TIMEOUT on the accepted socket = %dms, want grpc-go's 20000ms", got)
	}
	if obs.opened.Load() < 1 {
		t.Fatal("the observer did not see the connection")
	}
}
