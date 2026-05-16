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
	"errors"
	"fmt"
	"math/rand"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

type standaloneFunc func(context.Context, *WaitGroup, *url.URL)

var (
	standalones = map[string]standaloneFunc{
		"unix": unixListener,
		"http": httpListener,
	}
)

type unixClientID struct {
	id         uint64
	addr       net.Addr
}

func (id unixClientID) String() string {
	return fmt.Sprintf("%d,%s", id.id, id.addr)
}

func unixListener(ctx context.Context, wg *WaitGroup, u *url.URL) {
	if u.Path == "" {
		RootLog.Fatalf("conn", "unix", "conf")(nil, "the socket path cannot be empty")()
	}
	path := u.Path
	if rel := u.Query().Get("relative"); rel != "" {
		if rel, err := parseBoolean(rel); err != nil {
			RootLog.Fatalf("conn", "unix", "conf")(nil, "failed to parse the argument 'relative' as bool: %s", err)()
		} else if rel {
			path = filepath.Join(".", path)
		}
	}
	listenConfig := new(net.ListenConfig)
	socket, err := listenConfig.Listen(ctx, "unix", path)
	if err != nil {
		RootLog.Fatalf("conn", "unix", "init")(nil, "failed to create the socket: %s", err)(path)
	}
	defer closeNoError(socket)
	done := make(chan struct{})
	defer close(done)
	wg.Go("unixListener done", func(...any) {
		RootLog.Logf(4, "conn", "unix", "{done}")(nil, "waiting for done")()
		select {
		case <-ctx.Done():
		case <-done:
		}
		closeNoError(socket)
		RootLog.Logf(4, "conn", "unix", "{done}")(nil, "done")()
	})
	if err = os.Chmod(path, 0777); err != nil {
		RootLog.Errorf("conn", "unix")(nil, "failed to chmod socket to 0777: %s", err)(path)
	}
	RootLog.Infof("conn", "unix")(nil, "waiting for connections")()
	initialzed := func(client *pdnsClient) []string {
		client.Logf(3, "conn", "unix", "init")("initialized")()
		return nil
	}
	status.serving = true
	for nextClientID := uint64(1); ; nextClientID++ {
		conn, err := socket.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				RootLog.Logf(1, "conn", "unix")(nil, "socket was closed")("ncid", nextClientID)
				break
			}
			RootLog.Errorf("conn", "unix")(nil, "failed to accept new connection [%d]: %s", nextClientID, err)()
			continue
		}
		RootLog.Logf(1, "conn", "unix")(nil, "new connection [%d]: %+v", nextClientID, conn)()
		id := unixClientID{nextClientID, conn.RemoteAddr()}
		wg.Go(fmt.Sprintf("serve[%s]", id), func(...any) {
			defer recoverPanics(func(v any) bool {
				recoverFunc(v, fmt.Sprintf("unix: serve[%s]", id), false)
				return false
			})
			defer closeNoError(conn)
			client := newPdnsClient(ctx, id, conn, conn)
			serve(ctx, wg, client, &initialzed, nil)
			client.Logf(4, "conn", "unix")("serve returned normally")()
		})
	}
	status.serving = false
}

type httpWriter struct {
	http.ResponseWriter
}

func (w *httpWriter) Close() error {
	return nil
}

var (
	requestsCount struct {
		cur, max atomic.Int32
	}
)

type httpClientID struct {
	clientAddr string
	requestID  uint16
}

func (id httpClientID) String() string {
	return fmt.Sprintf("%s #%04x", id.clientAddr, id.requestID)
}

func httpListener(ctx context.Context, wg *WaitGroup, u *url.URL) {
	if u.Hostname() == "" {
		RootLog.Fatalf("conn", "http", "conf")(nil, "<listen-address> may not be empty")()
	}
	if u.Port() == "" {
		RootLog.Fatalf("conn", "http", "conf")(nil, "<listen-port> may not be empty")()
	}
	RootLog.Logf(1, "conn", "http", "init")(nil, "creating server")(u.Host)
	socket, err := net.Listen("tcp", u.Host)
	if err != nil {
		RootLog.Fatalf("conn", "http")(nil, "failed to create TCP listening socket: %s", err)(u.Host)
	}
	defer closeNoError(socket)
	headerIsMT := func(client *pdnsClient, r *http.Request, header string, mt string) bool {
		mth := r.Header.Get(header)
		if mth == "" {
			return false
		}
		mediatype, _, err := mime.ParseMediaType(mth)
		if err != nil {
			client.Logf(3, "conn", "http")("failed to parse media type: %s", err)(mth)
			return false
		}
		return mediatype == mt
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := httpClientID{r.RemoteAddr, uint16(rand.Uint32())}
		idStr := id.String()
		wg.Register(idStr)
		defer wg.Done(idStr)
		client := newPdnsClient(ctx, id, r.Body, &httpWriter{w})
		client.Logf(2, "conn", "http")("new request")("method", r.Method, "url", r.URL.String, "header", r.Header) // lesser debug level due to no real request information here (determined later from body)
		if r.Method != http.MethodPost {
			client.Logf(1, "conn", "http")("non-POST method")(r.Method)
			http.Error(w, "only POST allowed", http.StatusMethodNotAllowed)
			return
		}
		if !headerIsMT(client, r, "Content-Type", "text/javascript") {
			client.Logf(1, "conn", "http")("non-TJS content-type (header)")(Supplier1(r.Header.Get, "Content-Type"))
			http.Error(w, "Content-Type must be text/javascript", http.StatusUnsupportedMediaType)
			return
		}
		if !headerIsMT(client, r, "Accept", "application/json") {
			client.Logf(1, "conn", "http")("non-JSON accept header")(Supplier1(r.Header.Get, "Accept"))
			http.Error(w, "Accept must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		cur := requestsCount.cur.Add(1)
		defer requestsCount.cur.Add(-1)
		requestsCount.max.CompareAndSwap(cur-1, cur)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		serve(ctx, wg, client, nil, nil)
		client.Logf(4, "conn", "http")("serve returned normally")()
	})
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       10 * time.Second,
	}
	done := make(chan struct{})
	wg.Go("shutdown http", func(...any) {
		defer close(done)
		RootLog.Logf(3, "conn", "http", "{shutdown}")(nil, "waiting for shutdown signal")()
		<-ctx.Done()
		timeout := 7 * time.Second
		RootLog.Logf(2, "conn", "http", "{shutdown}")(nil, "shutting down (timeout: %s)", timeout)()
		shutdownCtx, shutdownCancel := context.WithTimeout(ctx, timeout)
		defer shutdownCancel()
		if err = server.Shutdown(shutdownCtx); err != nil {
			RootLog.Errorf("conn", "http", "{shutdown}")(nil, "Shutdown() failed: %s; using Close()", err)()
			if err = server.Close(); err != nil {
				RootLog.Errorf("conn", "http", "{shutdown}")(nil, "Close() failed: %s", err)()
			} else {
				RootLog.Logf(2, "conn", "http", "{shutdown}")(nil, "Close() succeeded")()
			}
		} else {
			RootLog.Logf(2, "conn", "http", "{shutdown}")(nil, "Shutdown() succeeded")()
		}
	})
	RootLog.Infof("conn", "http")(nil, "starting server")(socket.Addr)
	status.serving = true
	err = server.Serve(socket)
	status.serving = false
	if !errors.Is(err, http.ErrServerClosed) {
		RootLog.Fatalf("conn", "http")(nil, "Serve() failed: %v", err)(err)
	}
	RootLog.Logf(2, "conn", "http")(nil, "waiting for server to complete shutdown")()
	<-done
	RootLog.Logf(4, "conn", "http")(nil, "server shutdown complete")()
}
