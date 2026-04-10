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

package transport

import "context"

// DaemonStatus represents the status of the qrdbd daemon process.
type DaemonStatus struct {
	Running bool   `json:"running"`
	PID     int    `json:"pid,omitempty"`
	LogFile string `json:"logFile,omitempty"`
}

// DaemonClient provides lifecycle management for the qrdbd daemon process.
type DaemonClient interface {
	// StartDaemon starts the qrdbd daemon process.
	StartDaemon(ctx context.Context, configPath string) error

	// StopDaemon stops the qrdbd daemon process.
	StopDaemon(ctx context.Context) error

	// RestartDaemon restarts the qrdbd daemon process.
	RestartDaemon(ctx context.Context, configPath string) error

	// DaemonStatus returns the current status of the daemon.
	DaemonStatus(ctx context.Context) (*DaemonStatus, error)

	// DaemonLogs returns the last N lines of daemon logs.
	DaemonLogs(ctx context.Context, maxLines int) (string, error)
}
