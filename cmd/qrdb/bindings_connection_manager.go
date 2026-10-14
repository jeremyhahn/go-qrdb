// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

const (
	// connectionsConfigFile is the file name for persisted connection profiles.
	connectionsConfigFile = "connections.json"

	// defaultConnectTimeout is the default timeout for external connections.
	defaultConnectTimeout = 10 * time.Second

	// connectionTypeEmbedded identifies an embedded (in-process) connection.
	connectionTypeEmbedded = "embedded"

	// connectionTypeExternal identifies a remote connection.
	connectionTypeExternal = "external"

	// connectionStatusDisconnected indicates the connection is not active.
	connectionStatusDisconnected = "disconnected"

	// connectionStatusConnecting indicates a connection attempt is in progress.
	connectionStatusConnecting = "connecting"

	// connectionStatusNeedsInit indicates the server requires first-run initialization.
	connectionStatusNeedsInit = "needs-init"

	// connectionStatusSealed indicates the server's barrier is sealed.
	connectionStatusSealed = "sealed"

	// connectionStatusConnected indicates the connection is active and the server is ready.
	connectionStatusConnected = "connected"

	// connectionStatusError indicates the connection encountered an error.
	connectionStatusError = "error"
)

// ProfileNotFoundError is returned when a connection profile ID is not found.
type ProfileNotFoundError struct {
	ID string
}

// Error returns a human-readable description.
func (e *ProfileNotFoundError) Error() string {
	return "qrdb: connection profile not found: " + e.ID
}

// ProfileValidationError is returned when a connection profile fails validation.
type ProfileValidationError struct {
	Field  string
	Reason string
}

// Error returns a human-readable description.
func (e *ProfileValidationError) Error() string {
	return "qrdb: invalid profile " + e.Field + ": " + e.Reason
}

// ConnectionProfile stores the persistent configuration for a saved connection.
type ConnectionProfile struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Type      string            `json:"type"` // "embedded" | "external"
	Embedded  *EmbeddedConfig   `json:"embedded,omitempty"`
	External  *ConnectionConfig `json:"external,omitempty"`
	CreatedAt string            `json:"createdAt"`
	LastUsed  string            `json:"lastUsed,omitempty"`
}

// ConnectionState represents the runtime state of a connection.
type ConnectionState struct {
	ProfileID  string `json:"profileId"`
	Status     string `json:"status"` // "disconnected" | "connecting" | "needs-init" | "sealed" | "connected" | "error"
	Error      string `json:"error,omitempty"`
	IsEmbedded bool   `json:"isEmbedded"`
}

// managedConnection holds the runtime state for a single active connection.
type managedConnection struct {
	profile ConnectionProfile
	client  *sdk.Client
	server  *EmbeddedServerBinding // nil for external connections
	state   atomic.Value           // stores ConnectionState
}

// loadState returns the current ConnectionState from the atomic value.
func (m *managedConnection) loadState() ConnectionState {
	if v := m.state.Load(); v != nil {
		return v.(ConnectionState) //nolint:errcheck // type assertion on a value this container only ever holds
	}
	return ConnectionState{ProfileID: m.profile.ID, Status: connectionStatusDisconnected}
}

// storeState atomically replaces the ConnectionState.
func (m *managedConnection) storeState(s ConnectionState) {
	m.state.Store(s)
}

// ConnectionManagerBinding manages multiple named database connections,
// supporting both embedded (in-process) and external (network) profiles.
// Profiles are persisted to disk at ~/.config/qrdb/connections.json and
// loaded automatically on startup.
type ConnectionManagerBinding struct {
	logger      *sdk.ConcreteLogger
	mu          sync.Mutex // guards profiles slice and disk writes
	profiles    []ConnectionProfile
	connections sync.Map     // profileID string → *managedConnection
	activeID    atomic.Value // stores string profileID
	ctx         context.Context
}

// NewConnectionManagerBinding creates a new ConnectionManagerBinding.
func NewConnectionManagerBinding(log *sdk.ConcreteLogger) *ConnectionManagerBinding {
	return &ConnectionManagerBinding{
		logger: log,
	}
}

// startup is called by Wails on application startup. It loads persisted
// profiles from disk, runs legacy migration if needed, and attempts
// auto-connect for all saved connections.
func (b *ConnectionManagerBinding) startup(ctx context.Context) {
	b.logger.Debug("ConnectionManagerBinding startup")
	b.ctx = ctx

	if err := b.migrate(ctx); err != nil {
		b.logger.Warn("connection manager: migration failed", "error", err)
	}

	if err := b.loadProfiles(); err != nil {
		b.logger.Warn("connection manager: failed to load profiles", "error", err)
		return
	}

	b.mu.Lock()
	profiles := make([]ConnectionProfile, len(b.profiles))
	copy(profiles, b.profiles)
	b.mu.Unlock()

	for _, p := range profiles {
		state, err := b.Connect(p.ID)
		if err != nil {
			b.logger.Warn("connection manager: auto-connect failed", "profileID", p.ID, "error", err)
			continue
		}
		b.logger.Debug("connection manager: auto-connected", "profileID", p.ID, "status", state.Status)
	}
}

// shutdown is called by Wails on application shutdown. It disconnects all
// active connections and stops any embedded servers.
func (b *ConnectionManagerBinding) shutdown(_ context.Context) {
	b.logger.Debug("ConnectionManagerBinding shutdown")
	b.connections.Range(func(key, value any) bool {
		profileID, _ := key.(string)
		if err := b.Disconnect(profileID); err != nil {
			b.logger.Warn("connection manager: disconnect failed on shutdown", "profileID", profileID, "error", err)
		}
		return true
	})
}

// ListProfiles returns a copy of all saved connection profiles.
func (b *ConnectionManagerBinding) ListProfiles() []ConnectionProfile {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]ConnectionProfile, len(b.profiles))
	copy(result, b.profiles)
	return result
}

// SaveProfile validates and upserts a connection profile to the in-memory
// list and persists the change to disk.
func (b *ConnectionManagerBinding) SaveProfile(p ConnectionProfile) error {
	b.logger.Debug("SaveProfile called", "profileID", p.ID, "name", p.Name, "type", p.Type)

	if err := validateProfile(p); err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// For embedded profiles backed by persistent storage, reject any
	// data-directory collision with a different profile. In-memory backends
	// share no on-disk state so they are exempt. Same-ID saves (updates) are
	// also exempt — they are updates, not new entries.
	if p.Type == connectionTypeEmbedded && p.Embedded != nil && isPersistentBackend(p.Embedded.StorageBackend) {
		newDir := resolveDataDir(p.Embedded.DataDir)
		if newDir != "" {
			for _, existing := range b.profiles {
				if existing.ID == p.ID {
					continue
				}
				if existing.Type != connectionTypeEmbedded || existing.Embedded == nil {
					continue
				}
				if !isPersistentBackend(existing.Embedded.StorageBackend) {
					continue
				}
				if resolveDataDir(existing.Embedded.DataDir) == newDir {
					return &DuplicateDataDirError{ID: existing.ID, Path: newDir}
				}
			}
		}
	}

	for i, existing := range b.profiles {
		if existing.ID == p.ID {
			b.profiles[i] = p
			return b.persistProfiles()
		}
	}

	b.profiles = append(b.profiles, p)
	return b.persistProfiles()
}

// DeleteProfile disconnects the profile if active, removes it from the
// in-memory list, and persists the change to disk.
func (b *ConnectionManagerBinding) DeleteProfile(id string) error {
	b.logger.Debug("DeleteProfile called", "profileID", id)

	if _, loaded := b.connections.Load(id); loaded {
		if err := b.Disconnect(id); err != nil {
			b.logger.Warn("connection manager: disconnect before delete failed", "profileID", id, "error", err)
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	idx := -1
	for i, p := range b.profiles {
		if p.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return &ProfileNotFoundError{ID: id}
	}

	b.profiles = append(b.profiles[:idx], b.profiles[idx+1:]...)

	// Clear active if it pointed to this profile.
	if b.GetActiveProfileID() == id {
		b.activeID.Store("")
	}

	return b.persistProfiles()
}

// Connect looks up the profile by ID, creates a managed connection, starts
// the embedded server or dials the external address, and probes barrier
// status to determine the final connection state.
//
// Idempotent: when the profile is already connected, the existing client is
// re-probed for its current barrier status and the refreshed state is
// returned. This lets the UI safely call Connect on a sidebar click even if
// the cached state drifted from reality (e.g., barrier was initialized after
// the initial connect).
func (b *ConnectionManagerBinding) Connect(profileID string) (*ConnectionState, error) {
	b.logger.Debug("Connect called", "profileID", profileID)

	if v, loaded := b.connections.Load(profileID); loaded {
		conn, _ := v.(*managedConnection)
		if conn != nil && conn.client != nil {
			status := b.probeBarrierStatus(conn.client, conn.profile)
			refreshed := ConnectionState{
				ProfileID:  conn.profile.ID,
				Status:     status,
				IsEmbedded: conn.profile.Type == connectionTypeEmbedded,
			}
			conn.storeState(refreshed)
			b.logger.Debug("Connect: re-probed existing connection", "profileID", profileID, "status", status)
			return &refreshed, nil
		}
	}

	p, err := b.findProfile(profileID)
	if err != nil {
		return nil, err
	}

	conn := &managedConnection{profile: p}
	conn.storeState(ConnectionState{
		ProfileID:  p.ID,
		Status:     connectionStatusConnecting,
		IsEmbedded: p.Type == connectionTypeEmbedded,
	})

	var client *sdk.Client

	switch p.Type {
	case connectionTypeEmbedded:
		client, err = b.connectEmbedded(conn, p)
	case connectionTypeExternal:
		client, err = b.connectExternal(p)
	default:
		return nil, &ProfileValidationError{Field: "type", Reason: "unsupported connection type: " + p.Type}
	}

	if err != nil {
		errState := ConnectionState{
			ProfileID:  p.ID,
			Status:     connectionStatusError,
			Error:      err.Error(),
			IsEmbedded: p.Type == connectionTypeEmbedded,
		}
		conn.storeState(errState)
		b.connections.Store(profileID, conn)
		return &errState, nil
	}

	conn.client = client

	status := b.probeBarrierStatus(client, p)
	finalState := ConnectionState{
		ProfileID:  p.ID,
		Status:     status,
		IsEmbedded: p.Type == connectionTypeEmbedded,
	}
	conn.storeState(finalState)
	b.connections.Store(profileID, conn)

	// Update last-used timestamp.
	b.touchProfile(profileID)

	b.logger.Debug("Connect completed", "profileID", profileID, "status", status)
	return &finalState, nil
}

// Disconnect stops the embedded server if applicable, closes the client,
// and removes the connection from the active set.
func (b *ConnectionManagerBinding) Disconnect(profileID string) error {
	b.logger.Debug("Disconnect called", "profileID", profileID)

	v, loaded := b.connections.LoadAndDelete(profileID)
	if !loaded {
		return nil
	}
	conn, _ := v.(*managedConnection)
	if conn == nil {
		return nil
	}

	if conn.server != nil && conn.server.IsEmbedded() {
		if err := conn.server.StopEmbedded(); err != nil {
			b.logger.Warn("connection manager: stop embedded failed", "profileID", profileID, "error", err)
		}
	}

	if conn.client != nil {
		if err := conn.client.Close(); err != nil {
			b.logger.Warn("connection manager: close client failed", "profileID", profileID, "error", err)
		}
	}

	disconnectedState := ConnectionState{
		ProfileID:  profileID,
		Status:     connectionStatusDisconnected,
		IsEmbedded: conn.profile.Type == connectionTypeEmbedded,
	}
	conn.storeState(disconnectedState)

	b.logger.Debug("Disconnect completed", "profileID", profileID)
	return nil
}

// GetState returns the current ConnectionState for the given profile ID,
// or nil if no connection record exists.
func (b *ConnectionManagerBinding) GetState(profileID string) *ConnectionState {
	v, ok := b.connections.Load(profileID)
	if !ok {
		return nil
	}
	conn, _ := v.(*managedConnection)
	if conn == nil {
		return nil
	}
	s := conn.loadState()
	return &s
}

// SetActive stores the profile ID as the currently active connection.
func (b *ConnectionManagerBinding) SetActive(profileID string) error {
	b.logger.Debug("SetActive called", "profileID", profileID)
	if _, err := b.findProfile(profileID); err != nil {
		return err
	}
	b.activeID.Store(profileID)
	return nil
}

// GetActiveProfileID returns the profile ID of the currently active connection,
// or an empty string if no connection is active.
func (b *ConnectionManagerBinding) GetActiveProfileID() string {
	v := b.activeID.Load()
	if v == nil {
		return ""
	}
	id, _ := v.(string)
	return id
}

// ActiveClient returns the SDK client for the currently active connection.
// Returns NotConnectedError when no active connection is set.
func (b *ConnectionManagerBinding) ActiveClient() (*sdk.Client, error) {
	id := b.GetActiveProfileID()
	if id == "" {
		return nil, &NotConnectedError{}
	}
	v, ok := b.connections.Load(id)
	if !ok {
		return nil, &NotConnectedError{}
	}
	conn, _ := v.(*managedConnection)
	if conn == nil || conn.client == nil {
		return nil, &NotConnectedError{}
	}
	return conn.client, nil
}

// activeKeyFile returns the client key file the active external connection
// was dialed with, or "" when the active connection is embedded or none is.
func (b *ConnectionManagerBinding) activeKeyFile() string {
	v, ok := b.connections.Load(b.GetActiveProfileID())
	if !ok {
		return ""
	}
	conn, _ := v.(*managedConnection)
	if conn == nil || conn.profile.External == nil {
		return ""
	}
	return conn.profile.External.KeyFile
}

// IsEmbeddedAvailable delegates to the package-level build-tag function
// to report whether embedded server support is compiled in.
func (b *ConnectionManagerBinding) IsEmbeddedAvailable() bool {
	return IsEmbeddedAvailable()
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// connectEmbedded starts an in-process server for the given profile and
// returns the resulting SDK client.
func (b *ConnectionManagerBinding) connectEmbedded(conn *managedConnection, p ConnectionProfile) (*sdk.Client, error) {
	if !IsEmbeddedAvailable() {
		return nil, &FeatureDisabledError{Feature: "embedded"}
	}
	if p.Embedded == nil {
		return nil, &ProfileValidationError{Field: "embedded", Reason: "embedded config is required for embedded connections"}
	}

	// Create a temporary ConnectionBinding to receive the injected client.
	tempConn := &ConnectionBinding{logger: b.logger}

	server := NewEmbeddedServerBinding(b.logger, tempConn, nil)
	if err := server.StartEmbedded(p.Embedded); err != nil {
		return nil, err
	}

	client := tempConn.client.Load()
	if client == nil {
		_ = server.StopEmbedded()
		return nil, &ClientCreateError{Err: &NotConnectedError{}}
	}

	conn.server = server
	return client, nil
}

// connectExternal dials the remote server described by the profile using
// the existing buildClient helper.
func (b *ConnectionManagerBinding) connectExternal(p ConnectionProfile) (*sdk.Client, error) {
	if p.External == nil {
		return nil, &ProfileValidationError{Field: "external", Reason: "external config is required for external connections"}
	}
	cfg := p.External
	return buildClient(b.ctx, cfg.Transport, cfg.Address, cfg.CAFile, cfg.CertFile, cfg.KeyFile, defaultConnectTimeout)
}

// probeBarrierStatus calls the barrier status endpoint to determine whether
// the server needs initialization, is sealed, or is ready. Returns
// connectionStatusConnected when the probe fails (barrier may be absent).
func (b *ConnectionManagerBinding) probeBarrierStatus(client *sdk.Client, p ConnectionProfile) string {
	ctx, cancel := context.WithTimeout(b.ctx, defaultConnectTimeout)
	defer cancel()

	resp, err := client.Barrier().BarrierStatus(ctx)
	if err != nil {
		b.logger.Debug("connection manager: barrier probe failed, assuming connected", "profileID", p.ID, "error", err)
		return connectionStatusConnected
	}

	if resp.InitializedAt == "" {
		return connectionStatusNeedsInit
	}
	if resp.Sealed {
		return connectionStatusSealed
	}
	return connectionStatusConnected
}

// findProfile looks up a profile by ID from the in-memory list.
func (b *ConnectionManagerBinding) findProfile(id string) (ConnectionProfile, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range b.profiles {
		if p.ID == id {
			return p, nil
		}
	}
	return ConnectionProfile{}, &ProfileNotFoundError{ID: id}
}

// touchProfile updates the LastUsed timestamp for the given profile ID and
// persists the change to disk. Errors are logged but not propagated.
func (b *ConnectionManagerBinding) touchProfile(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, p := range b.profiles {
		if p.ID == id {
			b.profiles[i].LastUsed = time.Now().UTC().Format(time.RFC3339)
			if err := b.persistProfiles(); err != nil {
				b.logger.Warn("connection manager: failed to update last-used timestamp", "profileID", id, "error", err)
			}
			return
		}
	}
}

// loadProfiles reads persisted connection profiles from disk into the
// in-memory slice. A missing file is treated as an empty list (not an error).
func (b *ConnectionManagerBinding) loadProfiles() error {
	path, err := b.profilesPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			b.mu.Lock()
			b.profiles = nil
			b.mu.Unlock()
			return nil
		}
		return &ConfigLoadError{Path: path, Err: err}
	}
	var profiles []ConnectionProfile
	if err := json.Unmarshal(data, &profiles); err != nil {
		return &ConfigLoadError{Path: path, Err: err}
	}
	b.mu.Lock()
	b.profiles = profiles
	b.mu.Unlock()
	b.logger.Debug("connection manager: profiles loaded", "path", path, "count", len(profiles))
	return nil
}

// persistProfiles serialises the in-memory profiles slice to disk.
// The caller must hold b.mu.
func (b *ConnectionManagerBinding) persistProfiles() error {
	path, err := b.profilesPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(b.profiles, "", "  ")
	if err != nil {
		return &ConfigSaveError{Path: path, Err: err}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return &ConfigSaveError{Path: path, Err: err}
	}
	b.logger.Debug("connection manager: profiles saved", "path", path, "count", len(b.profiles))
	return nil
}

// profilesPath returns the absolute path to the connections.json file,
// creating the parent directory if it does not yet exist.
func (b *ConnectionManagerBinding) profilesPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", &DataDirCreateError{Path: "~/.config", Err: err}
	}
	dir := filepath.Join(configDir, "qrdb")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", &DataDirCreateError{Path: dir, Err: err}
	}
	return filepath.Join(dir, connectionsConfigFile), nil
}

// migrate checks for a legacy embedded.json and, when connections.json does
// not yet exist, converts it to a default ConnectionProfile and saves it.
func (b *ConnectionManagerBinding) migrate(_ context.Context) error {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return &DataDirCreateError{Path: "~/.config", Err: err}
	}
	dir := filepath.Join(configDir, "qrdb")
	legacyPath := filepath.Join(dir, "embedded.json")
	newPath := filepath.Join(dir, connectionsConfigFile)

	// Only migrate when the legacy file exists and connections.json does not.
	if _, err := os.Stat(legacyPath); os.IsNotExist(err) {
		return nil
	}
	if _, err := os.Stat(newPath); err == nil {
		return nil
	}

	data, err := os.ReadFile(legacyPath)
	if err != nil {
		return &ConfigLoadError{Path: legacyPath, Err: err}
	}
	var cfg EmbeddedConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return &ConfigLoadError{Path: legacyPath, Err: err}
	}

	id, err := newUUID()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	profile := ConnectionProfile{
		ID:        id,
		Name:      "Local Database",
		Type:      connectionTypeEmbedded,
		Embedded:  &cfg,
		CreatedAt: now,
	}

	b.mu.Lock()
	b.profiles = []ConnectionProfile{profile}
	if err := b.persistProfiles(); err != nil {
		b.mu.Unlock()
		return err
	}
	b.mu.Unlock()

	// Remove the legacy file after successful migration.
	if err := os.Remove(legacyPath); err != nil && !os.IsNotExist(err) {
		b.logger.Warn("connection manager: failed to remove legacy embedded.json", "path", legacyPath, "error", err)
	}

	b.logger.Debug("connection manager: migrated legacy embedded.json", "profileID", id)
	return nil
}

// validateProfile returns a ProfileValidationError if p is missing required
// fields or has an unsupported type.
func validateProfile(p ConnectionProfile) error {
	if p.ID == "" {
		return &ProfileValidationError{Field: "id", Reason: "must not be empty"}
	}
	if p.Name == "" {
		return &ProfileValidationError{Field: "name", Reason: "must not be empty"}
	}
	switch p.Type {
	case connectionTypeEmbedded:
		if p.Embedded == nil {
			return &ProfileValidationError{Field: "embedded", Reason: "required for type \"embedded\""}
		}
	case connectionTypeExternal:
		if p.External == nil {
			return &ProfileValidationError{Field: "external", Reason: "required for type \"external\""}
		}
		if p.External.Address == "" {
			return &ProfileValidationError{Field: "external.address", Reason: "must not be empty"}
		}
	default:
		return &ProfileValidationError{Field: "type", Reason: "must be \"embedded\" or \"external\""}
	}
	return nil
}

// resolveDataDir expands a leading "~" to the user's home directory and cleans
// the path. It does not create directories; it is used only for comparison.
// An empty raw string resolves to the default qrdb data directory
// (~/.config/qrdb/data). Returns an empty string when home-directory lookup
// fails, which causes the collision check to be skipped rather than block saves.
func resolveDataDir(raw string) string {
	if raw == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return ""
		}
		return filepath.Clean(filepath.Join(configDir, "qrdb", "data"))
	}
	if strings.HasPrefix(raw, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Clean(raw)
		}
		raw = filepath.Join(home, raw[2:])
	}
	return filepath.Clean(raw)
}

// isPersistentBackend reports whether the named storage backend writes data to
// disk and therefore requires exclusive directory access via a lock file.
// In-memory backends share no on-disk state and are exempt from the
// data-directory collision check.
func isPersistentBackend(backend string) bool {
	switch backend {
	case "pebble", "pebbledb", "file", "":
		// Empty resolves to the "pebble" default at startup time.
		return true
	default:
		return false
	}
}

// newUUID generates a version-4 UUID using crypto/rand and returns it as a
// canonical hyphenated hex string (e.g., "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx").
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	// Set version 4 bits (bits 12–15 of byte 6).
	b[6] = (b[6] & 0x0f) | 0x40
	// Set variant bits (bits 6–7 of byte 8 to 10xxxxxx).
	b[8] = (b[8] & 0x3f) | 0x80

	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}
