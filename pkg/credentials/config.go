// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package credentials

const (
	// StrategyManual stores credentials in-memory with blocking retrieval
	// until a credential is submitted.
	StrategyManual = "manual"

	// StrategyBarrier stores credentials encrypted via the seal StorageBarrier.
	StrategyBarrier = "barrier"
)

// validStrategies is the set of recognized credential strategies.
var validStrategies = map[string]bool{
	StrategyManual:  true,
	StrategyBarrier: true,
}

// Config holds the configuration for a CredentialService.
type Config struct {
	// Strategy selects the credential storage strategy.
	// Valid values are "manual" and "barrier".
	Strategy string
}
