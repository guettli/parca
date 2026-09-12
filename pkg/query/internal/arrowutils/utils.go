// Copyright 2026 The Parca Authors
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

package arrowutils

import (
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// ArrayConcatenator is an object that helps callers keep track of a slice of
// arrays and concatenate them into a single one when needed. This is more
// efficient and memory safe than using a builder.
type ArrayConcatenator struct {
	arrs []arrow.Array
}

func (c *ArrayConcatenator) Add(arr arrow.Array) {
	c.arrs = append(c.arrs, arr)
}

func (c *ArrayConcatenator) NewArray(mem memory.Allocator) (arrow.Array, error) {
	arr, err := array.Concatenate(c.arrs, mem)
	if err != nil {
		return nil, err
	}
	c.arrs = c.arrs[:0]
	return arr, err
}

func (c *ArrayConcatenator) Len() int {
	return len(c.arrs)
}

func (c *ArrayConcatenator) Release() {
	for _, arr := range c.arrs {
		arr.Release()
	}
	c.arrs = c.arrs[:0]
}
