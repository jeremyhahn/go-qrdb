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

package config

import (
	"os"
	"strings"
)

// ResolveJoinToken reads the member-side join admission secret from exactly
// one of a file or an environment variable, never from inline YAML --
// ClusterJoinConfig.TokenFile/TokenEnv and BootstrapConfig.JoinTokenFile/
// JoinTokenEnv both resolve through this one function, so there is exactly
// one place that decides what counts as "configured" and what an empty
// result means.
//
// Validate (Config.Validate) already refuses a config carrying both a file
// and an env name; this function assumes that invariant holds and refuses
// again defensively rather than silently preferring one.
func ResolveJoinToken(file, env string) (string, error) {
	if file != "" && env != "" {
		return "", &JoinTokenSourceConflictError{}
	}
	if file == "" && env == "" {
		return "", &JoinTokenNotConfiguredError{}
	}
	if file != "" {
		data, err := os.ReadFile(file) // #nosec G304 -- operator-configured path, not user input
		if err != nil {
			return "", &JoinTokenFileError{Path: file, Err: err}
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", &JoinTokenEmptyError{Source: "file:" + file}
		}
		return token, nil
	}
	token := os.Getenv(env)
	if token == "" {
		return "", &JoinTokenEmptyError{Source: "env:" + env}
	}
	return token, nil
}

// JoinTokenSourceConflictError is returned when both a token file and a
// token environment variable are configured. Exactly one source is
// resolved; defaulting to either would mean the unused source's value is
// silently ignored, which is indistinguishable from a configuration mistake.
type JoinTokenSourceConflictError struct{}

// Error returns a human-readable description.
func (e *JoinTokenSourceConflictError) Error() string {
	return "config: join token file and env are both configured; set exactly one"
}

// JoinTokenNotConfiguredError is returned when ResolveJoinToken is called
// with neither a file nor an env name. Callers that reach this had no
// business calling it: Config.Validate refuses a join-enabled config with
// no token source before this function would ever run against it.
type JoinTokenNotConfiguredError struct{}

// Error returns a human-readable description.
func (e *JoinTokenNotConfiguredError) Error() string {
	return "config: no join token source configured (file or env)"
}

// JoinTokenFileError wraps a failure reading the join token file.
type JoinTokenFileError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *JoinTokenFileError) Error() string {
	return "config: failed to read join token file " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *JoinTokenFileError) Unwrap() error { return e.Err }

// JoinTokenEmptyError is returned when the configured token source (file or
// env) resolves to an empty string after trimming whitespace. An empty
// token is not a valid admission secret, and a shipped default is exactly
// what this is refusing: there is none.
type JoinTokenEmptyError struct {
	// Source describes where the empty value came from, e.g. "file:/etc/...".
	Source string
}

// Error returns a human-readable description.
func (e *JoinTokenEmptyError) Error() string {
	return "config: join token from " + e.Source + " is empty"
}
