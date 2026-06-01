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

type pdnsClientID interface {
	String() string
	SetClientID(clid string)
}

type pdnsClient struct {
	ID          pdnsClientID
	PdnsVersion uint
	Comm        *commType[pdnsRequest]
	in          io.ReadCloser
	out         io.WriteCloser
}

func newPdnsClient(ctx context.Context, id pdnsClientID, in io.ReadCloser, out io.WriteCloser) *pdnsClient {
	client := &pdnsClient{
		ID:          id,
		PdnsVersion: defaultPdnsVersion,
		Comm:        newComm[pdnsRequest](ctx, in, out),
		in:          in,
		out:         out,
	}
	return client
}

// TODO on fatal errors which are local to a client, don't stop the whole program

func (client *pdnsClient) Logf(level int, component ...string) func(string, ...any) func(...any) {
	return func(format string, args ...any) func(...any) {
		return RootLog.Logf(level, component...)(&client.ID, format, args...)
	}
}

func (client *pdnsClient) Fatalf(component ...string) func(string, ...any) func(...any) {
	return client.Logf(FatalLevel, component...)
}

func (client *pdnsClient) Errorf(component ...string) func(string, ...any) func(...any) {
	return client.Logf(ErrorLevel, component...)
}

func (client *pdnsClient) Warnf(component ...string) func(string, ...any) func(...any) {
	return client.Logf(WarningLevel, component...)
}

func (client *pdnsClient) Respond(response any) {
	client.Logf(2, "pdns")("response")(response)
	if err := client.Comm.write(response); err != nil {
		client.Fatalf("pdns")("failed to encode response: %s", err)("response", response)
	}
}

func (client *pdnsClient) Fatal(msg any) {
	s := fmt.Sprintf("%s", msg)
	client.Respond(makeResponse(false, s))
	client.Fatalf("pdns")("fatal error: %s", s)()
}

type pdnsClientRequest struct {
	Client    *pdnsClient
	RequestID uint64
	Request   *pdnsRequest
}

func (cr *pdnsClientRequest) Logf(level int, component ...string) func(string, ...any) func(...any) {
	return func(format string, args ...any) func(...any) {
		return func(fields ...any) {
			fields = Prepend(fields, "reqID", any(cr.RequestID))
			cr.Client.Logf(level, component...)(format, args...)(fields...)
		}
	}
}

func (cr *pdnsClientRequest) Errorf(component ...string) func(string, ...any) func(...any) {
	return cr.Logf(ErrorLevel, component...)
}
