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

package netport

import (
	"strconv"
	"strings"
)

// NoProbeNetworkError reports a port search that named no network to prove
// the port free on.
//
// It is not a degenerate "find any number" request. A caller that names no
// network has not said what the port is for, and the whole point of the probe
// is that it matches the listener (go-qrdb#257) -- so there is nothing to match
// against and the only honest answer is a refusal.
type NoProbeNetworkError struct {
	// Host is the bind host the search was asked about.
	Host string
}

// Error implements error.
func (e *NoProbeNetworkError) Error() string {
	return "netport: port search at " + strconv.Quote(e.Host) +
		" named no network; a port must be proven free on the protocols its listener will bind"
}

// UnknownProbeNetworkError reports a network name the probe table has no
// entry for.
type UnknownProbeNetworkError struct {
	// Network is the name that missed the table.
	Network string
}

// Error implements error.
func (e *UnknownProbeNetworkError) Error() string {
	return "netport: unknown port-probe network " + strconv.Quote(e.Network) +
		" (want " + strconv.Quote(NetworkTCP) + " or " + strconv.Quote(NetworkUDP) + ")"
}

// ProbeAddrError reports a probe socket whose bound address has no readable
// port component.
type ProbeAddrError struct {
	// Addr is the address the socket reported.
	Addr string
	// Err is the parse failure.
	Err error
}

// Error implements error.
func (e *ProbeAddrError) Error() string {
	return "netport: cannot read the port from probe address " + strconv.Quote(e.Addr) + ": " + e.Err.Error()
}

// Unwrap returns the parse failure.
func (e *ProbeAddrError) Unwrap() error { return e.Err }

// NoFreePortError reports that FreePortOn drew no port free on every requested
// network within its attempt budget.
//
// The host and the network list are both named because they are the two
// things a reader needs in order to act: a failure at "0.0.0.0" on tcp+udp
// means some other holder has each candidate on one stack or one interface,
// and the remedy is to pin the port explicitly rather than to re-run and hope.
type NoFreePortError struct {
	// Host is the bind host the search probed at.
	Host string
	// Networks are the protocols every candidate had to be free on.
	Networks []string
	// Attempts is how many candidates were drawn.
	Attempts int
	// Err is the last candidate's bind failure.
	Err error
}

// Error implements error.
func (e *NoFreePortError) Error() string {
	msg := "netport: no port free on " + strings.Join(e.Networks, "+") +
		" at " + strconv.Quote(e.Host) + " after " + strconv.Itoa(e.Attempts) + " attempts"
	if e.Err != nil {
		msg += ": last bind error: " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the last bind failure.
func (e *NoFreePortError) Unwrap() error { return e.Err }

// EphemeralRangeError reports that the kernel's ephemeral port range, which
// the reservation window must sit below, could not be determined.
type EphemeralRangeError struct {
	// Path is the file the range was read from.
	Path string
	// Reason says what was wrong with it.
	Reason string
	// Err is the underlying read or parse failure, if any.
	Err error
}

// Error implements error.
func (e *EphemeralRangeError) Error() string {
	msg := "netport: cannot determine the kernel ephemeral port range from " + e.Path + ": " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying read or parse failure.
func (e *EphemeralRangeError) Unwrap() error { return e.Err }

// PortWindowError reports an ephemeral floor so low that the reservation
// window would reach into the well-known ports.
type PortWindowError struct {
	// EphemeralLow is the kernel's ephemeral floor.
	EphemeralLow int
	// Window is how many ports the window needs below that floor.
	Window int
}

// Error implements error.
func (e *PortWindowError) Error() string {
	return "netport: ephemeral floor " + strconv.Itoa(e.EphemeralLow) +
		" leaves no room for a " + strconv.Itoa(e.Window) + "-port reservation window"
}

// PortExhaustedError reports that this process has drawn every candidate in
// the reservation window.
type PortExhaustedError struct {
	// Window is the window's size.
	Window int
	// Base is the window's first port.
	Base int
	// Low is the ephemeral floor, one past the window's last port.
	Low int
}

// Error implements error.
func (e *PortExhaustedError) Error() string {
	return "netport: exhausted the " + strconv.Itoa(e.Window) + "-port reservation window [" +
		strconv.Itoa(e.Base) + "," + strconv.Itoa(e.Low) + ")"
}

// PortReservationError reports a Reserve call that drew its whole candidate
// budget without finding enough ports that both locked and bound.
type PortReservationError struct {
	// Host is the bind host the candidates were probed at.
	Host string
	// Networks are the protocols every candidate had to be free on.
	Networks []string
	// Requested is how many ports the call asked for.
	Requested int
	// Reserved is how many it had found when the budget ran out.
	Reserved int
	// LastErr is the last candidate's lock or bind failure.
	LastErr error
}

// Error implements error.
func (e *PortReservationError) Error() string {
	msg := "netport: could not reserve " + strconv.Itoa(e.Requested) + " unique ports free on " +
		strings.Join(e.Networks, "+") + " at " + strconv.Quote(e.Host) +
		" (reserved " + strconv.Itoa(e.Reserved) + ")"
	if e.LastErr != nil {
		msg += ": last error: " + e.LastErr.Error()
	}
	return msg
}

// Unwrap returns the last lock or bind failure.
func (e *PortReservationError) Unwrap() error { return e.LastErr }
