/* Copyright 2016-2024 nix <https://keybase.io/nixn>

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
	"encoding/json"
	"io"
)

type commType[T any] struct {
	in  *json.Decoder
	out *json.Encoder
}

func newComm[T any](in io.Reader, out io.Writer) *commType[T] {
	comm := commType[T]{
		json.NewDecoder(in),
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
