// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// ShareMessageType identifies the kind of share exchange message.
type ShareMessageType uint8

const (
	// ShareMessageRequest requests a share from a peer node.
	ShareMessageRequest ShareMessageType = iota

	// ShareMessageResponse carries a share in reply to a request.
	ShareMessageResponse
)

// String returns the human-readable name of the message type.
func (t ShareMessageType) String() string {
	switch t {
	case ShareMessageRequest:
		return "request"
	case ShareMessageResponse:
		return "response"
	default:
		return "unknown"
	}
}

// ShareExchangeMessage is the wire-format envelope exchanged between
// cluster nodes during auto-unseal share distribution.
type ShareExchangeMessage struct {
	// FromNodeID is the sender node ID.
	FromNodeID uint64 `json:"from_node_id"`

	// ToNodeID is the target node ID (0 for broadcast).
	ToNodeID uint64 `json:"to_node_id"`

	// Share is the Shamir share payload.
	Share []byte `json:"share,omitempty"`

	// Type distinguishes requests from responses.
	Type ShareMessageType `json:"type"`

	// Epoch is the seal epoch for replay protection.
	Epoch uint64 `json:"epoch"`
}

// ShareTransport abstracts the network layer for exchanging shares between nodes.
type ShareTransport interface {
	// SendShare sends a share to the specified node.
	SendShare(ctx context.Context, toNodeID uint64, msg *ShareExchangeMessage) error

	// BroadcastShareRequest requests shares from all known peers.
	BroadcastShareRequest(ctx context.Context, fromNodeID uint64) error
}

// UnsealHandler is called when sufficient shares have been collected.
type UnsealHandler func(ctx context.Context, shares [][]byte) error

// ShareExchangeConfig configures a ShareExchangeCoordinator.
type ShareExchangeConfig struct {
	// NodeID is this node's unique identifier.
	NodeID uint64

	// Logger is the structured logger.
	Logger *slog.Logger

	// Threshold is the minimum number of shares needed to unseal.
	Threshold int

	// Transport abstracts the network layer.
	Transport ShareTransport

	// UnsealHandler is invoked when quorum is reached.
	UnsealHandler UnsealHandler
}

// --- Typed errors ---

// CoordinatorNotStartedError is returned when an operation requires
// the coordinator to be started but it is not.
type CoordinatorNotStartedError struct {
	Op string
}

// Error returns a human-readable description of the error.
func (e *CoordinatorNotStartedError) Error() string {
	return "qrdb/seal: coordinator not started: " + e.Op
}

// CoordinatorAlreadyStartedError is returned when Start is called on
// an already-running coordinator.
type CoordinatorAlreadyStartedError struct {
	Op string
}

// Error returns a human-readable description of the error.
func (e *CoordinatorAlreadyStartedError) Error() string {
	return "qrdb/seal: coordinator already started: " + e.Op
}

// QuorumNotReachedError is returned when an operation requires quorum
// but insufficient shares have been collected.
type QuorumNotReachedError struct {
	Have int
	Need int
}

// Error returns a human-readable description of the error.
func (e *QuorumNotReachedError) Error() string {
	return "qrdb/seal: quorum not reached"
}

// InvalidShareMessageError is returned when a received share message
// fails validation.
type InvalidShareMessageError struct {
	Reason string
}

// Error returns a human-readable description of the error.
func (e *InvalidShareMessageError) Error() string {
	return "qrdb/seal: invalid share message: " + e.Reason
}

// ShareChannelFullError is returned when the internal share channel
// is full and a message cannot be enqueued.
type ShareChannelFullError struct {
	Op string
}

// Error returns a human-readable description of the error.
func (e *ShareChannelFullError) Error() string {
	return "qrdb/seal: share channel full: " + e.Op
}

// NoLocalShareError is returned when an operation requires the local
// share but none has been set.
type NoLocalShareError struct {
	Op string
}

// Error returns a human-readable description of the error.
func (e *NoLocalShareError) Error() string {
	return "qrdb/seal: no local share available: " + e.Op
}

// ShareTimeoutError is returned when share collection exceeds the
// configured deadline.
type ShareTimeoutError struct {
	Collected int
	Threshold int
}

// Error returns a human-readable description of the error.
func (e *ShareTimeoutError) Error() string {
	return "qrdb/seal: timeout waiting for shares"
}

// ShareDeserializationError is returned when a share message cannot
// be deserialized from JSON.
type ShareDeserializationError struct {
	Err error
}

// Error returns a human-readable description of the error.
func (e *ShareDeserializationError) Error() string {
	return "qrdb/seal: share message deserialization failed"
}

// Unwrap returns the underlying error.
func (e *ShareDeserializationError) Unwrap() error {
	return e.Err
}

// --- Message dispatch handler type ---

type shareMessageHandler func(ctx context.Context, msg *ShareExchangeMessage)

// --- Coordinator ---

// ShareExchangeCoordinator coordinates the exchange of Shamir shares
// between cluster nodes during auto-unseal. It is transport-agnostic,
// relying on the ShareTransport interface for network operations.
type ShareExchangeCoordinator struct {
	nodeID        uint64
	logger        *slog.Logger
	threshold     int
	transport     ShareTransport
	unsealHandler UnsealHandler

	// mu protects peerShares and localShare.
	mu         sync.RWMutex
	peerShares map[uint64][]byte
	localShare []byte

	// Atomic state flags (lock-free).
	started      atomic.Bool
	quorumCalled atomic.Bool

	// Channels for lifecycle and message passing.
	stopCh  chan struct{}
	shareCh chan *ShareExchangeMessage

	// Map-based dispatch for message types.
	handlers map[ShareMessageType]shareMessageHandler
}

// NewShareExchangeCoordinator creates a new ShareExchangeCoordinator.
// Returns a validation error if required configuration fields are missing.
func NewShareExchangeCoordinator(cfg ShareExchangeConfig) (*ShareExchangeCoordinator, error) {
	if cfg.Logger == nil {
		return nil, &InvalidShareMessageError{Reason: "logger is required"}
	}
	if cfg.Threshold < 1 {
		return nil, &InvalidShareMessageError{Reason: "threshold must be >= 1"}
	}
	if cfg.Transport == nil {
		return nil, &InvalidShareMessageError{Reason: "transport is required"}
	}

	c := &ShareExchangeCoordinator{
		nodeID:        cfg.NodeID,
		logger:        cfg.Logger,
		threshold:     cfg.Threshold,
		transport:     cfg.Transport,
		unsealHandler: cfg.UnsealHandler,
		peerShares:    make(map[uint64][]byte),
		stopCh:        make(chan struct{}),
		shareCh:       make(chan *ShareExchangeMessage, 100),
	}

	// Build map-based dispatch table for O(1) message routing.
	c.handlers = map[ShareMessageType]shareMessageHandler{
		ShareMessageRequest:  c.handleShareRequest,
		ShareMessageResponse: c.handleShareResponse,
	}

	return c, nil
}

// Start begins the share exchange coordination loop. Returns
// CoordinatorAlreadyStartedError if already running.
func (c *ShareExchangeCoordinator) Start(ctx context.Context) error {
	if c.started.Swap(true) {
		return &CoordinatorAlreadyStartedError{Op: "ShareExchangeCoordinator.Start"}
	}

	c.logger.Info("starting share exchange coordinator", "node_id", c.nodeID)

	go c.processLoop(ctx)

	return nil
}

// Stop gracefully shuts down the coordination loop. Calling Stop on a
// stopped coordinator is safe and returns nil.
func (c *ShareExchangeCoordinator) Stop() error {
	if !c.started.Swap(false) {
		return nil
	}

	c.logger.Info("stopping share exchange coordinator", "node_id", c.nodeID)
	close(c.stopCh)
	return nil
}

// processLoop drains the share channel until the context is cancelled
// or Stop is called.
func (c *ShareExchangeCoordinator) processLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case msg := <-c.shareCh:
			c.dispatchMessage(ctx, msg)
		}
	}
}

// dispatchMessage routes a message to the appropriate handler using
// the map-based dispatch table.
func (c *ShareExchangeCoordinator) dispatchMessage(ctx context.Context, msg *ShareExchangeMessage) {
	handler, ok := c.handlers[msg.Type]
	if !ok {
		c.logger.Warn("unknown share message type",
			"type", msg.Type,
			"from_node", msg.FromNodeID)
		return
	}
	handler(ctx, msg)
}

// handleShareRequest responds to a peer's request for our local share.
func (c *ShareExchangeCoordinator) handleShareRequest(ctx context.Context, msg *ShareExchangeMessage) {
	c.mu.RLock()
	localShare := c.localShare
	c.mu.RUnlock()

	if localShare == nil {
		c.logger.Warn("no local share available to send",
			"requester", msg.FromNodeID)
		return
	}

	c.logger.Info("sending share to requesting peer",
		"to_node", msg.FromNodeID)

	response := &ShareExchangeMessage{
		FromNodeID: c.nodeID,
		ToNodeID:   msg.FromNodeID,
		Share:      localShare,
		Type:       ShareMessageResponse,
		Epoch:      msg.Epoch,
	}

	if err := c.transport.SendShare(ctx, msg.FromNodeID, response); err != nil {
		c.logger.Error("failed to send share response",
			"to_node", msg.FromNodeID,
			"error", err)
	}
}

// handleShareResponse stores a received share and checks for quorum.
func (c *ShareExchangeCoordinator) handleShareResponse(ctx context.Context, msg *ShareExchangeMessage) {
	c.mu.Lock()
	c.peerShares[msg.FromNodeID] = msg.Share
	total := c.shareCountLocked()
	c.mu.Unlock()

	c.logger.Info("received share from peer",
		"from_node", msg.FromNodeID,
		"total_shares", total)

	// Check quorum and invoke handler exactly once.
	if total >= c.threshold && c.unsealHandler != nil {
		if c.quorumCalled.CompareAndSwap(false, true) {
			shares := c.GetCollectedShares()
			c.logger.Info("quorum reached, invoking unseal handler",
				"total_shares", len(shares),
				"threshold", c.threshold)
			if err := c.unsealHandler(ctx, shares); err != nil {
				c.logger.Error("unseal handler failed", "error", err)
				// Allow retry on failure.
				c.quorumCalled.Store(false)
			}
		}
	}
}

// RequestShares asks all peers for their shares via the transport layer.
func (c *ShareExchangeCoordinator) RequestShares(ctx context.Context) error {
	if !c.started.Load() {
		return &CoordinatorNotStartedError{Op: "ShareExchangeCoordinator.RequestShares"}
	}

	c.logger.Info("broadcasting share request to peers", "node_id", c.nodeID)
	return c.transport.BroadcastShareRequest(ctx, c.nodeID)
}

// ProvideShare sends this node's local share to a specific peer.
func (c *ShareExchangeCoordinator) ProvideShare(ctx context.Context, toNodeID uint64) error {
	c.mu.RLock()
	localShare := c.localShare
	c.mu.RUnlock()

	if localShare == nil {
		return &NoLocalShareError{Op: "ShareExchangeCoordinator.ProvideShare"}
	}

	c.logger.Info("providing share to peer", "to_node", toNodeID)

	msg := &ShareExchangeMessage{
		FromNodeID: c.nodeID,
		ToNodeID:   toNodeID,
		Share:      localShare,
		Type:       ShareMessageResponse,
	}

	return c.transport.SendShare(ctx, toNodeID, msg)
}

// OnShareReceived enqueues an incoming share message for processing.
// Returns ShareChannelFullError if the internal buffer is full.
func (c *ShareExchangeCoordinator) OnShareReceived(ctx context.Context, msg *ShareExchangeMessage) error {
	if msg == nil {
		return &InvalidShareMessageError{Reason: "message is nil"}
	}

	select {
	case c.shareCh <- msg:
		return nil
	default:
		return &ShareChannelFullError{Op: "ShareExchangeCoordinator.OnShareReceived"}
	}
}

// GetCollectedShares returns all collected shares (local + peer).
// The local share, if set, is always first.
func (c *ShareExchangeCoordinator) GetCollectedShares() [][]byte {
	c.mu.RLock()
	defer c.mu.RUnlock()

	shares := make([][]byte, 0, len(c.peerShares)+1)

	if c.localShare != nil {
		shares = append(shares, c.localShare)
	}

	for _, share := range c.peerShares {
		shares = append(shares, share)
	}

	return shares
}

// IsQuorumReached returns true if the collected share count meets or
// exceeds the configured threshold.
func (c *ShareExchangeCoordinator) IsQuorumReached() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.shareCountLocked() >= c.threshold
}

// SetLocalShare sets this node's Shamir share for auto-unseal exchange.
func (c *ShareExchangeCoordinator) SetLocalShare(share []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.localShare = make([]byte, len(share))
	copy(c.localShare, share)
}

// GetCollectedShareCount returns the total number of shares held
// (local + peer).
func (c *ShareExchangeCoordinator) GetCollectedShareCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.shareCountLocked()
}

// ClearCollectedShares removes all collected peer shares. The local
// share is not affected.
func (c *ShareExchangeCoordinator) ClearCollectedShares() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.peerShares = make(map[uint64][]byte)
	// Reset quorum flag so handler can fire again after re-collection.
	c.quorumCalled.Store(false)
}

// CollectSharesWithTimeout requests shares and waits until quorum is
// reached or the deadline expires. Uses a timer-based approach with
// channel select instead of time.Sleep.
func (c *ShareExchangeCoordinator) CollectSharesWithTimeout(ctx context.Context, timeout time.Duration) ([][]byte, error) {
	if !c.started.Load() {
		return nil, &CoordinatorNotStartedError{Op: "ShareExchangeCoordinator.CollectSharesWithTimeout"}
	}

	c.logger.Info("collecting shares from peers",
		"timeout", timeout,
		"threshold", c.threshold)

	// Request shares from all peers.
	if err := c.transport.BroadcastShareRequest(ctx, c.nodeID); err != nil {
		c.logger.Warn("failed to broadcast share request", "error", err)
	}

	// If quorum is already reached, return immediately.
	if c.IsQuorumReached() {
		return c.GetCollectedShares(), nil
	}

	// Poll with a ticker until quorum or deadline.
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, &ShareTimeoutError{
				Collected: c.GetCollectedShareCount(),
				Threshold: c.threshold,
			}
		case <-ticker.C:
			if c.IsQuorumReached() {
				return c.GetCollectedShares(), nil
			}
		}
	}
}

// shareCountLocked returns the total share count. Caller must hold mu.
func (c *ShareExchangeCoordinator) shareCountLocked() int {
	count := len(c.peerShares)
	if c.localShare != nil {
		count++
	}
	return count
}

// --- Serialization helpers ---

// MarshalShareExchangeMessage serializes a ShareExchangeMessage to JSON.
func MarshalShareExchangeMessage(msg *ShareExchangeMessage) ([]byte, error) {
	return json.Marshal(msg)
}

// UnmarshalShareExchangeMessage deserializes a ShareExchangeMessage from JSON.
func UnmarshalShareExchangeMessage(data []byte) (*ShareExchangeMessage, error) {
	var msg ShareExchangeMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, &ShareDeserializationError{Err: err}
	}
	return &msg, nil
}

// --- Error type checking helpers ---

// IsCoordinatorNotStartedError returns true if err is a CoordinatorNotStartedError.
func IsCoordinatorNotStartedError(err error) bool {
	var target *CoordinatorNotStartedError
	return errors.As(err, &target)
}

// IsCoordinatorAlreadyStartedError returns true if err is a CoordinatorAlreadyStartedError.
func IsCoordinatorAlreadyStartedError(err error) bool {
	var target *CoordinatorAlreadyStartedError
	return errors.As(err, &target)
}

// IsShareChannelFullError returns true if err is a ShareChannelFullError.
func IsShareChannelFullError(err error) bool {
	var target *ShareChannelFullError
	return errors.As(err, &target)
}
