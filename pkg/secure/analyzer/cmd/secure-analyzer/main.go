// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Command secure-analyzer is a standalone go vet tool that runs the two
// pkg/secure analyzers: securecheck (declaration and access rules 1-4) and
// securecopy (lifetimes of copies taken out of protected memory). Build it and
// use it via:
//
//	go build -o $(go env GOPATH)/bin/secure-analyzer \
//	    ./pkg/secure/analyzer/cmd/secure-analyzer
//	go vet -vettool=$(go env GOPATH)/bin/secure-analyzer ./...
package main

import (
	"golang.org/x/tools/go/analysis/multichecker"

	"github.com/jeremyhahn/go-qrdb/pkg/secure/analyzer"
)

func main() {
	multichecker.Main(analyzer.Analyzer, analyzer.CopyOutAnalyzer)
}
