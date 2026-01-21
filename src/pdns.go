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
	in          io.ReadCloser
	out         io.WriteCloser
}

func newPdnsClient(ctx context.Context, id uint, in io.ReadCloser, out io.WriteCloser) *pdnsClient {
	client := &pdnsClient{
		ID:          id,
		PdnsVersion: defaultPdnsVersion,
		Comm:        newComm[pdnsRequest](ctx, in, out),
		log:         newLog(&id, "main", "pdns", "data"), // TODO timings
		in:          in,
		out:         out,
	}
	for comp, logger := range client.log {
		logger.SetLevel(log.logger(comp).GetLevel())
	}
	return client
}

// TODO on fatal errors which are local to a client, don't stop the whole program

func (client *pdnsClient) Respond(response any) {
	client.log.pdns(response).Tracef("response")
	if err := client.Comm.write(response); err != nil {
		client.log.pdns("response", response).Panicf("failed to encode response: %s", err)
	}
}

func (client *pdnsClient) Fatal(msg any) {
	s := fmt.Sprintf("%s", msg)
	client.Respond(makeResponse(false, s))
	client.log.main().Panicf("fatal error: %s", s)
}
