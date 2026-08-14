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
	"strconv"
	"time"
)

// NotConnectedError is returned when a binding method is called
// before a client connection has been established.
type NotConnectedError struct{}

// Error returns a human-readable description.
func (e *NotConnectedError) Error() string {
	return "qrdb: not connected to server"
}

// GUIUnavailableError is returned when the binary was built without
// the desktop build tag, indicating no GUI support.
type GUIUnavailableError struct{}

// Error returns a human-readable description.
func (e *GUIUnavailableError) Error() string {
	return "qrdb: GUI unavailable (built without desktop tag)"
}

// FeatureDisabledError is returned when a build-tag gated feature
// (e.g., time series, vector) is not compiled into the binary.
type FeatureDisabledError struct {
	Feature string
}

// Error returns a human-readable description.
func (e *FeatureDisabledError) Error() string {
	return "qrdb: feature disabled: " + e.Feature
}

// EmbeddedServerError is returned when the embedded server encounters
// an error during startup or shutdown.
type EmbeddedServerError struct {
	Op  string
	Err error
}

// Error returns a human-readable description.
func (e *EmbeddedServerError) Error() string {
	return "qrdb: embedded server " + e.Op + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *EmbeddedServerError) Unwrap() error {
	return e.Err
}

// AlreadyRunningError is returned when an attempt is made to start
// the embedded server while it is already running.
type AlreadyRunningError struct{}

// Error returns a human-readable description.
func (e *AlreadyRunningError) Error() string {
	return "qrdb: embedded server is already running"
}

// NotRunningError is returned when an attempt is made to stop the
// embedded server while it is not running.
type NotRunningError struct{}

// Error returns a human-readable description.
func (e *NotRunningError) Error() string {
	return "qrdb: embedded server is not running"
}

// SMResolveError is returned when the state machine type cannot be
// resolved from the statemachine registry.
type SMResolveError struct {
	Name string
	Err  error
}

// Error returns a human-readable description.
func (e *SMResolveError) Error() string {
	return "qrdb: resolve state machine " + e.Name + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *SMResolveError) Unwrap() error {
	return e.Err
}

// HostCreateError is returned when the quicraft host cannot be created.
type HostCreateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *HostCreateError) Error() string {
	return "qrdb: create host: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *HostCreateError) Unwrap() error {
	return e.Err
}

// ShardStartError is returned when a Raft shard cannot be started.
type ShardStartError struct {
	ShardID uint64
	Err     error
}

// Error returns a human-readable description.
func (e *ShardStartError) Error() string {
	return "qrdb: start shard: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ShardStartError) Unwrap() error {
	return e.Err
}

// TransportCreateError is returned when the embedded SDK transport
// cannot be created.
type TransportCreateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *TransportCreateError) Error() string {
	return "qrdb: create transport: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *TransportCreateError) Unwrap() error {
	return e.Err
}

// ClientCreateError is returned when the SDK client cannot be created.
type ClientCreateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ClientCreateError) Error() string {
	return "qrdb: create client: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ClientCreateError) Unwrap() error {
	return e.Err
}

// DataDirCreateError is returned when the data directory cannot be
// created or accessed.
type DataDirCreateError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *DataDirCreateError) Error() string {
	return "qrdb: create data directory " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DataDirCreateError) Unwrap() error {
	return e.Err
}

// UnknownBackendError is returned when the storage backend name is not
// recognized.
type UnknownBackendError struct {
	Backend string
}

// Error returns a human-readable description.
func (e *UnknownBackendError) Error() string {
	return "qrdb: unknown storage backend: " + e.Backend
}

// SMOpenError is returned when the state machine cannot be opened.
type SMOpenError struct {
	Backend string
	Err     error
}

// Error returns a human-readable description.
func (e *SMOpenError) Error() string {
	return "qrdb: open state machine (" + e.Backend + "): " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *SMOpenError) Unwrap() error {
	return e.Err
}

// LeaderElectionTimeoutError is returned when Raft leader election does
// not complete within the specified timeout.
type LeaderElectionTimeoutError struct {
	ShardID uint64
	Timeout time.Duration
}

// Error returns a human-readable description.
func (e *LeaderElectionTimeoutError) Error() string {
	return "qrdb: leader election timeout for shard " + strconv.FormatUint(e.ShardID, 10) + " after " + e.Timeout.String()
}

// SQLExecutionError is returned when a SQL query fails to execute.
type SQLExecutionError struct {
	Query string
	Err   error
}

// Error returns a human-readable description.
func (e *SQLExecutionError) Error() string {
	return "qrdb: SQL execution failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *SQLExecutionError) Unwrap() error {
	return e.Err
}

// SQLNotAvailableError is returned when SQL services have not been
// initialized (e.g., no embedded server is running).
type SQLNotAvailableError struct{}

// Error returns a human-readable description.
func (e *SQLNotAvailableError) Error() string {
	return "qrdb: SQL not available (embedded server not running)"
}

// InstanceAlreadyExistsError is returned when a database instance is
// already registered in the instance registry.
type InstanceAlreadyExistsError struct {
	Name string
}

// Error returns a human-readable description.
func (e *InstanceAlreadyExistsError) Error() string {
	return "qrdb: database instance already exists: " + strconv.Quote(e.Name)
}

// InstanceNotFoundError is returned when a database instance is not
// found in the instance registry.
type InstanceNotFoundError struct {
	Name string
}

// Error returns a human-readable description.
func (e *InstanceNotFoundError) Error() string {
	return "qrdb: database instance not found: " + strconv.Quote(e.Name)
}

// DaemonAlreadyRunningError is returned when the daemon is already running.
type DaemonAlreadyRunningError struct {
	PID int
}

// Error returns a human-readable description.
func (e *DaemonAlreadyRunningError) Error() string {
	return "qrdb: daemon already running with PID " + strconv.Itoa(e.PID)
}

// DaemonNotRunningError is returned when the daemon is not running.
type DaemonNotRunningError struct{}

// Error returns a human-readable description.
func (e *DaemonNotRunningError) Error() string {
	return "qrdb: daemon is not running"
}

// DaemonStartError is returned when the daemon fails to start.
type DaemonStartError struct {
	Err error
}

// Error returns a human-readable description.
func (e *DaemonStartError) Error() string {
	return "qrdb: daemon start failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DaemonStartError) Unwrap() error {
	return e.Err
}

// DaemonStopError is returned when the daemon fails to stop.
type DaemonStopError struct {
	Err error
}

// Error returns a human-readable description.
func (e *DaemonStopError) Error() string {
	return "qrdb: daemon stop failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DaemonStopError) Unwrap() error {
	return e.Err
}

// ConfigSaveError is returned when the embedded config cannot be
// persisted to disk.
type ConfigSaveError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *ConfigSaveError) Error() string {
	return "qrdb: save embedded config " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ConfigSaveError) Unwrap() error {
	return e.Err
}

// ConfigLoadError is returned when the embedded config cannot be
// loaded from disk.
type ConfigLoadError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *ConfigLoadError) Error() string {
	return "qrdb: load embedded config " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ConfigLoadError) Unwrap() error {
	return e.Err
}

// UnknownEngineError is returned when the store engine name is not
// recognized.
type UnknownEngineError struct {
	Engine string
}

// Error returns a human-readable description.
func (e *UnknownEngineError) Error() string {
	return "qrdb: unknown store engine: " + e.Engine
}

// NoSavedConfigError is returned when no saved embedded config exists.
type NoSavedConfigError struct{}

// Error returns a human-readable description.
func (e *NoSavedConfigError) Error() string {
	return "qrdb: no saved embedded configuration found"
}

// DuplicateDataDirError is returned when a new embedded profile's data
// directory resolves to the same path as an existing profile's data directory.
type DuplicateDataDirError struct {
	// ID is the profile ID that already owns the data directory.
	ID string
	// Path is the resolved absolute path of the conflicting data directory.
	Path string
}

// Error returns a human-readable description.
func (e *DuplicateDataDirError) Error() string {
	return "qrdb: data directory already used by profile " + e.ID + ": " + e.Path
}

// DataDirLockedError is returned when the Pebble storage engine cannot
// acquire the directory lock because another process already holds it.
// Only one embedded server may use a given data directory at a time.
type DataDirLockedError struct {
	// Path is the data directory that is locked.
	Path string
	// Err is the underlying lock error from Pebble.
	Err error
}

// Error returns a human-readable description.
func (e *DataDirLockedError) Error() string {
	return "qrdb: data directory is locked by another process; only one embedded server can use " +
		e.Path + " at a time: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DataDirLockedError) Unwrap() error {
	return e.Err
}
