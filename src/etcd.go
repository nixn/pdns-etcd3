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
	"strings"
	"time"

	"github.com/coreos/etcd/clientv3"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
)

var (
	cli *clientv3.Client
)

func setupClient() (logMessages []string, err error) {
	if len(*args.ConfigFile) > 0 {
		cli, err = clientv3.NewFromConfigFile(*args.ConfigFile)
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
	logMessages = append(logMessages, fmt.Sprintf("%s: %v", endpointsParam, cfg.Endpoints))
	return
}

func closeClient() {
	cli.Close()
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
	log.etcd().WithFields(logrus.Fields{"multi": multi, "rev": revision}).Tracef("get %q", key)
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
	log.etcd().WithFields(logrus.Fields{"multi": multi, "dur": dur, "rev": revision, "#": response.Count, "more": response.More}).Tracef("got %q", key)
	return getResponse(response), nil
}

func watchData(doneCtx context.Context, revision int64) {
	watcher := clientv3.NewWatcher(cli)
	defer watcher.Close()
WATCH:
	for {
		watchCtx := clientv3.WithRequireLeader(doneCtx)
		watchChan := watcher.Watch(watchCtx, *args.Prefix, clientv3.WithPrefix(), clientv3.WithRev(revision))
	SELECT:
		for {
			select {
			case <-doneCtx.Done():
				break WATCH
			case watchResponse, ok := <-watchChan:
				if ok {
					if watchResponse.Canceled {
						log.etcd().WithError(watchResponse.Err()).Error("watch canceled")
						break
					} else {
						log.etcd().WithFields(logrus.Fields{"compact-rev": watchResponse.CompactRevision, "#events": len(watchResponse.Events), "rev": watchResponse.Header.Revision}).Debug("watch event")
						for _, ev := range watchResponse.Events {
							handleEvent(ev)
						}
					}
				} else {
					log.etcd().WithError(watchResponse.Err()).Errorf("watch failed")
					break SELECT
				}
			}
		}
	}
}
