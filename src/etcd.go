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
	"slices"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
	clientv3yaml "go.etcd.io/etcd/client/v3/yaml"
	"golang.org/x/net/context"
)

type etcdClient struct {
	*clientv3.Client
	Connected       bool
	CurrentRevision int64
}

func (cli *etcdClient) Setup(args *programArgs) (logMessages []string, err error) {
	if *args.ConfigFile != "" {
		cfg, fileErr := clientv3yaml.NewConfig(*args.ConfigFile)
		if fileErr != nil {
			err = fmt.Errorf("failed to read config from file %q: %s", *args.ConfigFile, fileErr)
			return
		}
		cli.Client, err = clientv3.New(*cfg)
		if err != nil {
			err = fmt.Errorf("failed to create client instance: %s", err)
			return
		}
		logMessages = append(logMessages, fmt.Sprintf("%s: %s", configFileParam, *args.ConfigFile))
		return
	}
	cfg := clientv3.Config{
		DialTimeout:          *args.DialTimeout,
		DialKeepAliveTime:    *args.DialKeepAliveTime,
		DialKeepAliveTimeout: *args.DialKeepAliveTimeout,
		PermitWithoutStream:  *args.PermitWithoutStream,
		AutoSyncInterval:     *args.AutoSyncInterval,
		Endpoints:            strings.Split(*args.Endpoints, `|`),
	}
	logMessages = append(logMessages,
		fmt.Sprintf("%s: %s", dialTimeoutParam, *args.DialTimeout),
		fmt.Sprintf("%s: %s", dialKeepAliveTimeParam, *args.DialKeepAliveTime),
		fmt.Sprintf("%s: %s", dialKeepAliveTimeoutParam, *args.DialKeepAliveTimeout),
		fmt.Sprintf("%s: %s", autoSyncIntervalParam, *args.AutoSyncInterval),
		fmt.Sprintf("%s: %v", permitWithoutStreamParam, *args.PermitWithoutStream),
		fmt.Sprintf("%s: %s", endpointsParam, *args.Endpoints),
	)
	cli.Client, err = clientv3.New(cfg)
	if err != nil {
		err = fmt.Errorf("failed to create ETCD client instance: %s", err)
		return
	}
	cli.Connected = true
	logMessages = append(logMessages, fmt.Sprintf("%s: %v", endpointsParam, cfg.Endpoints))
	return
}

func (cli *etcdClient) Close() {
	if cli.Client != nil {
		_ = cli.Client.Close()
	}
}

type etcdItem struct {
	Key        string
	Value      []byte
	CRev, MRev int64
}

func (ei etcdItem) Rev() int64 {
	return max(ei.CRev, ei.MRev)
}

type getResponseType struct {
	Revision int64
	DataChan <-chan etcdItem
}

func getResponse(response *clientv3.GetResponse) *getResponseType {
	ch := make(chan etcdItem)
	go func() {
		for _, item := range response.Kvs {
			ch <- etcdItem{string(item.Key), item.Value, item.CreateRevision, item.ModRevision}
		}
		close(ch)
	}()
	return &getResponseType{response.Header.Revision, ch}
}

func (cli *etcdClient) Get(key string, multi bool, revision *int64, timeout time.Duration) (*getResponseType, error) {
	log.etcd("multi", multi, "rev", revision).Tracef("get %q", key)
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
	response, err := cli.Client.Get(ctx, key, opts...)
	dur := time.Since(since)
	if err != nil {
		return nil, fmt.Errorf("[dur %s] %s", dur, err)
	}
	log.etcd("multi", multi, "dur", dur, "rev", revision, "#", response.Count, "more", response.More).Tracef("got %q", key)
	return getResponse(response), nil
}

func (cli *etcdClient) Put(key string, value string, timeout time.Duration) (*clientv3.PutResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return cli.Client.Put(ctx, key, value)
}

func (cli *etcdClient) Del(key string, multi bool, timeout time.Duration) (*clientv3.DeleteResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if multi {
		return cli.Client.Delete(ctx, key, clientv3.WithPrefix())
	} else {
		return cli.Client.Delete(ctx, key)
	}
}

func delOp(key string, multi bool) clientv3.Op {
	if multi {
		return clientv3.OpDelete(key, clientv3.WithPrefix())
	} else {
		return clientv3.OpDelete(key)
	}
}

func putOp(key, value string) clientv3.Op {
	return clientv3.OpPut(key, value)
}

func (cli *etcdClient) Txn(timeout time.Duration, ops ...clientv3.Op) (*clientv3.TxnResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	txn := cli.Client.Txn(ctx)
	txn.Then(ops...)
	return txn.Commit()
}

func (cli *etcdClient) WatchData(ctx context.Context, prefix string) {
	watcher := clientv3.NewWatcher(cli.Client)
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
		log.etcd("currRev", cli.CurrentRevision).Tracef("creating watch")
		watchCtx := clientv3.WithRequireLeader(ctx)
		watchChan := watcher.Watch(watchCtx, prefix, clientv3.WithPrefix(), clientv3.WithRev(cli.CurrentRevision+1))
	EVENTS:
		for {
			log.etcd("currRev", cli.CurrentRevision).Trace("waiting for next event")
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
					log.etcd("currRev", cli.CurrentRevision).Tracef("stopping watch")
					break WATCH
				}
				handleEvents(watchResponse.Header.Revision, watchResponse.Events)
				cli.CurrentRevision = watchResponse.Header.Revision
			}
		}
		log.etcd().Debugf("retrying watch in %s", watchRetryInterval)
		_ = Sleep(ctx, watchRetryInterval)
	}
}

type Transaction struct {
	LockKey string
	Prefix  string
	session *concurrency.Session
	mutex   *concurrency.Mutex
	items   map[string]etcdItem
	maxRev  int64
	puts    map[string]string
	dels    map[string]struct{}
}

func NewTransaction(lockKey, prefix string) *Transaction {
	return &Transaction{
		LockKey: lockKey,
		Prefix:  prefix,
		items:   map[string]etcdItem{},
		puts:    map[string]string{},
		dels:    map[string]struct{}{},
	}
}

func (tx *Transaction) Start(timeout time.Duration) (id clientv3.LeaseID, err error) {
	if tx.session, err = concurrency.NewSession(cli.Client, concurrency.WithTTL(int(timeout/time.Second))); err != nil {
		return 0, fmt.Errorf("failed to create session: %s", err)
	}
	tx.mutex = concurrency.NewMutex(tx.session, tx.LockKey)
	{
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err = tx.mutex.Lock(ctx); err != nil {
			_ = tx.session.Close()
			return 0, fmt.Errorf("failed to acquire lock: %s", err)
		}
	}
	response, err := cli.Get(tx.Prefix, true, nil, timeout)
	if err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		_ = tx.mutex.Unlock(ctx)
		_ = tx.session.Close()
		return 0, fmt.Errorf("failed to get values: %s", err)
	}
	// FIXME don't process items in sub-zones
	for item := range response.DataChan {
		tx.items[strings.TrimPrefix(item.Key, tx.Prefix)] = item
		if !strings.HasPrefix(item.Key, tx.LockKey) {
			tx.maxRev = max(tx.maxRev, item.Rev())
		}
	}
	return tx.session.Lease(), nil
}

func (tx *Transaction) Put(key, value string) {
	delete(tx.dels, key)
	if item, ok := tx.items[key]; ok && slices.Equal(item.Value, []byte(value)) {
		delete(tx.puts, key)
	} else {
		tx.puts[key] = value
	}
}

func (tx *Transaction) PutsCount() int {
	return len(tx.puts)
}

func (tx *Transaction) Del(key string, multi bool) {
	if multi {
		for _, k := range Keys(tx.items) {
			if strings.HasPrefix(k, key) {
				tx.Del(k, false)
			}
		}
		for _, k := range Keys(tx.puts) {
			if strings.HasPrefix(k, key) {
				tx.Del(k, false)
			}
		}
	} else {
		delete(tx.puts, key)
		if _, ok := tx.items[key]; ok {
			tx.dels[key] = struct{}{}
		} else {
			delete(tx.dels, key)
		}
	}
}

func (tx *Transaction) DelsCount() int {
	return len(tx.dels)
}

func (tx *Transaction) OpsCount() int {
	return tx.PutsCount() + tx.DelsCount()
}

func (tx *Transaction) Commit(timeout time.Duration) (int64, error) {
	cmps := make([]clientv3.Cmp, 0, len(tx.items))
	for _, item := range tx.items {
		cmps = append(cmps, clientv3.Compare(clientv3.CreateRevision(item.Key), "=", item.CRev), clientv3.Compare(clientv3.ModRevision(item.Key), "=", item.MRev))
	}
	ops := make([]clientv3.Op, 0, len(tx.puts)+len(tx.dels))
	for k, v := range tx.puts {
		ops = append(ops, clientv3.OpPut(tx.Prefix+k, v))
	}
	for k := range tx.dels {
		ops = append(ops, clientv3.OpDelete(tx.Prefix+k))
	}
	rev, err := func() (int64, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		defer func() {
			_ = tx.mutex.Unlock(ctx)
			_ = tx.session.Close()
		}()
		if response, err := cli.Client.Txn(ctx).If(cmps...).Then(ops...).Commit(); err != nil {
			return 0, fmt.Errorf("commit failed: %s", err)
		} else if !response.Succeeded {
			return 0, fmt.Errorf("commit not succeeded")
		} else {
			// unfortunately we can't get the revision of the tx.mutex.Unlock() delete op, which would be the better one
			return response.Header.Revision, nil
		}
	}()
	<-tx.session.Done()
	return rev, err
}

func (tx *Transaction) Abort(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = tx.mutex.Unlock(ctx)
	_ = tx.session.Close()
}
