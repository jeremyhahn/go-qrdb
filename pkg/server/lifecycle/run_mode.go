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

package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/util/filelock"
	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
)

// runModeMarker is the file in a data directory that records the run mode
// the directory was first started in.
const runModeMarker = ".qrdb-run-mode"

// ClaimRunMode records mode in dataDir, or refuses when dataDir belongs to
// the other mode. The standalone and raft modes keep separate stores in the
// same data directory and no migration exists between them, so a node
// started in the other mode would run on an empty system store and ignore
// the existing one.
//
// A present marker must name mode (*RunModeMismatchError otherwise, or
// *RunModeMarkerError when it is unreadable or names no known mode). A
// directory without a marker is fresh: the marker records mode, claimed
// under an exclusive lock so that of two concurrent first starts only one
// writes it and the other validates it. An empty
// mode is raft, the configuration default; any mode other than standalone
// or raft is refused with *RunModeInvalidError before dataDir is created,
// read or written, so an unknown mode never reaches the marker.
func ClaimRunMode(dataDir, mode string) error {
	if mode == "" {
		mode = capabilities.ModeRaft
	}
	if mode != capabilities.ModeStandalone && mode != capabilities.ModeRaft {
		return &RunModeInvalidError{Mode: mode}
	}
	marker := filepath.Join(dataDir, runModeMarker)
	data, err := os.ReadFile(marker) // #nosec G304 -- marker is the node's own data directory joined with a constant name
	switch {
	case err == nil:
		return checkRunMode(dataDir, marker, string(data), mode)
	case !errors.Is(err, os.ErrNotExist):
		return &RunModeMarkerError{Path: marker, Err: err}
	}
	return writeRunMode(dataDir, marker, mode)
}

// writeRunMode claims a directory that had no marker. Under an exclusive
// lock on <marker>.lock it re-reads the marker, validating one a concurrent
// start wrote meanwhile, and otherwise writes mode to marker atomically: a
// synced temporary file renamed over the marker, with the directory synced
// after the rename. The lock file stays in the directory: removing it would
// let a later claimant lock a fresh inode while another holds the old one.
func writeRunMode(dataDir, marker, mode string) (err error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return &RunModeMarkerError{Path: dataDir, Err: err}
	}
	// Two first starts can both find no marker. The exclusive lock makes the
	// re-read and the write one step, so the second start sees the first
	// one's marker instead of overwriting it, and the fixed .tmp name below
	// is never written by two claimants at once.
	lockPath := marker + ".lock"
	lock, err := filelock.Open(lockPath)
	if err != nil {
		return &RunModeMarkerError{Path: lockPath, Err: err}
	}
	defer func() {
		if cerr := lock.Close(); cerr != nil && err == nil {
			err = &RunModeMarkerError{Path: lockPath, Err: cerr}
		}
	}()
	if err := lock.Lock(); err != nil {
		return &RunModeMarkerError{Path: lockPath, Err: err}
	}
	defer func() {
		if uerr := lock.Unlock(); uerr != nil && err == nil {
			err = &RunModeMarkerError{Path: lockPath, Err: uerr}
		}
	}()
	data, err := os.ReadFile(marker) // #nosec G304 -- marker is the node's own data directory joined with a constant name
	switch {
	case err == nil:
		return checkRunMode(dataDir, marker, string(data), mode)
	case !errors.Is(err, os.ErrNotExist):
		return &RunModeMarkerError{Path: marker, Err: err}
	}
	tmp := marker + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- tmp is the node's own data directory joined with a constant name
	if err != nil {
		return &RunModeMarkerError{Path: tmp, Err: err}
	}
	if _, err := f.WriteString(mode + "\n"); err != nil {
		_ = f.Close()
		return &RunModeMarkerError{Path: tmp, Err: err}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return &RunModeMarkerError{Path: tmp, Err: err}
	}
	if err := f.Close(); err != nil {
		return &RunModeMarkerError{Path: tmp, Err: err}
	}
	if err := fsync.Rename(tmp, marker); err != nil {
		return &RunModeMarkerError{Path: marker, Err: err}
	}
	return nil
}

// checkRunMode validates a present marker's content against mode.
func checkRunMode(dataDir, marker, content, mode string) error {
	recorded := strings.TrimSpace(content)
	if recorded != capabilities.ModeStandalone && recorded != capabilities.ModeRaft {
		return &RunModeMarkerError{Path: marker, Content: recorded}
	}
	if recorded != mode {
		return &RunModeMismatchError{DataDir: dataDir, Recorded: recorded, Configured: mode}
	}
	return nil
}
