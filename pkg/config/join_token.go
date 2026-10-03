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
	"strconv"
	"strings"
	"syscall"
)

// MinJoinTokenLength is the shortest value ResolveJoinToken accepts from
// either source. A real minted token (clusterenroll.MintJoinToken) is a
// hex-encoded 32-byte value -- 64 characters -- so this bound exists only to
// catch an operator-typed placeholder or truncated copy-paste, not to size
// real tokens.
const MinJoinTokenLength = 20

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
		if err := checkJoinTokenFilePermissions(file); err != nil {
			return "", err
		}
		data, err := os.ReadFile(file) // #nosec G304 -- operator-configured path, not user input
		if err != nil {
			return "", &JoinTokenFileError{Path: file, Err: err}
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", &JoinTokenEmptyError{Source: "file:" + file}
		}
		if len(token) < MinJoinTokenLength {
			return "", &JoinTokenTooShortError{Source: "file:" + file, Length: len(token)}
		}
		return token, nil
	}
	token := os.Getenv(env)
	if token == "" {
		return "", &JoinTokenEmptyError{Source: "env:" + env}
	}
	if len(token) < MinJoinTokenLength {
		return "", &JoinTokenTooShortError{Source: "env:" + env, Length: len(token)}
	}
	return token, nil
}

// checkJoinTokenFilePermissions refuses a join-token file that is readable,
// writable or executable by its group or by anyone else, or that is not
// owned by this process's own user -- the same posture an SSH private key
// demands, for the same reason: a token on disk is bearer-equivalent to the
// node identity it admits.
func checkJoinTokenFilePermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return &JoinTokenFileError{Path: path, Err: err}
	}
	if info.Mode().Perm()&0o077 != 0 {
		return &JoinTokenFileModeError{Path: path, Mode: info.Mode().Perm()}
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		// Not a platform that exposes a Unix Stat_t (e.g. unsupported by
		// the host OS's os.FileInfo.Sys() implementation): the mode check
		// above still ran, and there is no portable owner to compare.
		return nil
	}
	if uid := os.Getuid(); uid >= 0 && uint32(uid) != stat.Uid { //nolint:gosec // uid is a non-negative process UID
		return &JoinTokenFileOwnerError{Path: path}
	}
	return nil
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

// JoinTokenFileModeError is returned when the join token file is readable,
// writable or executable by its group or by anyone else. The token it holds
// admits a node to the cluster; a world-readable copy of it is as much a
// compromise as a world-readable SSH private key.
type JoinTokenFileModeError struct {
	Path string
	Mode os.FileMode
}

// Error returns a human-readable description.
func (e *JoinTokenFileModeError) Error() string {
	return "config: join token file " + e.Path + " has mode " + e.Mode.String() +
		"; refusing a group- or world-readable join token file (want at most 0600)"
}

// JoinTokenFileOwnerError is returned when the join token file is not owned
// by this process's own user. A token readable only by its listed owner but
// owned by a DIFFERENT user is not this process's secret to trust.
type JoinTokenFileOwnerError struct {
	Path string
}

// Error returns a human-readable description.
func (e *JoinTokenFileOwnerError) Error() string {
	return "config: join token file " + e.Path + " is not owned by this process's user"
}

// JoinTokenTooShortError is returned when a resolved token is shorter than
// MinJoinTokenLength. A real minted token is far longer; this catches an
// operator-typed placeholder or a truncated copy-paste before it is
// presented to a signing member and refused there with no diagnostic detail
// (no token-validity oracle).
type JoinTokenTooShortError struct {
	// Source describes where the short value came from, e.g. "file:/etc/...".
	Source string
	Length int
}

// Error returns a human-readable description.
func (e *JoinTokenTooShortError) Error() string {
	return "config: join token from " + e.Source + " is " + strconv.Itoa(e.Length) +
		" characters, want at least " + strconv.Itoa(MinJoinTokenLength)
}
