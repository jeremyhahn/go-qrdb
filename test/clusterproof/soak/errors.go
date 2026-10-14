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

package soak

import (
	"errors"
	"strings"
)

// ErrUnavailable reports a seam with no implementation: the evidence it would
// supply does not exist, and the verdict says so instead of passing.
var ErrUnavailable = errors.New("soak: not available")

// ConfigError reports a run input the soak refuses before it touches the
// cluster.
type ConfigError struct {
	Field  string
	Reason string
	Err    error
}

func (e *ConfigError) Error() string {
	msg := "soak: " + e.Field + ": " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *ConfigError) Unwrap() error { return e.Err }

// BundleError reports an evidence bundle that cannot be written, or one that
// fails verification: a checksum, a missing or extra file, or a derived file
// that no longer follows from the recorded evidence.
type BundleError struct {
	Path   string
	Reason string
	Err    error
}

func (e *BundleError) Error() string {
	msg := "soak: bundle " + e.Path + ": " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *BundleError) Unwrap() error { return e.Err }

// ClusterError reports a query to one node of the cluster under test that
// failed.
type ClusterError struct {
	Node string
	Op   string
	Err  error
}

func (e *ClusterError) Error() string {
	msg := "soak: " + e.Op + " on node " + e.Node
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *ClusterError) Unwrap() error { return e.Err }

// LeaderError reports that the cluster named no single leader: none, or
// several, of the nodes that answered reported themselves leader.
type LeaderError struct {
	Leaders  []string
	Failures []string
}

func (e *LeaderError) Error() string {
	msg := "soak: no node reports itself leader"
	if len(e.Leaders) > 1 {
		msg = "soak: several nodes report themselves leader: " + strings.Join(e.Leaders, ", ")
	}
	if len(e.Failures) > 0 {
		msg += "; " + strings.Join(e.Failures, "; ")
	}
	return msg
}
