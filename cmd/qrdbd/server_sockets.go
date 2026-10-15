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
	"net"
	"strconv"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
)

// serverSockets holds the REST, gRPC, QUIC and MCP sockets that every server
// lifetime of one runStart serves on.
//
// One boot can run several servers on the same ports: the sealed-window
// listener, the node's own server once the barrier is unsealed, and a new
// server after each in-process system encryption migration. If each server
// bound its own sockets, the ports would belong to nobody from one server's
// Stop to the next one's Start -- after an unseal, that is as long as
// initializeNode and first boot take -- and any socket on the host could take
// one in between: another process binding it, or an outbound connection
// drawing it as its ephemeral source port. The node would then unseal and
// fail to serve with "bind: address already in use".
//
// So the sockets are bound once, immediately before the first server starts,
// and handed to every server through server.WithRESTListener,
// WithRESTPacketConn, WithGRPCListener, WithQUICPacketConn and
// WithMCPListener. A server's Stop ends its use of a
// handed-over socket without closing it; Close closes the sockets for real
// when runStart returns. The zero value is ready to use.
type serverSockets struct {
	opts  []server.ServerOption
	bound bool
}

// options returns the server options that hand the sockets to a server,
// binding them on the first call. cfg says which protocols are enabled and
// where they listen; every call within one runStart is made with the
// configuration buildServerConfig derives from the same flags.
func (s *serverSockets) options(cfg *qrdbconfig.Config) ([]server.ServerOption, error) {
	if s.bound {
		return s.opts, nil
	}
	opts, err := bindServerSockets(cfg)
	if err != nil {
		return nil, err
	}
	s.opts, s.bound = opts, true
	return s.opts, nil
}

// Close closes every socket options bound. It is safe to call when nothing
// was bound, and after a server that refused to start already closed them.
func (s *serverSockets) Close() {
	closeServerSockets(s.opts)
	s.opts, s.bound = nil, false
}

// bindServerSockets binds the sockets of every enabled protocol that accepts
// a handed-over socket: REST's TCP listener with its HTTP/3 UDP socket,
// gRPC's TCP listener, the native QUIC transport's UDP socket and MCP's TCP
// listener. It binds them where the server itself would. Either
// every socket is bound or none is left open.
func bindServerSockets(cfg *qrdbconfig.Config) (_ []server.ServerOption, err error) {
	var opts []server.ServerOption
	defer func() {
		if err != nil {
			closeServerSockets(opts)
		}
	}()

	if cfg.Protocols.REST {
		addr := net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.AdminPort))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, &ServerSocketBindError{Protocol: "rest", Network: "tcp", Addr: addr, Err: err}
		}
		opts = append(opts, server.WithRESTListener(ln))

		// HTTP/3 shares the TCP listener's host and port: the port is the one
		// Alt-Svc advertises, and the REST server refuses a socket on any
		// other. It is read back from the bound listener, as the server does
		// when it binds the pair itself, so a kernel-chosen port carries over.
		h3Addr := ln.Addr().String()
		pc, err := net.ListenPacket("udp", h3Addr)
		if err != nil {
			return nil, &ServerSocketBindError{Protocol: "rest", Network: "udp", Addr: h3Addr, Err: err}
		}
		opts = append(opts, server.WithRESTPacketConn(pc))
	}

	if cfg.Protocols.GRPC {
		addr := net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.GRPCPort))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, &ServerSocketBindError{Protocol: "grpc", Network: "tcp", Addr: addr, Err: err}
		}
		opts = append(opts, server.WithGRPCListener(ln))
	}

	if cfg.Protocols.QUIC {
		addr := net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.QUICPort))
		pc, err := net.ListenPacket("udp", addr)
		if err != nil {
			return nil, &ServerSocketBindError{Protocol: "quic", Network: "udp", Addr: addr, Err: err}
		}
		opts = append(opts, server.WithQUICPacketConn(pc))
	}

	if cfg.Protocols.MCP {
		addr := net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.MCP.Port))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, &ServerSocketBindError{Protocol: "mcp", Network: "tcp", Addr: addr, Err: err}
		}
		opts = append(opts, server.WithMCPListener(ln))
	}

	return opts, nil
}

// closeServerSockets closes every socket opts hands over.
func closeServerSockets(opts []server.ServerOption) {
	for _, sock := range server.PreboundSockets(opts...) {
		_ = sock.Close()
	}
}
