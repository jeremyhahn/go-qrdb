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

//go:build clusterproof

package container

import "strconv"

// ConfigError is an inventory the harness cannot run.
type ConfigError struct {
	Field  string
	Reason string
}

func (e *ConfigError) Error() string {
	return "container: inventory " + e.Field + ": " + e.Reason
}

// UnknownProtocolError is a protocol with no SDK transport behind it.
type UnknownProtocolError struct {
	Protocol string
}

func (e *UnknownProtocolError) Error() string {
	return "container: unknown protocol " + strconv.Quote(e.Protocol) + " (want quic, grpc or rest)"
}

// CommandError is a container command that failed on a node's runtime.
// Output is what the command printed, which is where docker says why.
type CommandError struct {
	Node   string
	Step   string
	Output string
	Err    error
}

func (e *CommandError) Error() string {
	msg := "container: " + e.Step + " on " + e.Node + ": " + e.Err.Error()
	if e.Output != "" {
		msg += ": " + e.Output
	}
	return msg
}

func (e *CommandError) Unwrap() error { return e.Err }

// IdentityError is node-identity output that is not a member identity.
type IdentityError struct {
	Node   string
	Output string
}

func (e *IdentityError) Error() string {
	return "container: node-identity on " + e.Node + " printed no spki:<sha256-hex> identity: " + strconv.Quote(e.Output)
}

// UnsupportedTransportError is a transport that connected but does not carry
// the calls a step needs.
type UnsupportedTransportError struct {
	Protocol string
	Need     string
}

func (e *UnsupportedTransportError) Error() string {
	return "container: transport " + strconv.Quote(e.Protocol) + " does not support " + e.Need
}

// StepError is a node that did not get through a step before its deadline.
// Err is the last failure seen.
type StepError struct {
	Node     string
	Protocol string
	Step     string
	Err      error
}

func (e *StepError) Error() string {
	on := e.Node
	if e.Protocol != "" {
		on += " over " + e.Protocol
	}
	return "container: " + e.Step + " on " + on + ": " + e.Err.Error()
}

func (e *StepError) Unwrap() error { return e.Err }

// CheckError is an answer that contradicts what BOOT-01 requires of it.
type CheckError struct {
	Check  string
	Reason string
}

func (e *CheckError) Error() string {
	return "container: " + e.Check + ": " + e.Reason
}
