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

package qrdbdharness

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// startCommand is the qrdbd subcommand that binds listeners.
const startCommand = "start"

// listenerFlag is one `qrdbd start` flag naming the port of a listener the
// daemon binds at a fixed default when the flag is not given.
type listenerFlag struct {
	// port is the flag that sets the listener's port.
	port string
	// enabledBy is the flag that turns the listener on; empty means the
	// listener is on by default.
	enabledBy string
	// reserve draws the port: UDP for a QUIC listener, TCP otherwise.
	reserve func() (int, error)
}

// defaultPortListeners lists every listener `qrdbd start` binds at a fixed
// default port (cmd/qrdbd/start.go and its per-protocol flag files). A fixed
// default is one port shared by every concurrent daemon on the host, so two
// test processes that both leave it unset collide with EADDRINUSE: the CI
// per-feature integration jobs run in parallel on one runner host.
//
// --listen and --raft-address are not here: every peer names them, so a test
// must choose them itself.
var defaultPortListeners = []listenerFlag{
	{port: "--admin-port", reserve: ReserveTCPPort},
	{port: "--grpc-port", reserve: ReserveTCPPort},
	// A node serving its self-managed CA's trust anchor, or advertising
	// enrollment as the first node, binds the Noise bootstrap server here
	// (clusterenroll.AdvertiseOptions.NoisePort).
	{port: "--bootstrap-noise-port", reserve: ReserveTCPPort},
	{port: "--quic-port", enabledBy: "--enable-quic", reserve: ReserveUDPPort},
	{port: "--mcp-port", enabledBy: "--enable-mcp", reserve: ReserveTCPPort},
	{port: "--cluster-join-listen-port", enabledBy: "--cluster-join-enabled", reserve: ReserveTCPPort},
	{port: "--mysql-port", enabledBy: "--enable-mysql", reserve: ReserveTCPPort},
	{port: "--pgsql-port", enabledBy: "--enable-pgsql", reserve: ReserveTCPPort},
}

// ReserveListenerPorts returns a copy of the `qrdbd start` args with a
// reserved port (ReserveTCPPort, ReserveUDPPort) appended for every listener
// the args would otherwise leave at its fixed default port. A port flag the
// args already carry is left alone, as is a listener whose enabling flag is
// absent. Args for any other subcommand are returned unchanged. Start applies it to
// every daemon it launches.
func ReserveListenerPorts(args []string) ([]string, error) {
	if len(args) == 0 || args[0] != startCommand {
		return args, nil
	}
	out := append([]string(nil), args...)
	for _, l := range defaultPortListeners {
		if hasFlag(args, l.port) || (l.enabledBy != "" && !hasFlag(args, l.enabledBy)) {
			continue
		}
		port, err := l.reserve()
		if err != nil {
			return nil, err
		}
		out = append(out, l.port, strconv.Itoa(port))
	}
	return out, nil
}

// addrInUse is how a failed bind ends (EADDRINUSE's strerror), on Linux and
// in Go's own net error text alike.
const addrInUse = "address already in use"

// portInLine matches a ":port" in a bind failure's "host:port" address.
var portInLine = regexp.MustCompile(`:(\d{1,5})\b`)

// portCollision classifies an exited daemon's wait error. When err's tail
// carries a failed bind, it returns a *PortCollisionError wrapping err, with
// Port and Reserved set when the failing line names one of the reserved
// ports; otherwise it returns err unchanged.
func portCollision(err *DaemonWaitError, reserved []string) error {
	collided := false
	for _, line := range strings.Split(err.Tail, "\n") {
		if !strings.Contains(line, addrInUse) {
			continue
		}
		collided = true
		for _, m := range portInLine.FindAllStringSubmatch(line, -1) {
			if slices.Contains(reserved, m[1]) {
				return &PortCollisionError{Port: m[1], Reserved: true, Err: err}
			}
		}
	}
	if collided {
		return &PortCollisionError{Err: err}
	}
	return err
}

// hasFlag reports whether args carry the flag, as "--name" or "--name=value".
func hasFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}
	return false
}
