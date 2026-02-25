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
	"errors"
	"strconv"
)

// errNoCertsInPEM is returned when a PEM file contains no valid
// certificate blocks.
var errNoCertsInPEM = errors.New("no certificates found in PEM data")

// PeerParseError is returned when a peer specification string cannot be
// parsed. The expected format is "id=address".
type PeerParseError struct {
	Input string
}

// Error returns a human-readable description of the parse failure.
func (e *PeerParseError) Error() string {
	return "cli: invalid peer format " + strconv.Quote(e.Input) + ", expected id=address"
}

// UnknownSMError is returned when the --sm flag specifies an unknown
// state machine type.
type UnknownSMError struct {
	Name string
}

// Error returns a human-readable description.
func (e *UnknownSMError) Error() string {
	return "cli: unknown state machine type " + strconv.Quote(e.Name)
}

// UnknownDiscoveryError is returned when the --discovery flag specifies
// an unknown discovery method.
type UnknownDiscoveryError struct {
	Name string
}

// Error returns a human-readable description.
func (e *UnknownDiscoveryError) Error() string {
	return "cli: unknown discovery method " + strconv.Quote(e.Name)
}

// MissingFlagError is returned when a required flag is not set.
type MissingFlagError struct {
	Flag string
}

// Error returns a human-readable description.
func (e *MissingFlagError) Error() string {
	return "cli: required flag --" + e.Flag + " is not set"
}

// NoPeersError is returned when no peers are specified for static
// discovery.
type NoPeersError struct{}

// Error returns a human-readable description.
func (e *NoPeersError) Error() string {
	return "cli: no peers specified, use --peers flag with static discovery"
}

// AdminFetchError is returned when an HTTP request to the admin server
// fails.
type AdminFetchError struct {
	URL string
	Err error
}

// Error returns a human-readable description.
func (e *AdminFetchError) Error() string {
	return "cli: failed to fetch " + e.URL + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *AdminFetchError) Unwrap() error {
	return e.Err
}

// AdminResponseError is returned when the admin server returns a
// non-200 status code.
type AdminResponseError struct {
	URL        string
	StatusCode int
}

// Error returns a human-readable description.
func (e *AdminResponseError) Error() string {
	return "cli: admin server returned status " + strconv.Itoa(e.StatusCode) + " for " + e.URL
}

// ConfigApplyError is returned when a YAML configuration file cannot be
// applied to the start flags.
type ConfigApplyError struct {
	Field string
	Err   error
}

// Error returns a human-readable description.
func (e *ConfigApplyError) Error() string {
	return "cli: config apply failed for " + strconv.Quote(e.Field) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ConfigApplyError) Unwrap() error {
	return e.Err
}

// TLSGenerateError is returned when TLS certificate generation fails.
type TLSGenerateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *TLSGenerateError) Error() string {
	return "cli: TLS certificate generation failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *TLSGenerateError) Unwrap() error {
	return e.Err
}

// TLSLoadError is returned when loading TLS certificate material from
// a file fails.
type TLSLoadError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *TLSLoadError) Error() string {
	return "cli: failed to load TLS file " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *TLSLoadError) Unwrap() error {
	return e.Err
}

// CACertWriteError is returned when writing the CA certificate to disk
// fails.
type CACertWriteError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *CACertWriteError) Error() string {
	return "cli: failed to write CA certificate to " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CACertWriteError) Unwrap() error {
	return e.Err
}
