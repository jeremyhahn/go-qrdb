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

//go:build !qrdb_vlog

package vlog

import "context"

// SoftwareHeadSignerProvider is the stub of the software HeadSignerProvider in
// a build without qrdb_vlog: every signer it is asked for is refused.
type SoftwareHeadSignerProvider struct{}

var _ HeadSignerProvider = (*SoftwareHeadSignerProvider)(nil)

// NewSoftwareHeadSignerProvider returns the stub provider; keys is unused.
func NewSoftwareHeadSignerProvider(keys SigningKeyReader) *SoftwareHeadSignerProvider {
	return &SoftwareHeadSignerProvider{}
}

// HeadSigner refuses: head signing is not compiled in.
func (p *SoftwareHeadSignerProvider) HeadSigner(ctx context.Context, req HeadSignerRequest) (HeadSigner, error) {
	return nil, &NotCompiledError{}
}

// NewSoftwareHeadSigner refuses: head signing is not compiled in.
func NewSoftwareHeadSigner(name string, k SigningKey) (HeadSigner, error) {
	return nil, &NotCompiledError{}
}
