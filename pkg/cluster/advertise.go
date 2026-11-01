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

package cluster

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/config"
)

// APIProtocol names one of the client-facing API protocols a node serves. A
// member advertises an address per protocol so a client that has discovered the
// shard leader can reach it over the SAME protocol it is already speaking.
//
// Unix domain sockets are deliberately absent: a socket path is meaningful only
// on the node that owns it, so advertising one cluster-wide would publish an
// address no remote peer can dial.
type APIProtocol string

const (
	// APIProtocolREST is the HTTP/REST API.
	APIProtocolREST APIProtocol = "rest"

	// APIProtocolGRPC is the gRPC API.
	APIProtocolGRPC APIProtocol = "grpc"

	// APIProtocolQUIC is the native binary QUIC API.
	APIProtocolQUIC APIProtocol = "quic"

	// APIProtocolMCP is the Model Context Protocol API.
	APIProtocolMCP APIProtocol = "mcp"
)

// APIProtocols lists every remotely dialable protocol a member may advertise.
var APIProtocols = []APIProtocol{
	APIProtocolREST,
	APIProtocolGRPC,
	APIProtocolQUIC,
	APIProtocolMCP,
}

// apiProtocolSet indexes APIProtocols for O(1) validation.
var apiProtocolSet = func() map[APIProtocol]struct{} {
	set := make(map[APIProtocol]struct{}, len(APIProtocols))
	for _, p := range APIProtocols {
		set[p] = struct{}{}
	}
	return set
}()

// Valid reports whether p is a recognized, remotely dialable protocol.
func (p APIProtocol) Valid() bool {
	_, ok := apiProtocolSet[p]
	return ok
}

// String returns the protocol's wire name.
func (p APIProtocol) String() string {
	return string(p)
}

// ParseAPIProtocol converts a wire name into an APIProtocol. It returns an
// UnknownAPIProtocolError for a name outside APIProtocols.
func ParseAPIProtocol(name string) (APIProtocol, error) {
	p := APIProtocol(name)
	if !p.Valid() {
		return "", &UnknownAPIProtocolError{Name: name}
	}
	return p, nil
}

// APIEndpoint returns the member's advertised address for protocol p and
// whether the member advertises that protocol at all. A member registered by an
// older node — or one whose operator did not configure an advertise address —
// advertises nothing, and callers fall back to their pre-advertisement
// behavior rather than treating the absence as an error.
func (m Member) APIEndpoint(p APIProtocol) (string, bool) {
	addr, ok := m.APIEndpoints[p]
	if !ok || addr == "" {
		return "", false
	}
	return addr, true
}

// ValidateAPIEndpoints rejects an endpoint map containing an unknown protocol
// or an empty address. It is called at Advertiser construction so a
// misconfigured node fails fast instead of publishing an unusable descriptor.
func ValidateAPIEndpoints(endpoints map[APIProtocol]string) error {
	for proto, addr := range endpoints {
		if !proto.Valid() {
			return &UnknownAPIProtocolError{Name: string(proto)}
		}
		if addr == "" {
			return &EmptyAPIEndpointError{Protocol: proto}
		}
	}
	return nil
}

// unspecifiedHosts are bind hosts that must never be advertised: they tell
// peers "any local interface", which is meaningless from another machine.
var unspecifiedHosts = map[string]struct{}{
	"":        {},
	"0.0.0.0": {},
	"::":      {},
	"[::]":    {},
}

// AdvertisableHost reports whether host is a concrete address peers can dial.
// A wildcard bind host is not.
func AdvertisableHost(host string) bool {
	_, unspecified := unspecifiedHosts[host]
	return !unspecified
}

// BuildAPIEndpoints assembles a member's advertised endpoint map from the
// node's own advertise host and its configured per-protocol ports. A protocol
// whose port is absent or non-positive is not advertised, which is how a node
// states that it does not serve it.
//
// The host must be a concrete address peers can reach; a wildcard bind host
// yields a nil map (advertise nothing) rather than an address no peer can use.
// Ports are taken from configuration only — never computed from the Raft
// address — so NAT, split networking and non-default ports all work.
//
// It returns an UnknownAPIProtocolError when ports names a protocol outside
// APIProtocols.
func BuildAPIEndpoints(host string, ports map[APIProtocol]int) (map[APIProtocol]string, error) {
	for proto := range ports {
		if !proto.Valid() {
			return nil, &UnknownAPIProtocolError{Name: string(proto)}
		}
	}
	if !AdvertisableHost(host) {
		return nil, nil
	}
	endpoints := make(map[APIProtocol]string, len(ports))
	for _, proto := range APIProtocols {
		port, ok := ports[proto]
		if !ok || port <= 0 {
			continue
		}
		endpoints[proto] = net.JoinHostPort(host, strconv.Itoa(port))
	}
	if len(endpoints) == 0 {
		return nil, nil
	}
	return endpoints, nil
}

// AdvertisedAPIEndpoints returns the API endpoints a node configured by cfg
// publishes in its member record: the advertise host (Server.AdvertiseHost,
// else the host of Raft.RaftAddress -- the address peers already dial) joined
// with the port of every client-facing protocol the node serves. A disabled
// protocol is omitted, and a host no peer can dial (a wildcard) publishes
// nothing rather than an address that cannot be reached. The derivation lives
// here so the daemon and the embeddable node publish the same record from the
// same configuration.
func AdvertisedAPIEndpoints(cfg *config.Config) (map[APIProtocol]string, error) {
	host := cfg.Server.AdvertiseHost
	if host == "" {
		host = HostFromAddress(cfg.Raft.RaftAddress)
	}
	served := []struct {
		proto   APIProtocol
		enabled bool
		port    int
	}{
		{APIProtocolREST, cfg.Protocols.REST, cfg.Server.AdminPort},
		{APIProtocolGRPC, cfg.Protocols.GRPC, cfg.Server.GRPCPort},
		{APIProtocolQUIC, cfg.Protocols.QUIC, cfg.Server.QUICPort},
		{APIProtocolMCP, cfg.Protocols.MCP, cfg.MCP.Port},
	}
	ports := make(map[APIProtocol]int, len(served))
	for _, p := range served {
		if p.enabled {
			ports[p.proto] = p.port
		}
	}
	return BuildAPIEndpoints(host, ports)
}

// HostFromAddress extracts the host portion of a "host:port" address. It is the
// documented default for an advertise host: a node's Raft address is by
// definition the address peers already use to reach it, so its host is a sane
// starting point when no dedicated API advertise host is configured. It returns
// "" when addr has no host:port shape or the host is a wildcard.
func HostFromAddress(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || !AdvertisableHost(host) {
		return ""
	}
	return host
}

// AdvertiseConfig tunes the registration lease and its refresh cadence.
type AdvertiseConfig struct {
	// LeaseTTL is how long the registration survives without a refresh. Zero
	// uses DefaultLeaseTTL.
	LeaseTTL time.Duration

	// KeepAliveInterval is the self-refresh cadence. It must be comfortably
	// shorter than LeaseTTL. Zero uses DefaultKeepAliveInterval.
	KeepAliveInterval time.Duration
}

func (c AdvertiseConfig) withDefaults() AdvertiseConfig {
	if c.LeaseTTL <= 0 {
		c.LeaseTTL = DefaultLeaseTTL
	}
	if c.KeepAliveInterval <= 0 {
		c.KeepAliveInterval = DefaultKeepAliveInterval
	}
	return c
}

// AdvertiseOption customizes an Advertiser.
type AdvertiseOption func(*Advertiser)

// WithAdvertiseLogger sets the Advertiser's logger. A nil logger is ignored.
func WithAdvertiseLogger(l *slog.Logger) AdvertiseOption {
	return func(a *Advertiser) {
		if l != nil {
			a.logger = l
		}
	}
}

// Settler runs attempt until it answers with anything but its shard electing
// a leader or moving it, ctx ends, or the settler's own window passes, and
// returns the last answer. A registration at boot lands on a system shard a
// restarting peer may just have taken the leader from, and the election's
// errors say nothing about the registration itself. The composer supplies
// one built on service.SettleLeadership, whose classification
// (service.LeadershipTransient) this package cannot import: pkg/service
// imports it. Each attempt runs on the context the settler hands it, which
// may end before ctx: a write no leader ever answers is abandoned for a fresh
// attempt rather than holding the whole window.
type Settler func(ctx context.Context, attempt func(context.Context) error) error

// settleOnce is the Settler of an Advertiser given none: one attempt, whose
// answer stands.
func settleOnce(ctx context.Context, attempt func(context.Context) error) error {
	return attempt(ctx)
}

// WithAdvertiseSettler has Register wait out an election through s. A nil s
// is ignored, leaving one attempt.
func WithAdvertiseSettler(s Settler) AdvertiseOption {
	return func(a *Advertiser) {
		if s != nil {
			a.settle = s
		}
	}
}

// SealProbe reports whether the node barrier is sealed. While it is, the
// node's replicated log cannot take an entry, and an entry proposed into it
// halts the shard, so a write that is not a client's request must not be
// proposed at all.
type SealProbe interface {
	IsSealed() bool
}

// WithAdvertiseSealProbe makes the advertiser consult p before every write it
// would propose: while the node barrier is sealed a refresh is skipped, a
// registration is refused with *NodeSealedError, and a withdrawal is left to
// the record's lease. A nil p writes unconditionally.
func WithAdvertiseSealProbe(p SealProbe) AdvertiseOption {
	return func(a *Advertiser) {
		a.seal = p
	}
}

// Advertiser publishes the local node's Member descriptor — its Raft address
// and its client-facing API endpoints — into the cluster registry and keeps the
// backing lease alive, so every peer can map a replica ID onto an address it
// can actually dial.
//
// It performs NO Raft reconfiguration. That separation is deliberate: a node
// that wants its API endpoints discoverable should not be forced to also opt
// into automatic membership changes. MembershipManager composes an Advertiser
// for its own registration duties and layers reconfiguration on top, so there
// is exactly one registration code path.
type Advertiser struct {
	store  MembershipStore
	self   Member
	cfg    AdvertiseConfig
	logger *slog.Logger
	seal   SealProbe
	settle Settler

	mu        sync.Mutex
	started   bool
	closed    bool
	closeOnce sync.Once
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// NewAdvertiser creates an Advertiser for the given node. store must be
// non-nil and self.APIEndpoints must contain only known protocols with
// non-empty addresses.
func NewAdvertiser(store MembershipStore, self Member, cfg AdvertiseConfig, opts ...AdvertiseOption) (*Advertiser, error) {
	if store == nil {
		return nil, &MembershipError{Reason: "store must not be nil"}
	}
	if err := ValidateAPIEndpoints(self.APIEndpoints); err != nil {
		return nil, err
	}
	a := &Advertiser{
		store:  store,
		self:   self,
		cfg:    cfg.withDefaults(),
		logger: slog.Default(),
		settle: settleOnce,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a, nil
}

// Self returns the descriptor this Advertiser publishes.
func (a *Advertiser) Self() Member {
	return a.self
}

// Start registers the local node and begins refreshing its lease: Register
// and then KeepAlive, both on ctx. The refresh goroutine runs until ctx is
// cancelled or Close is called. Start is not idempotent: a second call on a
// started (or closed) Advertiser is an error.
func (a *Advertiser) Start(ctx context.Context) error {
	a.mu.Lock()
	err := a.startableLocked()
	a.mu.Unlock()
	if err != nil {
		return err
	}
	if err := a.Register(ctx); err != nil {
		return err
	}
	return a.KeepAlive(ctx)
}

// KeepAlive begins refreshing the registration Register wrote. The refresh
// goroutine runs until ctx is cancelled or Close is called, and ctx is used
// for nothing else; each refresh is bounded by the keep-alive interval. A
// caller whose registration was bounded by a startup deadline passes
// context.WithoutCancel of that context here, so the advertisement outlives
// the startup. A second call on a started (or closed) Advertiser is an error.
func (a *Advertiser) KeepAlive(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.startableLocked(); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	a.started = true
	a.cancel = cancel
	a.wg.Add(1)
	go a.refresh(runCtx)
	return nil
}

// startableLocked reports why the refresh loop cannot start: the Advertiser
// is closed or already started. The caller holds a.mu.
func (a *Advertiser) startableLocked() error {
	if a.closed {
		return &MembershipError{Reason: "advertiser is closed"}
	}
	if a.started {
		return &MembershipError{Reason: "advertiser already started"}
	}
	return nil
}

// Register writes the local node's descriptor once, without starting the
// refresh loop. MembershipManager calls it directly so it can roll back a
// failed start before any goroutine exists.
//
// The write waits out its shard electing a leader through the Settler
// (WithAdvertiseSettler); any other error, and an election that outlasts the
// settler, stands. Retrying a write whose outcome is unknown -- one a leader
// stepping down had already accepted -- cannot register the node twice: the
// record is the one key of this node's ID, and a retry writes it the same
// bytes again. A retry that holds a fresh lease rebinds the key to it, so the
// earlier lease's revocation or expiry no longer removes the record.
func (a *Advertiser) Register(ctx context.Context) error {
	if a.sealed() {
		return &NodeSealedError{Op: "register"}
	}
	data, err := json.Marshal(a.self)
	if err != nil {
		return &MembershipCauseError{Reason: "failed to marshal self", Cause: err}
	}
	if err := a.settle(ctx, func(attemptCtx context.Context) error {
		return a.store.PutMember(attemptCtx, a.self.NodeID, data, a.cfg.LeaseTTL)
	}); err != nil {
		return &MembershipCauseError{Reason: "failed to register self", Cause: err}
	}
	a.logger.Info("registered cluster member",
		"node_id", a.self.NodeID,
		"address", a.self.Address,
		"api_endpoints", len(a.self.APIEndpoints))
	return nil
}

// sealed reports whether the node barrier is sealed, so a write proposed now
// would reach a log that cannot take it.
func (a *Advertiser) sealed() bool {
	return a.seal != nil && a.seal.IsSealed()
}

// Deregister removes the local node's descriptor. It runs on a fresh context so
// shutdown still deregisters after the caller's context is cancelled.
func (a *Advertiser) Deregister() error {
	if a.sealed() {
		return &NodeSealedError{Op: "deregister"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), reconfigTimeout)
	defer cancel()
	return a.store.DeleteMember(ctx, a.self.NodeID)
}

// refresh periodically renews the registration so its lease does not expire
// while the node is healthy.
func (a *Advertiser) refresh(runCtx context.Context) {
	defer a.wg.Done()

	data, err := json.Marshal(a.self)
	if err != nil {
		a.logger.Error("keepalive marshal failed", "error", err)
		return
	}

	ticker := time.NewTicker(a.cfg.KeepAliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-runCtx.Done():
			return
		case <-ticker.C:
			// A sealed barrier is an expected state, not a failure: the
			// refresh is skipped, and the lease expiry sweep holds while
			// sealed too, so the record survives until the next tick after
			// the unseal renews it.
			if a.sealed() {
				continue
			}
			ctx, cancel := context.WithTimeout(runCtx, a.cfg.KeepAliveInterval)
			if err := a.store.PutMember(ctx, a.self.NodeID, data, a.cfg.LeaseTTL); err != nil {
				a.logger.Warn("membership keepalive failed", "node_id", a.self.NodeID, "error", err)
			}
			cancel()
		}
	}
}

// Close stops the refresh loop and deregisters the node. It is idempotent.
func (a *Advertiser) Close() error {
	var err error
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		cancel, started := a.cancel, a.started
		a.cancel = nil
		a.started = false
		a.mu.Unlock()

		if cancel != nil {
			cancel()
		}
		a.wg.Wait()
		if started {
			err = a.Deregister()
		}
	})
	return err
}
