/* Copyright 2016 nix <https://github.com/nixn>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. */

package main

import (
	"fmt"
	"github.com/coreos/etcd/clientv3"
	"golang.org/x/net/context"
	"log"
	"strconv"
	"strings"
	"time"
)

var (
	cli     *clientv3.Client
	timeout = 2 * time.Second
)

func setConfigFileParameter(value string) error {
	client, err := clientv3.NewFromConfigFile(value)
	if err != nil {
		return fmt.Errorf("failed to create client instance: %s", err)
	}
	cli = client
	return nil
}

func setupClient(params map[string]interface{}) ([]string, error) {
	haveConfigFile, err := readParameter("config-file", params, setConfigFileParameter)
	if err != nil {
		return nil, err
	}
	if haveConfigFile {
		return []string{}, nil
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
					return nil, fmt.Errorf("Timeout may not be zero")
				}
			} else {
				return nil, fmt.Errorf("Failed to parse timeout value: %s", err)
			}
		} else {
			return nil, fmt.Errorf("parameters.timeout is not a string")
		}
	}
	logMessages := []string{fmt.Sprintf("timeout: %s", timeout)}
	// endpoints
	if endpoints, ok := params["endpoints"]; ok {
		if endpoints, ok := endpoints.(string); ok {
			endpoints := strings.Split(endpoints, "|")
			cfg.Endpoints = endpoints
			if client, err := clientv3.New(cfg); err == nil {
				cli = client
			} else {
				return nil, fmt.Errorf("Failed to parse endpoints: %s", err)
			}
		} else {
			return nil, fmt.Errorf("parameters.endpoints is not a string")
		}
	} else {
		cfg.Endpoints = []string{"[::1]:2379", "127.0.0.1:2379"}
		if client, err := clientv3.New(cfg); err == nil {
			cli = client
		} else {
			return nil, fmt.Errorf("Failed to create client: %s", err)
		}
	}
	return logMessages, nil
}

func closeClient() {
	cli.Close()
}

func get(key string, multi bool, revision *int64) (*clientv3.GetResponse, error) {
	log.Println("loading", key)
	opts := []clientv3.OpOption{}
	if multi {
		opts = append(opts, clientv3.WithPrefix())
	}
	if revision != nil {
		opts = append(opts, clientv3.WithRev(*revision))
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	since := time.Now()
	response, err := cli.Get(ctx, key, opts...)
	dur := time.Since(since)
	cancel()
	log.Println("loading", key, "dur:", dur)
	return response, err
}
