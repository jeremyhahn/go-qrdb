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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/seal"

// ---------------------------------------------------------------------------
// Strategy registry
// ---------------------------------------------------------------------------

// StrategyFactory constructs a SealingStrategy on demand.
type StrategyFactory = seal.StrategyFactory

// NewDefaultStrategyRegistry creates a new default strategy registry.
var NewDefaultStrategyRegistry = seal.NewDefaultStrategyRegistry

// ---------------------------------------------------------------------------
// None strategy
// ---------------------------------------------------------------------------

// StrategyNone identifies the no-op (unsealed) sealing strategy.
const StrategyNone = seal.StrategyNone

// NewNoneStrategy creates a new no-op sealing strategy.
var NewNoneStrategy = seal.NewNoneStrategy
