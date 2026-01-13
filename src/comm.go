/* Copyright 2016-2026 nix <https://keybase.io/nixn>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. */

package src

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (cr *contextReader) Read(p []byte) (int, error) {
	select {
	case <-cr.ctx.Done():
		return 0, fmt.Errorf("read()[1]: %s", cr.ctx.Err())
	default:
	}
	// perform actual read
	n, err := cr.reader.Read(p)
	if err != nil {
		return n, err
	}
	// fail fast if canceled mid-read
	select {
	case <-cr.ctx.Done():
		return 0, fmt.Errorf("read()[2]: %s", cr.ctx.Err())
	default:
		return n, nil
	}
}

type commType[T any] struct {
	in  *json.Decoder
	out *json.Encoder
}

func newComm[T any](ctx context.Context, in io.Reader, out io.Writer) *commType[T] {
	comm := commType[T]{
		json.NewDecoder(&contextReader{ctx, in}),
		json.NewEncoder(out),
	}
	comm.out.SetEscapeHTML(false)
	return &comm
}

func (comm *commType[T]) read() (*T, error) {
	var data T
	err := comm.in.Decode(&data)
	return &data, err
}

func (comm *commType[T]) write(data any) error {
	return comm.out.Encode(data)
}
