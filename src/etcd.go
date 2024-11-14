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
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/etcd/clientv3"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
)

var (
	cli     *clientv3.Client
	timeout = defaultDialTimeout
)

func setConfigFileParameter(value string) error {
	client, err := clientv3.NewFromConfigFile(value)
	if err != nil {
		return fmt.Errorf("failed to create client instance: %s", err)
	}
	cli = client
	return nil
}

func setupClient(params objectType) ([]string, error) {
	haveConfigFile, err := readParameter("config-file", params, setConfigFileParameter)
	if err != nil {
		return nil, err
	}
	if haveConfigFile {
		return []string{fmt.Sprintf("config-file: %s", params["config-file"])}, nil
	}
	cfg := clientv3.Config{DialTimeout: timeout}
	// timeout
	if tmo, ok := params["timeout"]; ok {
		if tmo, ok := tmo.(string); ok {
			if tmo, err := strconv.ParseUint(tmo, 10, 32); err == nil {
				if tmo > 0 {
					timeout = time.Duration(tmo) * time.Millisecond
					cfg.DialTimeout = timeout
				} else {
					return nil, fmt.Errorf("timeout may not be zero")
				}
			} else {
				return nil, fmt.Errorf("failed to parse timeout value: %s", err)
			}
		} else {
			return nil, fmt.Errorf("timeout is not a string")
		}
	}
	logMessages := []string{fmt.Sprintf("timeout: %s", timeout)}
	// endpoints
	if endpoints, ok := params["endpoints"]; ok {
		if endpoints, ok := endpoints.(string); ok {
			endpoints := strings.Split(endpoints, "|")
			cfg.Endpoints = endpoints
			client, err := clientv3.New(cfg)
			if err != nil {
				return nil, fmt.Errorf("failed to parse endpoints: %s", err)
			}
			cli = client
		} else {
			return nil, fmt.Errorf("parameters.endpoints is not a string")
		}
	} else {
		cfg.Endpoints = []string{defaultEndpointIPv6, defaultEndpointIPv4}
		client, err := clientv3.New(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create client: %s", err)
		}
		cli = client
	}
	logMessages = append(logMessages, fmt.Sprintf("endpoints: %v", cfg.Endpoints))
	return logMessages, nil
}

func closeClient() {
	cli.Close()
}

type keyMultiPair struct {
	key   string
	multi bool
}

func (kmp *keyMultiPair) String() string {
	s := kmp.key
	if kmp.multi {
		s += "…"
	}
	return s
}

type keyValuePair struct {
	Key   string
	Value []byte
}

type getResponseType struct {
	Revision int64
	DataChan <-chan keyValuePair
}

func getResponse(response *clientv3.GetResponse) *getResponseType {
	ch := make(chan keyValuePair)
	go func() {
		for _, item := range response.Kvs {
			ch <- keyValuePair{string(item.Key), item.Value}
		}
		close(ch)
	}()
	return &getResponseType{response.Header.Revision, ch}
}

func txnResponse(response *clientv3.TxnResponse) *getResponseType {
	ch := make(chan keyValuePair)
	go func() {
		for _, txnOp := range response.Responses {
			for _, item := range txnOp.GetResponseRange().Kvs {
				ch <- keyValuePair{string(item.Key), item.Value}
			}
		}
		close(ch)
	}()
	return &getResponseType{response.Header.Revision, ch}
}

func get(key string, multi bool, revision *int64) (*getResponseType, error) {
	log.etcd.WithFields(logrus.Fields{"multi": multi, "rev": revision}).Tracef("get %q", key)
	opts := []clientv3.OpOption(nil)
	if multi {
		opts = append(opts, clientv3.WithPrefix())
	}
	if revision != nil {
		opts = append(opts, clientv3.WithRev(*revision))
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	since := time.Now()
	response, err := cli.Get(ctx, key, opts...)
	dur := time.Since(since)
	if err != nil {
		return nil, fmt.Errorf("[dur %s] %s", dur, err)
	}
	log.etcd.WithFields(logrus.Fields{"multi": multi, "dur": dur, "rev": revision, "#": response.Count, "more": response.More}).Tracef("got %q", key)
	return getResponse(response), nil
}

func getall(keys []keyMultiPair, revision *int64) (*getResponseType, error) {
	ops := []clientv3.Op(nil)
	for _, kmp := range keys {
		opts := []clientv3.OpOption(nil)
		if kmp.multi {
			opts = append(opts, clientv3.WithPrefix())
		}
		if revision != nil {
			opts = append(opts, clientv3.WithRev(*revision))
		}
		ops = append(ops, clientv3.OpGet(kmp.key, opts...))
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	since := time.Now()
	txn := cli.Txn(ctx)
	txn.Then(ops...)
	response, err := txn.Commit()
	dur := time.Since(since)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("[dur %s] %s", dur, err)
	}
	if !response.Succeeded {
		return nil, fmt.Errorf("[dur %s] txn not succeeded", dur)
	}
	counts := []int64(nil)
	for _, response := range response.Responses {
		counts = append(counts, response.GetResponseRange().Count)
	}
	log.etcd.Printf("get %v @%d, dur: %s, # %v", keys, revision, dur, counts)
	return txnResponse(response), nil
}

func startWatchData(doneCtx context.Context, revision int64) <-chan *clientv3.Event {
	ch := make(chan *clientv3.Event)
	go func() {
		defer close(ch)
		watcher := clientv3.NewWatcher(cli)
		defer watcher.Close()
		for {
			watchCtx := clientv3.WithRequireLeader(doneCtx)
			watchChan := watcher.Watch(watchCtx, prefix, clientv3.WithPrefix(), clientv3.WithRev(revision))
			for {
				select {
				case <-doneCtx.Done():
					return
				case watchResponse, ok := (<-watchChan):
					if ok {
						if watchResponse.Canceled {
							log.etcd.WithError(watchResponse.Err()).Error("watch canceled")
							break
						} else {
							log.etcd.WithFields(logrus.Fields{"compact-rev": watchResponse.CompactRevision, "#events": len(watchResponse.Events), "rev": watchResponse.Header.Revision}).Debug("watch event")
							for _, ev := range watchResponse.Events {
								ch <- ev
							}
						}
					} else {
						log.etcd.WithError(watchResponse.Err()).Errorf("watch failed")
						break
					}
				}
			}
		}
	}()
	return ch
}
