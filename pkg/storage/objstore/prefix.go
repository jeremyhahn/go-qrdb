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

package objstore

import (
	"context"
	"io"
	"strconv"
	"strings"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
)

// WithObjectPrefix places every object the engine writes -- records, the
// format marker and the batch commit journal -- under prefix, so engines that
// share one provider (a bucket, or one memory store) never see each other's
// keys. prefix must end in "/" and must not start with one, every segment
// must be a plain name (no empty, "." or ".." segment, no backslash), and it
// must leave room for every name the engine writes within the provider's
// name limit, so it is at most maxObjectPrefixLen bytes. New refuses
// anything else with ObjectPrefixError. Keys are named within the limit the
// prefix leaves, so a prefix changes which keys are stored hashed.
func WithObjectPrefix(prefix string) Option {
	return func(o *options) { o.prefix = prefix }
}

// ObjectPrefixError is returned by New for a WithObjectPrefix value that is
// not a valid object-name prefix.
type ObjectPrefixError struct {
	Prefix string
}

// Error returns a human-readable description of the refused prefix.
func (e *ObjectPrefixError) Error() string {
	return `objstore: object prefix "` + e.Prefix + `" must not start with "/", must end with "/", ` +
		`must have no empty, "." or ".." segment and no "\", and must be at most ` +
		strconv.Itoa(maxObjectPrefixLen) + ` bytes`
}

// validObjectPrefix accepts no prefix, or one that is a relative object-name
// directory: no leading "/", a trailing "/", only plain-name segments, and
// short enough that every name the engine writes still fits the provider.
func validObjectPrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	if strings.HasPrefix(prefix, "/") || !strings.HasSuffix(prefix, "/") || strings.Contains(prefix, `\`) {
		return &ObjectPrefixError{Prefix: prefix}
	}
	// Every segment must be a plain name: an empty, "." or ".." segment is
	// one a path-joining provider resolves away on Get and Put but matches
	// literally on List, so two spellings would share one namespace.
	for _, segment := range strings.Split(strings.TrimSuffix(prefix, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return &ObjectPrefixError{Prefix: prefix}
		}
	}
	if len(prefix) > maxObjectPrefixLen {
		return &ObjectPrefixError{Prefix: prefix}
	}
	return nil
}

// maxObjectPrefixLen is the longest prefix that leaves room for every name
// the engine writes beneath it: a hashed key name, the format marker and the
// commit journal.
const maxObjectPrefixLen = objcommon.MaxKeyLength - max(hashedNameLen, len(formatMarkerName), len(journalName))

// prefixedStore namespaces a go-objstore Storage: every object name and list
// prefix it is given gets the prefix added, and every name it lists has the
// prefix stripped. Lifecycle policies are the provider's and pass through.
type prefixedStore struct {
	objcommon.Storage
	prefix string
}

func withPrefix(store objcommon.Storage, prefix string) objcommon.Storage {
	return &prefixedStore{Storage: store, prefix: prefix}
}

func (p *prefixedStore) strip(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if rest, ok := strings.CutPrefix(name, p.prefix); ok {
			out = append(out, rest)
		}
	}
	return out
}

func (p *prefixedStore) Put(key string, data io.Reader) error {
	return p.Storage.Put(p.prefix+key, data)
}

func (p *prefixedStore) PutWithContext(ctx context.Context, key string, data io.Reader) error {
	return p.Storage.PutWithContext(ctx, p.prefix+key, data)
}

func (p *prefixedStore) PutWithMetadata(ctx context.Context, key string, data io.Reader, metadata *objcommon.Metadata) error {
	return p.Storage.PutWithMetadata(ctx, p.prefix+key, data, metadata)
}

func (p *prefixedStore) Get(key string) (io.ReadCloser, error) {
	return p.Storage.Get(p.prefix + key)
}

func (p *prefixedStore) GetWithContext(ctx context.Context, key string) (io.ReadCloser, error) {
	return p.Storage.GetWithContext(ctx, p.prefix+key)
}

func (p *prefixedStore) GetMetadata(ctx context.Context, key string) (*objcommon.Metadata, error) {
	return p.Storage.GetMetadata(ctx, p.prefix+key)
}

func (p *prefixedStore) UpdateMetadata(ctx context.Context, key string, metadata *objcommon.Metadata) error {
	return p.Storage.UpdateMetadata(ctx, p.prefix+key, metadata)
}

func (p *prefixedStore) Delete(key string) error {
	return p.Storage.Delete(p.prefix + key)
}

func (p *prefixedStore) DeleteWithContext(ctx context.Context, key string) error {
	return p.Storage.DeleteWithContext(ctx, p.prefix+key)
}

func (p *prefixedStore) Exists(ctx context.Context, key string) (bool, error) {
	return p.Storage.Exists(ctx, p.prefix+key)
}

func (p *prefixedStore) List(prefix string) ([]string, error) {
	names, err := p.Storage.List(p.prefix + prefix)
	if err != nil {
		return nil, err
	}
	return p.strip(names), nil
}

func (p *prefixedStore) ListWithContext(ctx context.Context, prefix string) ([]string, error) {
	names, err := p.Storage.ListWithContext(ctx, p.prefix+prefix)
	if err != nil {
		return nil, err
	}
	return p.strip(names), nil
}

func (p *prefixedStore) ListWithOptions(ctx context.Context, opts *objcommon.ListOptions) (*objcommon.ListResult, error) {
	scoped := objcommon.ListOptions{Prefix: p.prefix}
	if opts != nil {
		scoped = *opts
		scoped.Prefix = p.prefix + opts.Prefix
	}
	res, err := p.Storage.ListWithOptions(ctx, &scoped)
	if err != nil || res == nil {
		return res, err
	}
	out := &objcommon.ListResult{NextToken: res.NextToken, Truncated: res.Truncated}
	for _, obj := range res.Objects {
		if obj == nil {
			continue
		}
		if rest, ok := strings.CutPrefix(obj.Key, p.prefix); ok {
			out.Objects = append(out.Objects, &objcommon.ObjectInfo{Key: rest, Metadata: obj.Metadata})
		}
	}
	out.CommonPrefixes = p.strip(res.CommonPrefixes)
	return out, nil
}

func (p *prefixedStore) Archive(key string, destination objcommon.Archiver) error {
	return p.Storage.Archive(p.prefix+key, destination)
}
