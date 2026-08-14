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
	"crypto/tls"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounceInterval is the duration the file watcher waits after the last
// filesystem event before triggering a certificate reload. This prevents
// redundant reloads when a file is written in multiple steps (e.g.,
// truncate + write).
const debounceInterval = 100 * time.Millisecond

// CertReloader provides atomic TLS certificate hot-reload. It watches
// the parent directory of the certificate file for filesystem changes
// and automatically reloads the certificate and key pair when the files
// are modified. The current certificate is stored in an atomic pointer
// so that concurrent TLS handshakes always see a consistent certificate
// without locking.
type CertReloader struct {
	certPath string
	keyPath  string
	cert     atomic.Pointer[tls.Certificate]
	watcher  *fsnotify.Watcher
	logger   *slog.Logger
	done     chan struct{}
}

// NewCertReloader creates a CertReloader that loads the initial
// certificate from certPath and keyPath, then starts an fsnotify
// watcher on the parent directory of certPath. The watcher goroutine
// debounces filesystem events and calls Reload when the certificate
// or key files change. The caller must call Stop to release resources.
func NewCertReloader(certPath, keyPath string, logger *slog.Logger) (*CertReloader, error) {
	absCert, err := filepath.Abs(certPath)
	if err != nil {
		return nil, &CertReloadError{Path: certPath, Err: err}
	}
	absKey, err := filepath.Abs(keyPath)
	if err != nil {
		return nil, &CertReloadError{Path: keyPath, Err: err}
	}

	cert, err := tls.LoadX509KeyPair(absCert, absKey)
	if err != nil {
		return nil, &CertReloadError{Path: absCert, Err: err}
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, &CertReloadError{Path: absCert, Err: err}
	}

	// Watch the parent directory so we catch file renames, creates,
	// and writes that tools like kubectl or certbot may perform.
	certDir := filepath.Dir(absCert)
	keyDir := filepath.Dir(absKey)

	if err := watcher.Add(certDir); err != nil {
		_ = watcher.Close()
		return nil, &CertReloadError{Path: certDir, Err: err}
	}
	if keyDir != certDir {
		if err := watcher.Add(keyDir); err != nil {
			_ = watcher.Close()
			return nil, &CertReloadError{Path: keyDir, Err: err}
		}
	}

	r := &CertReloader{
		certPath: absCert,
		keyPath:  absKey,
		watcher:  watcher,
		logger:   logger,
		done:     make(chan struct{}),
	}
	r.cert.Store(&cert)

	go r.watch()

	logger.Info("certificate reloader started",
		"cert", absCert,
		"key", absKey,
		"watch_dir", certDir,
	)

	return r, nil
}

// GetCertificate returns the current TLS certificate from the atomic
// pointer. It is safe for concurrent use and satisfies the
// tls.Config.GetCertificate callback signature.
func (r *CertReloader) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	return r.cert.Load(), nil
}

// Reload loads the certificate and key from disk and atomically swaps
// the stored pointer. On success the new certificate is immediately
// visible to all TLS handshakes. On failure the previous certificate
// remains active and a CertReloadError is returned.
func (r *CertReloader) Reload() error {
	cert, err := tls.LoadX509KeyPair(r.certPath, r.keyPath)
	if err != nil {
		r.logger.Error("certificate reload failed, keeping previous certificate",
			"cert", r.certPath,
			"key", r.keyPath,
			"error", err,
		)
		return &CertReloadError{Path: r.certPath, Err: err}
	}

	r.cert.Store(&cert)

	r.logger.Info("certificate reloaded successfully",
		"cert", r.certPath,
		"key", r.keyPath,
	)
	return nil
}

// Stop shuts down the filesystem watcher and signals the watch
// goroutine to exit. It is safe to call Stop multiple times; subsequent
// calls are no-ops.
func (r *CertReloader) Stop() error {
	select {
	case <-r.done:
		// Already stopped.
		return nil
	default:
		close(r.done)
		return r.watcher.Close()
	}
}

// watch runs in a dedicated goroutine and listens for filesystem events.
// It uses a debounce timer to coalesce rapid writes into a single
// Reload call. Only events matching the certificate or key file paths
// trigger a reload.
func (r *CertReloader) watch() {
	certBase := filepath.Base(r.certPath)
	keyBase := filepath.Base(r.keyPath)

	var debounce *time.Timer

	for {
		select {
		case <-r.done:
			if debounce != nil {
				debounce.Stop()
			}
			return

		case event, ok := <-r.watcher.Events:
			if !ok {
				return
			}

			// Only react to writes and creates on the cert or key files.
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}

			base := filepath.Base(event.Name)
			if base != certBase && base != keyBase {
				continue
			}

			// Reset the debounce timer. If it fires, Reload is called.
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(debounceInterval, func() {
				if err := r.Reload(); err != nil {
					r.logger.Error("debounced reload failed", "error", err)
				}
			})

		case err, ok := <-r.watcher.Errors:
			if !ok {
				return
			}
			r.logger.Error("filesystem watcher error", "error", err)
		}
	}
}
