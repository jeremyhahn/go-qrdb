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

package statemachine

import "github.com/jeremyhahn/go-qrdb/pkg/capabilities"

// profileFields maps each state-machine Capability to the capability-profile
// flag it decides. A profile flag with no entry here is not a property of the
// state machine — vector search runs on its own storage, clustering and
// backup on the raft host — and Narrow leaves it alone.
var profileFields = map[Capability]func(*capabilities.Capabilities) *bool{
	CapabilityKV:          func(c *capabilities.Capabilities) *bool { return &c.KV },
	CapabilityBatch:       func(c *capabilities.Capabilities) *bool { return &c.Batch },
	CapabilityTransaction: func(c *capabilities.Capabilities) *bool { return &c.Transaction },
	CapabilityMVCC:        func(c *capabilities.Capabilities) *bool { return &c.MVCC },
	CapabilityIndex:       func(c *capabilities.Capabilities) *bool { return &c.Index },
	CapabilityLease:       func(c *capabilities.Capabilities) *bool { return &c.Lease },
	CapabilityWatch:       func(c *capabilities.Capabilities) *bool { return &c.Watch },
	CapabilityTimeSeries:  func(c *capabilities.Capabilities) *bool { return &c.TimeSeries },
}

// Narrow returns profile with every flag the state machine decides turned off
// unless the descriptor declares it. It only ever narrows: a flag the build,
// the run mode or the configuration already turned off stays off.
//
// Distributed locking is derived rather than declared: a lock is a leased key
// taken by a transaction and waited on through a watch, so it is available
// exactly when all three are.
func (d Descriptor) Narrow(profile capabilities.Capabilities) capabilities.Capabilities {
	for c, field := range profileFields {
		if flag := field(&profile); !d.Has(c) {
			*flag = false
		}
	}
	profile.Lock = profile.Lock && profile.Transaction && profile.Lease && profile.Watch
	return profile
}

// NarrowCapabilities narrows a raft node's capability profile — the build-tag
// x mode x configuration profile config.EffectiveCapabilities computes — to
// what the state machine it runs actually serves, so the node's capability
// report names the real sm_type rather than every feature the binary could
// serve on some other one. The (type, backend) pair resolves exactly as
// ResolveTyped resolves it.
func (r *Registry) NarrowCapabilities(profile capabilities.Capabilities, smType, smBackend string) (capabilities.Capabilities, error) {
	e, err := r.lookup(r.typedName(smType, smBackend))
	if err != nil {
		return capabilities.Capabilities{}, err
	}
	return e.descriptor.Narrow(profile), nil
}

// NarrowCapabilities narrows profile by a state machine in the default
// registry; see Registry.NarrowCapabilities.
func NarrowCapabilities(profile capabilities.Capabilities, smType, smBackend string) (capabilities.Capabilities, error) {
	return defaultRegistry.NarrowCapabilities(profile, smType, smBackend)
}
