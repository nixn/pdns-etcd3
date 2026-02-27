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

func unixListener(ctx context.Context, wg *WaitGroup, u *url.URL) {
	if u.Path == "" {
		log.main().Panicf("unix: the socket path cannot be empty")
	}
	path := u.Path
	if rel := u.Query().Get("relative"); rel != "" {
		if rel, err := parseBoolean(rel); err != nil {
			log.main().Panicf("unix: failed to parse the 'relative' argument as bool: %s", err)
		} else if rel {
			path = filepath.Join(".", path)
		}
	}
	listenConfig := new(net.ListenConfig)
	socket, err := listenConfig.Listen(ctx, "unix", path)
	if err != nil {
		log.main().Panicf("unix: failed to create a socket at %s: %s", path, err)
	}
	defer closeNoError(socket)
	done := make(chan struct{})
	defer close(done)
	wg.Go("unixListener done", func(_ any) {
		select {
		case <-ctx.Done():
		case <-done:
		}
		closeNoError(socket)
	}, nil)
	if err = os.Chmod(path, 0777); err != nil {
		log.main().Errorf("unix: failed to chmod socket to 0777: %s", err)
	}
	log.main().Info("unix: waiting for connections")
	initialzed := func(client *pdnsClient) []string {
		client.log.pdns().Trace("initialzed")
		return nil
	}
	status.serving = true
	var nextClientID uint64 = 1
	for {
		conn, err := socket.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				log.main().Debug("unix: socket was closed")
				break
			}
			log.main().Errorf("unix: failed to accept new connection: %s", err)
			continue
		}
		log.main().Debugf("unix: new connection [%d]: %+v", nextClientID, conn)
		wg.Go(fmt.Sprintf("serve[%d]", nextClientID), func(clientID_ any) {
			clientID := clientID_.(uint64)
			defer recoverPanics(func(v any) bool {
				recoverFunc(v, fmt.Sprintf("unix: serve[%d]", clientID), false)
				return false
			})
			defer closeNoError(conn)
			serve(ctx, wg, newPdnsClient(ctx, fmt.Sprintf("%d,%s", clientID, conn.RemoteAddr()), conn, conn), &initialzed, nil)
			log.main().Tracef("unix: serve[%d] returned normally", clientID)
		}, nextClientID)
		nextClientID++
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

func httpListener(ctx context.Context, wg *WaitGroup, u *url.URL) {
	if u.Hostname() == "" {
		log.main().Panic("http: <listen-address> may not be empty")
	}
	if u.Port() == "" {
		log.main().Panic("http: <listen-port> may not be empty")
	}
	log.main().Tracef("http: creating server (%s)", u.Host)
	socket, err := net.Listen("tcp", u.Host)
	if err != nil {
		log.main("error", err, "addr", u.Host).Panicf("http: failed to create the TCP listening socket: %s", err)
	}
	defer closeNoError(socket)
	headerIsMT := func(r *http.Request, header string, mt string) bool {
		h := r.Header.Get(header)
		if h == "" {
			return false
		}
		mediatype, _, err := mime.ParseMediaType(h)
		if err != nil {
			log.main(h).Tracef("failed to parse media type: %s", err)
			return false
		}
		return mediatype == mt
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := fmt.Sprintf("%s #%04x", r.RemoteAddr, rand.Uint32())
		wg.Register(id)
		defer wg.Done(id)
		log.main("client", r.RemoteAddr, "method", r.Method, "header", r.Header, "url", r.URL.String()).Trace("http: new request")
		if r.Method != http.MethodPost {
			log.main(r.Method).Debug("http: non-POST method")
			http.Error(w, "only POST allowed", http.StatusMethodNotAllowed)
			return
		}
		if !headerIsMT(r, "Content-Type", "text/javascript") {
			log.main(r.Header.Get("Content-Type")).Debug("http: non-TJS content-type (header)")
			http.Error(w, "Content-Type must be text/javascript", http.StatusUnsupportedMediaType)
			return
		}
		if !headerIsMT(r, "Accept", "application/json") {
			log.main(r.Header.Get("Accept")).Debug("http: non-JSON accept header")
			http.Error(w, "Accept must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		cur := requestsCount.cur.Add(1)
		requestsCount.max.CompareAndSwap(cur-1, cur)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		serve(ctx, wg, newPdnsClient(ctx, id, r.Body, &httpWriter{w}), nil, nil)
		requestsCount.cur.Add(-1)
	})
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       10 * time.Second,
	}
	done := make(chan struct{})
	wg.Go("shutdown http", func(_ any) {
		defer close(done)
		<-ctx.Done()
		log.main().Debug("{shutdown http} shutting down")
		shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 7*time.Second)
		defer shutdownCancel()
		if err = server.Shutdown(shutdownCtx); err != nil {
			log.main(err).Errorf("{shutdown http} Shutdown() failed: %s; using Close()", err)
			if err = server.Close(); err != nil {
				log.main(err).Errorf("{shutdown http} Close() failed: %s", err)
			} else {
				log.main().Debugf("{shutdown http} Close() succeeded")
			}
		} else {
			log.main().Trace("{shutdown http} Shutdown() succeeded")
		}
	}, nil)
	log.main(socket.Addr()).Debugf("http: starting server (%s)", socket.Addr())
	status.serving = true
	err = server.Serve(socket)
	status.serving = false
	if !errors.Is(err, http.ErrServerClosed) {
		log.main(err).Panicf("http: Serve() failed: %v", err)
	}
	log.main().Trace("http: waiting for server to complete shutdown")
	<-done
}
