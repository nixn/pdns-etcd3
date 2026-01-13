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
	"fmt"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	clientv3yaml "go.etcd.io/etcd/client/v3/yaml"
	"golang.org/x/net/context"
)

var (
	cli             *clientv3.Client
	currentRevision int64
)

func setupClient() (logMessages []string, err error) {
	if *args.ConfigFile != "" {
		cfg, fileErr := clientv3yaml.NewConfig(*args.ConfigFile)
		if fileErr != nil {
			err = fmt.Errorf("failed to read config from file %q: %s", *args.ConfigFile, fileErr)
			return
		}
		cli, err = clientv3.New(*cfg)
		if err != nil {
			err = fmt.Errorf("failed to create client instance: %s", err)
			return
		}
		logMessages = append(logMessages, fmt.Sprintf("%s: %s", configFileParam, *args.ConfigFile))
		return
	}
	cfg := clientv3.Config{
		DialTimeout: *args.DialTimeout,
		Endpoints:   strings.Split(*args.Endpoints, `|`),
	}
	logMessages = append(logMessages,
		fmt.Sprintf("%s: %s", dialTimeoutParam, *args.DialTimeout),
		fmt.Sprintf("%s: %s", endpointsParam, *args.Endpoints),
	)
	cli, err = clientv3.New(cfg)
	if err != nil {
		err = fmt.Errorf("failed to create ETCD client instance: %s", err)
		return
	}
	connected = true
	logMessages = append(logMessages, fmt.Sprintf("%s: %v", endpointsParam, cfg.Endpoints))
	return
}

func closeClient() {
	_ = cli.Close()
}

type etcdItem struct {
	Key   string
	Value []byte
	Rev   int64
}

type getResponseType struct {
	Revision int64
	DataChan <-chan etcdItem
}

func getResponse(response *clientv3.GetResponse) *getResponseType {
	ch := make(chan etcdItem)
	go func() {
		for _, item := range response.Kvs {
			ch <- etcdItem{string(item.Key), item.Value, maxOf(item.CreateRevision, item.ModRevision)}
		}
		close(ch)
	}()
	return &getResponseType{response.Header.Revision, ch}
}

func get(key string, multi bool, revision *int64) (*getResponseType, error) {
	log.etcd("multi", multi, "rev", revision).Tracef("get %q", key)
	opts := []clientv3.OpOption(nil)
	if multi {
		opts = append(opts, clientv3.WithPrefix())
	}
	if revision != nil {
		opts = append(opts, clientv3.WithRev(*revision))
	}
	ctx, cancel := context.WithTimeout(context.Background(), *args.DialTimeout)
	defer cancel()
	since := time.Now()
	response, err := cli.Get(ctx, key, opts...)
	dur := time.Since(since)
	if err != nil {
		return nil, fmt.Errorf("[dur %s] %s", dur, err)
	}
	log.etcd("multi", multi, "dur", dur, "rev", revision, "#", response.Count, "more", response.More).Tracef("got %q", key)
	return getResponse(response), nil
}

func put(key string, value string, timeout time.Duration) (*clientv3.PutResponse, error) {
	opts := []clientv3.OpOption(nil)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return cli.Put(ctx, key, value, opts...)
}

func watchData(ctx context.Context) {
	watcher := clientv3.NewWatcher(cli)
	defer closeNoError(watcher)
	watchRetryInterval := 5 * time.Second // TODO make a program argument
WATCH:
	for {
		// fail fast
		select {
		case <-ctx.Done():
			break WATCH
		default:
		}
		log.etcd("rev", currentRevision).Tracef("creating watch")
		watchCtx := clientv3.WithRequireLeader(ctx)
		watchChan := watcher.Watch(watchCtx, *args.Prefix, clientv3.WithPrefix(), clientv3.WithRev(currentRevision+1))
	EVENTS:
		for {
			log.etcd().Trace("waiting for next event")
			watchResponse, ok := <-watchChan
			if !ok {
				log.etcd().Trace("watch channel closed")
				select {
				case <-ctx.Done():
					break WATCH
				default:
					break EVENTS
				}
			}
			if err := watchResponse.Err(); err != nil {
				log.etcd(watchResponse).Errorf("watch failed: %s", err)
			} else {
				n := len(watchResponse.Events)
				log.etcd("compact-rev", watchResponse.CompactRevision, "#events", n, "rev", watchResponse.Header.Revision).Debug("watch event")
				if n == 0 {
					log.etcd("rev", currentRevision).Tracef("stopping watch")
					break WATCH
				}
				for _, ev := range watchResponse.Events {
					handleEvent(ev) // TODO handle all events in a run and reload data afterwards
				}
				currentRevision = watchResponse.Header.Revision
			}
		}
		log.etcd().Debugf("retrying watch in %s", watchRetryInterval)
		interruptibleSleep(ctx, watchRetryInterval)
	}
}

func interruptibleSleep(ctx context.Context, dur time.Duration) {
	sleepCtx, cancel := context.WithTimeout(ctx, dur)
	defer cancel()
	<-sleepCtx.Done()
}
