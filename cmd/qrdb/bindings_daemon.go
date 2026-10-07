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
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// daemonConfigDir is the directory under the user config path where
// daemon runtime files (PID file, log file) are stored.
const daemonConfigDir = "qrdb"

// DaemonStatus holds the current state of the qrdbd daemon process.
type DaemonStatus struct {
	Running bool   `json:"running"`
	PID     int    `json:"pid"`
	LogFile string `json:"logFile"`
}

// DaemonBinding manages the lifecycle of an external qrdbd daemon
// process from the GUI. It tracks the running process with an atomic
// pointer for lock-free access and persists a PID file for recovery
// across restarts.
type DaemonBinding struct {
	logger    *sdk.ConcreteLogger
	process   atomic.Pointer[os.Process]
	ctx       context.Context
	configDir string // override for testing; empty uses os.UserConfigDir
	logFile   string // app log file path from --log-file flag
}

// NewDaemonBinding creates a new DaemonBinding. The logFile parameter
// is the path to the application log file (from --log-file). When
// empty, the daemon log viewer falls back to ~/.config/qrdb/qrdbd.log.
func NewDaemonBinding(log *sdk.ConcreteLogger, logFile string) *DaemonBinding {
	return &DaemonBinding{logger: log, logFile: logFile}
}

// startup is called by Wails on application startup. It stores the
// Wails context for lifecycle management.
func (d *DaemonBinding) startup(ctx context.Context) {
	d.logger.Debug("DaemonBinding startup")
	d.ctx = ctx
}

// shutdown is called by Wails on application shutdown. It stops the
// daemon if one is being tracked.
func (d *DaemonBinding) shutdown(_ context.Context) {
	d.logger.Debug("DaemonBinding shutdown")
	if p := d.process.Load(); p != nil {
		_ = d.Stop()
	}
}

// Start launches the qrdbd daemon as a child process with the given
// configuration file path. The process runs in its own process group
// and its stdout/stderr are redirected to a log file.
func (d *DaemonBinding) Start(configPath string) error {
	d.logger.Debug("DaemonBinding Start called", "configPath", configPath)

	if p := d.process.Load(); p != nil {
		d.logger.Error("DaemonBinding Start failed", "error", "already running", "pid", p.Pid)
		return &DaemonAlreadyRunningError{PID: p.Pid}
	}

	configDir, err := d.resolveConfigDir()
	if err != nil {
		return &DaemonStartError{Err: err}
	}

	logPath := filepath.Join(configDir, "qrdbd.log")
	pidPath := filepath.Join(configDir, "qrdbd.pid")

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return &DaemonStartError{Err: err}
	}

	cmd := exec.Command("qrdbd", "start", "--config", configPath) // #nosec G204 -- configPath is a trusted file path from the user's own CLI invocation
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return &DaemonStartError{Err: err}
	}

	// Close the log file handle in the parent process; the child owns
	// the file descriptors now.
	_ = logFile.Close()

	// Write PID file for recovery.
	pidData := []byte(strconv.Itoa(cmd.Process.Pid))
	if err := os.WriteFile(pidPath, pidData, 0o600); err != nil {
		d.logger.Warn("DaemonBinding: failed to write PID file", "error", err)
	}

	d.process.Store(cmd.Process)
	d.logger.Debug("DaemonBinding Start completed", "pid", cmd.Process.Pid)
	return nil
}

// Stop sends SIGTERM to the tracked daemon process and waits for it
// to exit. It cleans up the PID file and clears the atomic pointer.
func (d *DaemonBinding) Stop() error {
	d.logger.Debug("DaemonBinding Stop called")

	p := d.process.Load()
	if p == nil {
		d.logger.Error("DaemonBinding Stop failed", "error", "not running")
		return &DaemonNotRunningError{}
	}

	if err := p.Signal(syscall.SIGTERM); err != nil {
		d.logger.Warn("DaemonBinding: SIGTERM failed, attempting kill", "error", err)
		_ = p.Kill()
	}

	// Wait for the process to exit and release resources.
	_, _ = p.Wait()

	d.process.Store(nil)
	d.cleanupPIDFile()

	d.logger.Debug("DaemonBinding Stop completed")
	return nil
}

// Restart stops the running daemon (if any) and starts a new one
// with the given configuration path.
func (d *DaemonBinding) Restart(configPath string) error {
	d.logger.Debug("DaemonBinding Restart called", "configPath", configPath)

	if p := d.process.Load(); p != nil {
		if err := d.Stop(); err != nil {
			return &DaemonStopError{Err: err}
		}
	}

	return d.Start(configPath)
}

// Status returns the current daemon status by checking the in-memory
// process pointer and falling back to the PID file on disk.
func (d *DaemonBinding) Status() (*DaemonStatus, error) {
	d.logger.Debug("DaemonBinding Status called")

	logPath, err := d.resolveLogPath()
	if err != nil {
		return nil, &DaemonStartError{Err: err}
	}

	configDir, err := d.resolveConfigDir()
	if err != nil {
		return nil, &DaemonStartError{Err: err}
	}

	// Check in-memory tracked process first.
	if p := d.process.Load(); p != nil {
		return &DaemonStatus{
			Running: true,
			PID:     p.Pid,
			LogFile: logPath,
		}, nil
	}

	// Fall back to PID file for processes started outside this session.
	pidPath := filepath.Join(configDir, "qrdbd.pid")
	pidData, err := os.ReadFile(pidPath)
	if err != nil {
		// No PID file means no daemon is running.
		return &DaemonStatus{
			Running: false,
			PID:     0,
			LogFile: logPath,
		}, nil
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		return &DaemonStatus{
			Running: false,
			PID:     0,
			LogFile: logPath,
		}, nil
	}

	// Check if the process is still alive. Sending signal 0 tests
	// for process existence without affecting it.
	proc, err := os.FindProcess(pid)
	if err != nil {
		return &DaemonStatus{Running: false, PID: 0, LogFile: logPath}, nil
	}

	if err := proc.Signal(syscall.Signal(0)); err != nil {
		// Process does not exist; clean up stale PID file.
		_ = os.Remove(pidPath)
		return &DaemonStatus{Running: false, PID: 0, LogFile: logPath}, nil
	}

	return &DaemonStatus{
		Running: true,
		PID:     pid,
		LogFile: logPath,
	}, nil
}

// GetLogs reads the last maxLines lines from the application log file.
// When the app was started with --log-file, that path is used.
// Otherwise falls back to ~/.config/qrdb/qrdbd.log.
// Returns an empty string if the log file does not exist.
func (d *DaemonBinding) GetLogs(maxLines int) (string, error) {
	d.logger.Debug("DaemonBinding GetLogs called", "maxLines", maxLines)

	logPath, err := d.resolveLogPath()
	if err != nil {
		return "", &DaemonStartError{Err: err}
	}

	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", &DaemonStartError{Err: err}
	}
	defer func() { _ = f.Close() }()

	// Read all lines into a ring buffer of size maxLines to avoid
	// loading the entire file into memory.
	lines := make([]string, 0, maxLines)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > maxLines {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return "", &DaemonStartError{Err: err}
	}

	return strings.Join(lines, "\n"), nil
}

// resolveLogPath returns the log file path. When the app was started
// with --log-file, that path is returned. Otherwise falls back to
// the default daemon log path under the config directory.
func (d *DaemonBinding) resolveLogPath() (string, error) {
	if d.logFile != "" {
		return d.logFile, nil
	}
	configDir, err := d.resolveConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "qrdbd.log"), nil
}

// resolveConfigDir returns the daemon config directory path, creating
// it if necessary. If configDir is set (e.g., for testing), it is
// used directly instead of querying os.UserConfigDir.
func (d *DaemonBinding) resolveConfigDir() (string, error) {
	if d.configDir != "" {
		if err := os.MkdirAll(d.configDir, 0o700); err != nil {
			return "", err
		}
		return d.configDir, nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(configDir, daemonConfigDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// cleanupPIDFile removes the PID file from the config directory.
func (d *DaemonBinding) cleanupPIDFile() {
	configDir, err := d.resolveConfigDir()
	if err != nil {
		d.logger.Warn("DaemonBinding: failed to resolve config dir for PID cleanup", "error", err)
		return
	}
	pidPath := filepath.Join(configDir, "qrdbd.pid")
	if err := os.Remove(pidPath); err != nil && !os.IsNotExist(err) {
		d.logger.Warn("DaemonBinding: failed to remove PID file", "error", err)
	}
}
