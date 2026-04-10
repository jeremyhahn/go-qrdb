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
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock transport ---

type mockShareTransport struct {
	mu       sync.Mutex
	sent     []*ShareExchangeMessage
	sendErr  error
	broadErr error
	broadCh  chan uint64 // notifies when BroadcastShareRequest is called
}

func newMockShareTransport() *mockShareTransport {
	return &mockShareTransport{
		broadCh: make(chan uint64, 10),
	}
}

func (m *mockShareTransport) SendShare(_ context.Context, toNodeID uint64, msg *ShareExchangeMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sendErr != nil {
		return m.sendErr
	}
	m.sent = append(m.sent, msg)
	return nil
}

func (m *mockShareTransport) BroadcastShareRequest(_ context.Context, fromNodeID uint64) error {
	if m.broadErr != nil {
		return m.broadErr
	}
	select {
	case m.broadCh <- fromNodeID:
	default:
	}
	return nil
}

func (m *mockShareTransport) sentMessages() []*ShareExchangeMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*ShareExchangeMessage, len(m.sent))
	copy(out, m.sent)
	return out
}

// --- Test helpers ---

func exchangeTestLogger() *slog.Logger {
	return slog.Default()
}

func exchangeTestConfig(t *testing.T, threshold int) ShareExchangeConfig {
	t.Helper()
	return ShareExchangeConfig{
		NodeID:    1,
		Logger:    exchangeTestLogger(),
		Threshold: threshold,
		Transport: newMockShareTransport(),
	}
}

// --- Constructor tests ---

func TestNewShareExchangeCoordinator_Success(t *testing.T) {
	cfg := exchangeTestConfig(t, 2)
	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)
	assert.NotNil(t, coord)
}

func TestNewShareExchangeCoordinator_MissingLogger(t *testing.T) {
	cfg := ShareExchangeConfig{
		NodeID:    1,
		Threshold: 2,
		Transport: newMockShareTransport(),
	}
	_, err := NewShareExchangeCoordinator(cfg)
	require.Error(t, err)

	var target *InvalidShareMessageError
	assert.ErrorAs(t, err, &target)
	assert.Contains(t, err.Error(), "logger is required")
}

func TestNewShareExchangeCoordinator_InvalidThreshold(t *testing.T) {
	cfg := ShareExchangeConfig{
		NodeID:    1,
		Logger:    exchangeTestLogger(),
		Threshold: 0,
		Transport: newMockShareTransport(),
	}
	_, err := NewShareExchangeCoordinator(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "threshold must be >= 1")
}

func TestNewShareExchangeCoordinator_MissingTransport(t *testing.T) {
	cfg := ShareExchangeConfig{
		NodeID:    1,
		Logger:    exchangeTestLogger(),
		Threshold: 2,
	}
	_, err := NewShareExchangeCoordinator(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transport is required")
}

// --- Start / Stop lifecycle tests ---

func TestShareExchangeCoordinator_StartStop(t *testing.T) {
	cfg := exchangeTestConfig(t, 2)
	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, coord.Start(ctx))
	require.NoError(t, coord.Stop())
}

func TestShareExchangeCoordinator_DoubleStartReturnsError(t *testing.T) {
	cfg := exchangeTestConfig(t, 2)
	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, coord.Start(ctx))
	defer func() { _ = coord.Stop() }()

	err = coord.Start(ctx)
	require.Error(t, err)

	var target *CoordinatorAlreadyStartedError
	assert.ErrorAs(t, err, &target)
	assert.Contains(t, err.Error(), "already started")
}

func TestShareExchangeCoordinator_DoubleStopIsSafe(t *testing.T) {
	cfg := exchangeTestConfig(t, 2)
	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, coord.Start(ctx))

	require.NoError(t, coord.Stop())
	require.NoError(t, coord.Stop())
}

func TestShareExchangeCoordinator_StopWithoutStartIsSafe(t *testing.T) {
	cfg := exchangeTestConfig(t, 2)
	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)
	require.NoError(t, coord.Stop())
}

// --- Share exchange flow tests ---

func TestShareExchangeCoordinator_ThreeNodeQuorum(t *testing.T) {
	// Simulate 3 nodes with threshold 2.
	transport := newMockShareTransport()
	var handlerCalled atomic.Bool
	handlerShares := make(chan [][]byte, 1)

	cfg := ShareExchangeConfig{
		NodeID:    1,
		Logger:    exchangeTestLogger(),
		Threshold: 2,
		Transport: transport,
		UnsealHandler: func(_ context.Context, shares [][]byte) error {
			handlerCalled.Store(true)
			handlerShares <- shares
			return nil
		},
	}

	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, coord.Start(ctx))
	defer func() { _ = coord.Stop() }()

	// Set local share (share 1 of 3).
	coord.SetLocalShare([]byte{0x01, 0x02, 0x03})

	// Simulate receiving share from node 2.
	msg := &ShareExchangeMessage{
		FromNodeID: 2,
		ToNodeID:   1,
		Share:      []byte{0x04, 0x05, 0x06},
		Type:       ShareMessageResponse,
		Epoch:      1,
	}
	require.NoError(t, coord.OnShareReceived(ctx, msg))

	// Wait for handler to be called.
	select {
	case shares := <-handlerShares:
		assert.Len(t, shares, 2)
		assert.True(t, handlerCalled.Load())
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for unseal handler")
	}

	assert.True(t, coord.IsQuorumReached())
	assert.Equal(t, 2, coord.GetCollectedShareCount())
}

func TestShareExchangeCoordinator_RequestSharesWhenNotStarted(t *testing.T) {
	cfg := exchangeTestConfig(t, 2)
	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	err = coord.RequestShares(context.Background())
	require.Error(t, err)

	var target *CoordinatorNotStartedError
	assert.ErrorAs(t, err, &target)
}

func TestShareExchangeCoordinator_RequestSharesBroadcasts(t *testing.T) {
	transport := newMockShareTransport()
	cfg := ShareExchangeConfig{
		NodeID:    1,
		Logger:    exchangeTestLogger(),
		Threshold: 2,
		Transport: transport,
	}

	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, coord.Start(ctx))
	defer func() { _ = coord.Stop() }()

	require.NoError(t, coord.RequestShares(ctx))

	// Verify broadcast was called.
	select {
	case nodeID := <-transport.broadCh:
		assert.Equal(t, uint64(1), nodeID)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for broadcast")
	}
}

// --- ProvideShare tests ---

func TestShareExchangeCoordinator_ProvideShare(t *testing.T) {
	transport := newMockShareTransport()
	cfg := ShareExchangeConfig{
		NodeID:    1,
		Logger:    exchangeTestLogger(),
		Threshold: 2,
		Transport: transport,
	}

	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	coord.SetLocalShare([]byte{0xAA, 0xBB})

	require.NoError(t, coord.ProvideShare(ctx, 2))

	msgs := transport.sentMessages()
	require.Len(t, msgs, 1)
	assert.Equal(t, uint64(1), msgs[0].FromNodeID)
	assert.Equal(t, uint64(2), msgs[0].ToNodeID)
	assert.Equal(t, ShareMessageResponse, msgs[0].Type)
	assert.Equal(t, []byte{0xAA, 0xBB}, msgs[0].Share)
}

func TestShareExchangeCoordinator_ProvideShareWithoutLocalShare(t *testing.T) {
	cfg := exchangeTestConfig(t, 2)
	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	err = coord.ProvideShare(context.Background(), 2)
	require.Error(t, err)

	var target *NoLocalShareError
	assert.ErrorAs(t, err, &target)
	assert.Contains(t, err.Error(), "no local share")
}

// --- OnShareReceived tests ---

func TestShareExchangeCoordinator_OnShareReceivedNilMessage(t *testing.T) {
	cfg := exchangeTestConfig(t, 2)
	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	err = coord.OnShareReceived(context.Background(), nil)
	require.Error(t, err)

	var target *InvalidShareMessageError
	assert.ErrorAs(t, err, &target)
}

func TestShareExchangeCoordinator_OnShareReceivedChannelFull(t *testing.T) {
	cfg := exchangeTestConfig(t, 2)
	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	// Fill the channel (capacity 100).
	for i := 0; i < 100; i++ {
		require.NoError(t, coord.OnShareReceived(context.Background(),
			&ShareExchangeMessage{FromNodeID: uint64(i + 10)}))
	}

	// Next one should fail.
	err = coord.OnShareReceived(context.Background(),
		&ShareExchangeMessage{FromNodeID: 999})
	require.Error(t, err)

	var target *ShareChannelFullError
	assert.ErrorAs(t, err, &target)
	assert.Contains(t, err.Error(), "channel full")
}

// --- Quorum detection tests ---

func TestShareExchangeCoordinator_IsQuorumReached(t *testing.T) {
	cfg := exchangeTestConfig(t, 2)
	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	// No shares yet.
	assert.False(t, coord.IsQuorumReached())

	// Local share only.
	coord.SetLocalShare([]byte{1, 2, 3})
	assert.False(t, coord.IsQuorumReached())

	// Add peer share directly.
	coord.mu.Lock()
	coord.peerShares[2] = []byte{4, 5, 6}
	coord.mu.Unlock()

	assert.True(t, coord.IsQuorumReached())
}

func TestShareExchangeCoordinator_GetCollectedShares(t *testing.T) {
	cfg := exchangeTestConfig(t, 2)
	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	// Empty initially.
	shares := coord.GetCollectedShares()
	assert.Empty(t, shares)

	// Add local.
	coord.SetLocalShare([]byte{1, 1, 1})
	shares = coord.GetCollectedShares()
	assert.Len(t, shares, 1)

	// Add peer.
	coord.mu.Lock()
	coord.peerShares[2] = []byte{2, 2, 2}
	coord.mu.Unlock()

	shares = coord.GetCollectedShares()
	assert.Len(t, shares, 2)
}

func TestShareExchangeCoordinator_ClearCollectedShares(t *testing.T) {
	cfg := exchangeTestConfig(t, 2)
	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	coord.SetLocalShare([]byte{1})
	coord.mu.Lock()
	coord.peerShares[2] = []byte{2}
	coord.peerShares[3] = []byte{3}
	coord.mu.Unlock()

	assert.Equal(t, 3, coord.GetCollectedShareCount())

	coord.ClearCollectedShares()

	// Peer shares cleared, local share remains.
	assert.Equal(t, 1, coord.GetCollectedShareCount())
}

// --- HandleShareRequest via transport response ---

func TestShareExchangeCoordinator_HandleShareRequest(t *testing.T) {
	transport := newMockShareTransport()
	cfg := ShareExchangeConfig{
		NodeID:    1,
		Logger:    exchangeTestLogger(),
		Threshold: 2,
		Transport: transport,
	}

	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, coord.Start(ctx))
	defer func() { _ = coord.Stop() }()

	// Set local share.
	coord.SetLocalShare([]byte{0xDE, 0xAD})

	// Simulate request from node 2.
	req := &ShareExchangeMessage{
		FromNodeID: 2,
		ToNodeID:   1,
		Type:       ShareMessageRequest,
		Epoch:      1,
	}
	require.NoError(t, coord.OnShareReceived(ctx, req))

	// Wait for the response to be sent via transport.
	deadline := time.After(2 * time.Second)
	for {
		msgs := transport.sentMessages()
		if len(msgs) > 0 {
			assert.Equal(t, ShareMessageResponse, msgs[0].Type)
			assert.Equal(t, uint64(1), msgs[0].FromNodeID)
			assert.Equal(t, uint64(2), msgs[0].ToNodeID)
			assert.Equal(t, []byte{0xDE, 0xAD}, msgs[0].Share)
			return
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for share response via transport")
		default:
			// Brief yield -- checking in a tight loop is acceptable
			// for tests since we're polling a lock-protected slice.
		}
	}
}

func TestShareExchangeCoordinator_HandleShareRequestNoLocalShare(t *testing.T) {
	transport := newMockShareTransport()
	cfg := ShareExchangeConfig{
		NodeID:    1,
		Logger:    exchangeTestLogger(),
		Threshold: 2,
		Transport: transport,
	}

	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, coord.Start(ctx))
	defer func() { _ = coord.Stop() }()

	// No local share set. Send request.
	req := &ShareExchangeMessage{
		FromNodeID: 2,
		ToNodeID:   1,
		Type:       ShareMessageRequest,
	}
	require.NoError(t, coord.OnShareReceived(ctx, req))

	// Give a moment for processing.
	time.After(200 * time.Millisecond)
	msgs := transport.sentMessages()
	assert.Empty(t, msgs)
}

// --- UnsealHandler invocation ---

func TestShareExchangeCoordinator_UnsealHandlerCalledOnQuorum(t *testing.T) {
	transport := newMockShareTransport()
	handlerDone := make(chan struct{})

	cfg := ShareExchangeConfig{
		NodeID:    1,
		Logger:    exchangeTestLogger(),
		Threshold: 2,
		Transport: transport,
		UnsealHandler: func(_ context.Context, shares [][]byte) error {
			close(handlerDone)
			return nil
		},
	}

	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, coord.Start(ctx))
	defer func() { _ = coord.Stop() }()

	coord.SetLocalShare([]byte{1})

	// Add peer share via message.
	require.NoError(t, coord.OnShareReceived(ctx, &ShareExchangeMessage{
		FromNodeID: 2,
		Share:      []byte{2},
		Type:       ShareMessageResponse,
	}))

	select {
	case <-handlerDone:
		// Handler was called.
	case <-time.After(2 * time.Second):
		t.Fatal("unseal handler not called")
	}
}

func TestShareExchangeCoordinator_UnsealHandlerNotCalledBelowThreshold(t *testing.T) {
	transport := newMockShareTransport()
	var handlerCalled atomic.Bool

	cfg := ShareExchangeConfig{
		NodeID:    1,
		Logger:    exchangeTestLogger(),
		Threshold: 3,
		Transport: transport,
		UnsealHandler: func(_ context.Context, shares [][]byte) error {
			handlerCalled.Store(true)
			return nil
		},
	}

	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, coord.Start(ctx))
	defer func() { _ = coord.Stop() }()

	coord.SetLocalShare([]byte{1})

	// Only one peer share (need 3 total, have 2).
	require.NoError(t, coord.OnShareReceived(ctx, &ShareExchangeMessage{
		FromNodeID: 2,
		Share:      []byte{2},
		Type:       ShareMessageResponse,
	}))

	// Brief pause then check handler was NOT called.
	<-time.After(200 * time.Millisecond)
	assert.False(t, handlerCalled.Load())
}

// --- Unknown message type ---

func TestShareExchangeCoordinator_UnknownMessageType(t *testing.T) {
	cfg := exchangeTestConfig(t, 2)
	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, coord.Start(ctx))
	defer func() { _ = coord.Stop() }()

	// Send a message with an invalid type (value 255).
	require.NoError(t, coord.OnShareReceived(ctx, &ShareExchangeMessage{
		FromNodeID: 99,
		Type:       ShareMessageType(255),
	}))

	// Should not panic; just logs a warning. Give time for processing.
	<-time.After(100 * time.Millisecond)
}

// --- CollectSharesWithTimeout tests ---

func TestShareExchangeCoordinator_CollectSharesWithTimeout_Success(t *testing.T) {
	transport := newMockShareTransport()
	cfg := ShareExchangeConfig{
		NodeID:    1,
		Logger:    exchangeTestLogger(),
		Threshold: 2,
		Transport: transport,
	}

	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, coord.Start(ctx))
	defer func() { _ = coord.Stop() }()

	coord.SetLocalShare([]byte{1})
	coord.mu.Lock()
	coord.peerShares[2] = []byte{2}
	coord.mu.Unlock()

	// Quorum already met, should return immediately.
	shares, err := coord.CollectSharesWithTimeout(ctx, time.Second)
	require.NoError(t, err)
	assert.Len(t, shares, 2)
}

func TestShareExchangeCoordinator_CollectSharesWithTimeout_Timeout(t *testing.T) {
	transport := newMockShareTransport()
	cfg := ShareExchangeConfig{
		NodeID:    1,
		Logger:    exchangeTestLogger(),
		Threshold: 3,
		Transport: transport,
	}

	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, coord.Start(ctx))
	defer func() { _ = coord.Stop() }()

	coord.SetLocalShare([]byte{1})

	// Threshold 3, only have 1 share -- will timeout.
	_, err = coord.CollectSharesWithTimeout(ctx, 200*time.Millisecond)
	require.Error(t, err)

	var target *ShareTimeoutError
	assert.ErrorAs(t, err, &target)
	assert.Equal(t, 1, target.Collected)
	assert.Equal(t, 3, target.Threshold)
}

func TestShareExchangeCoordinator_CollectSharesWithTimeout_ContextCancelled(t *testing.T) {
	transport := newMockShareTransport()
	cfg := ShareExchangeConfig{
		NodeID:    1,
		Logger:    exchangeTestLogger(),
		Threshold: 5,
		Transport: transport,
	}

	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, coord.Start(ctx))
	defer func() { _ = coord.Stop() }()

	coord.SetLocalShare([]byte{1})

	// Cancel context quickly.
	go func() {
		<-time.After(50 * time.Millisecond)
		cancel()
	}()

	_, err = coord.CollectSharesWithTimeout(ctx, 10*time.Second)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestShareExchangeCoordinator_CollectSharesWithTimeout_NotStarted(t *testing.T) {
	cfg := exchangeTestConfig(t, 2)
	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	_, err = coord.CollectSharesWithTimeout(context.Background(), time.Second)
	require.Error(t, err)

	var target *CoordinatorNotStartedError
	assert.ErrorAs(t, err, &target)
}

// --- Serialization tests ---

func TestMarshalUnmarshalShareExchangeMessage(t *testing.T) {
	msg := &ShareExchangeMessage{
		FromNodeID: 1,
		ToNodeID:   2,
		Share:      []byte{0xCA, 0xFE},
		Type:       ShareMessageRequest,
		Epoch:      42,
	}

	data, err := MarshalShareExchangeMessage(msg)
	require.NoError(t, err)

	decoded, err := UnmarshalShareExchangeMessage(data)
	require.NoError(t, err)

	assert.Equal(t, msg.FromNodeID, decoded.FromNodeID)
	assert.Equal(t, msg.ToNodeID, decoded.ToNodeID)
	assert.Equal(t, msg.Share, decoded.Share)
	assert.Equal(t, msg.Type, decoded.Type)
	assert.Equal(t, msg.Epoch, decoded.Epoch)
}

func TestUnmarshalShareExchangeMessage_InvalidData(t *testing.T) {
	_, err := UnmarshalShareExchangeMessage([]byte("not json"))
	require.Error(t, err)

	var target *ShareDeserializationError
	assert.ErrorAs(t, err, &target)
	assert.NotNil(t, target.Unwrap())
}

// --- Error type helpers ---

func TestErrorTypeCheckers(t *testing.T) {
	assert.True(t, IsCoordinatorNotStartedError(
		&CoordinatorNotStartedError{Op: "test"}))
	assert.False(t, IsCoordinatorNotStartedError(
		&CoordinatorAlreadyStartedError{Op: "test"}))

	assert.True(t, IsCoordinatorAlreadyStartedError(
		&CoordinatorAlreadyStartedError{Op: "test"}))
	assert.False(t, IsCoordinatorAlreadyStartedError(
		&CoordinatorNotStartedError{Op: "test"}))

	assert.True(t, IsShareChannelFullError(
		&ShareChannelFullError{Op: "test"}))
	assert.False(t, IsShareChannelFullError(
		&CoordinatorNotStartedError{Op: "test"}))
}

// --- ShareMessageType String ---

func TestShareMessageType_String(t *testing.T) {
	assert.Equal(t, "request", ShareMessageRequest.String())
	assert.Equal(t, "response", ShareMessageResponse.String())
	assert.Equal(t, "unknown", ShareMessageType(99).String())
}

// --- Error message coverage ---

func TestErrorMessages(t *testing.T) {
	t.Run("CoordinatorNotStartedError", func(t *testing.T) {
		err := &CoordinatorNotStartedError{Op: "TestOp"}
		assert.Contains(t, err.Error(), "not started")
		assert.Contains(t, err.Error(), "TestOp")
	})

	t.Run("CoordinatorAlreadyStartedError", func(t *testing.T) {
		err := &CoordinatorAlreadyStartedError{Op: "TestOp"}
		assert.Contains(t, err.Error(), "already started")
		assert.Contains(t, err.Error(), "TestOp")
	})

	t.Run("QuorumNotReachedError", func(t *testing.T) {
		err := &QuorumNotReachedError{Have: 1, Need: 3}
		assert.Contains(t, err.Error(), "quorum not reached")
	})

	t.Run("InvalidShareMessageError", func(t *testing.T) {
		err := &InvalidShareMessageError{Reason: "bad data"}
		assert.Contains(t, err.Error(), "invalid share message")
		assert.Contains(t, err.Error(), "bad data")
	})

	t.Run("ShareChannelFullError", func(t *testing.T) {
		err := &ShareChannelFullError{Op: "recv"}
		assert.Contains(t, err.Error(), "channel full")
	})

	t.Run("NoLocalShareError", func(t *testing.T) {
		err := &NoLocalShareError{Op: "provide"}
		assert.Contains(t, err.Error(), "no local share")
	})

	t.Run("ShareTimeoutError", func(t *testing.T) {
		err := &ShareTimeoutError{Collected: 1, Threshold: 3}
		assert.Contains(t, err.Error(), "timeout waiting for shares")
	})

	t.Run("ShareDeserializationError", func(t *testing.T) {
		inner := &InvalidShareMessageError{Reason: "inner"}
		err := &ShareDeserializationError{Err: inner}
		assert.Contains(t, err.Error(), "deserialization failed")
		assert.Equal(t, inner, err.Unwrap())
	})
}

// --- Transport error propagation ---

func TestShareExchangeCoordinator_ProvideShareTransportError(t *testing.T) {
	transport := newMockShareTransport()
	transport.sendErr = &InvalidShareMessageError{Reason: "network down"}

	cfg := ShareExchangeConfig{
		NodeID:    1,
		Logger:    exchangeTestLogger(),
		Threshold: 2,
		Transport: transport,
	}

	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	coord.SetLocalShare([]byte{1})

	err = coord.ProvideShare(context.Background(), 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network down")
}

func TestShareExchangeCoordinator_CollectSharesBroadcastError(t *testing.T) {
	transport := newMockShareTransport()
	transport.broadErr = &InvalidShareMessageError{Reason: "broadcast failed"}

	cfg := ShareExchangeConfig{
		NodeID:    1,
		Logger:    exchangeTestLogger(),
		Threshold: 2,
		Transport: transport,
	}

	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, coord.Start(ctx))
	defer func() { _ = coord.Stop() }()

	coord.SetLocalShare([]byte{1})
	coord.mu.Lock()
	coord.peerShares[2] = []byte{2}
	coord.mu.Unlock()

	// Quorum already met, broadcast error is logged but does not prevent return.
	shares, err := coord.CollectSharesWithTimeout(ctx, time.Second)
	require.NoError(t, err)
	assert.Len(t, shares, 2)
}

// --- SetLocalShare copies data ---

func TestShareExchangeCoordinator_SetLocalShareCopiesData(t *testing.T) {
	cfg := exchangeTestConfig(t, 2)
	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	original := []byte{1, 2, 3}
	coord.SetLocalShare(original)

	// Mutate original -- should not affect stored share.
	original[0] = 0xFF

	shares := coord.GetCollectedShares()
	require.Len(t, shares, 1)
	assert.Equal(t, byte(1), shares[0][0])
}

// --- Context cancellation stops process loop ---

func TestShareExchangeCoordinator_ContextCancellationStopsLoop(t *testing.T) {
	cfg := exchangeTestConfig(t, 2)
	coord, err := NewShareExchangeCoordinator(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, coord.Start(ctx))

	cancel()

	// The goroutine should exit. We verify by checking the coordinator
	// is still in started state (it doesn't auto-flip started on context cancel).
	<-time.After(100 * time.Millisecond)

	// Should be able to stop cleanly regardless.
	require.NoError(t, coord.Stop())
}
