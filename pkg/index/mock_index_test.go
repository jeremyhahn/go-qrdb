// Copyright (c) 2026 Jeremy Hahn
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

package index

// deletefailIndex is an Index whose Delete always returns deleteErr.
type deletefailIndex struct {
	deleteErr error
}

func (f *deletefailIndex) Put(_ []byte, _ []byte) error                  { return nil }
func (f *deletefailIndex) Get(_ []byte) ([][]byte, error)                { return nil, nil }
func (f *deletefailIndex) Delete(_ []byte, _ []byte) error               { return f.deleteErr }
func (f *deletefailIndex) Scan(_, _ []byte) (map[string][][]byte, error) { return nil, nil }
func (f *deletefailIndex) GetAll() ([][]byte, error)                     { return nil, nil }
func (f *deletefailIndex) Clear() error                                  { return nil }
func (f *deletefailIndex) Close() error                                  { return nil }

// putfailIndex is an Index whose Put always returns putErr.
// Delete succeeds so the old-value removal succeeds; Put then fails for the new value.
type putfailIndex struct {
	putErr error
}

func (f *putfailIndex) Put(_ []byte, _ []byte) error                  { return f.putErr }
func (f *putfailIndex) Get(_ []byte) ([][]byte, error)                { return nil, nil }
func (f *putfailIndex) Delete(_ []byte, _ []byte) error               { return nil }
func (f *putfailIndex) Scan(_, _ []byte) (map[string][][]byte, error) { return nil, nil }
func (f *putfailIndex) GetAll() ([][]byte, error)                     { return nil, nil }
func (f *putfailIndex) Clear() error                                  { return nil }
func (f *putfailIndex) Close() error                                  { return nil }
