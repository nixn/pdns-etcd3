/* Copyright 2016-2025 nix <https://keybase.io/nixn>

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
	"fmt"
	"io"
)

type pdnsRequest struct {
	Method     string
	Parameters objectType[any]
}

func (req *pdnsRequest) String() string {
	return fmt.Sprintf("%s: %+v", req.Method, req.Parameters)
}

type pdnsClient struct {
	ID          uint
	PdnsVersion uint
	Comm        *commType[pdnsRequest]
	log         logType
	out         io.Closer
}

func newPdnsClient(id uint, in io.Reader, out interface {
	io.Writer
	io.Closer
}) *pdnsClient {
	return &pdnsClient{
		ID:          id,
		PdnsVersion: defaultPdnsVersion,
		Comm:        newComm[pdnsRequest](in, out),
		log:         newLog(fmt.Sprintf("[%d] ", id), "main", "pdns", "data"), // TODO timings
		out:         out,
	}
}

func (client *pdnsClient) respond(response any) {
	client.log.pdns().WithField("response", response).Tracef("response")
	if err := client.Comm.write(response); err != nil {
		client.log.pdns().WithError(err).WithField("response", response).Fatalf("failed to encode response")
	}
}
