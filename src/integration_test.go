//go:build integration

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
	"maps"
	"net"
	"net/url"
	"os"
	"runtime/debug"
	"sync"
	"testing"
	"time"

	"github.com/docker/go-connections/nat"
	"github.com/miekg/dns"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func newEntry(t *testing.T, key, value string) int64 {
	t.Helper()
	if resp, err := put(key, value, 10*time.Second); err != nil {
		t.Fatalf("failed to put %q (%q): %s", key, value, err)
		return 0
	} else {
		return resp.Header.Revision
	}
}

func TestRequests(t *testing.T) {
	defer recoverPanicsT(t)
	// start ETCD
	etcd, err := startETCD(t)
	fatalOnErr(t, "start ETCD container", err)
	defer etcd.Terminate()
	t.Logf("ETCD endpoint: %s", etcd.Endpoint)
	sleepT(t, 1*time.Second)
	// start pdns-etcd3 (main function)
	inR, inW, _ := os.Pipe()
	defer func() {
		t.Log("closing input stream to pdns-etcd3")
		closeNoError(inW)
	}()
	outR, outW, _ := os.Pipe()
	defer closeNoError(outR) // this should be done automatically by pdns-etcd3, but just in case
	config := ""
	timeout, _ := time.ParseDuration("2s")
	prefix := ""
	args = programArgs{
		ConfigFile:  &config,
		Endpoints:   &etcd.Endpoint,
		DialTimeout: &timeout,
		Prefix:      &prefix,
	}
	t.Logf("starting pdns-etcd3.serve() with ETCD endpoint %s", etcd.Endpoint)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wg := new(sync.WaitGroup)
	go pipe(wg, ctx, inR, outW)
	pe3 := newComm[any](ctx, outR, inW)
	action := func(request pdnsRequest) (any, error) {
		t.Logf("request: %s", val2str(request))
		_ = pe3.write(request)
		response, err := pe3.read()
		t.Logf("response: %s, err: %v", val2str(*response), err)
		return *response, err
	}
	testPrefix := "/DNS/"
	request := pdnsRequest{"initialize", objectType[any]{"pdns-version": "3", "prefix": testPrefix, "log-trace": "main+pdns+etcd", "log-debug": "data"}}
	var expectedResponse any
	expectedResponse = map[string]any{"result": true, "log": Ignore{}}
	if !check(t, "initialize", action, request, ve[any]{v: expectedResponse}) {
		t.Fatalf("failed to initialize")
	}
	if prefix != testPrefix {
		t.Fatalf("prefix mismatch after initialize: expected %q, got %q", testPrefix, prefix)
	}
	err = waitFor(t, "populated", func() bool { return populated }, 100*time.Millisecond, 30*time.Second)
	fatalOnErr(t, "wait for populated", err)
	sleepT(t, 1*time.Second)
	var rev1 int64
	for _, entry := range []struct {
		key, value string
	}{
		{"-defaults-", `{"ttl": "1h"}`},
		{"-defaults-/SRV", `{"priority": 0, "weight": 0}`},
		{"-defaults-/SOA", `{"refresh": "1h", "retry": "30m", "expire": 604800, "neg-ttl": "10m"}`},
		{"net.example/SOA", `{"primary": "ns1", "mail": "horst.master"}`},
		{"net.example/NS#first", `{"hostname": "ns1"}`},
		{"net.example/NS#second", `="ns2"`},
		{"net.example/-options-/A", `{"ip-prefix": [192, 0, 2]}`},
		{"net.example/-options-/AAAA", `{"ip-prefix": "20010db8"}`},
		{"net.example/ns1/A", `=2`},
		{"net.example/ns1/AAAA", `="02"`},
		{"net.example/ns2/A", `{"ip": "192.0.2.3"}`},
		{"net.example/ns2/AAAA", `{"ip": [3]}`},
		{"net.example/-defaults-/MX", `{"ttl": "2h"}`},
		{"net.example/MX#1", `{"priority": 10, "target": "mail"}`},
		{"net.example/mail/A", `{"ip": [192,0,2,10]}`},
		{"net.example/mail/AAAA", `2001:0db8::10`},
		{"net.example/TXT#spf", `v=spf1 ip4:192.0.2.0/24 ip6:2001:db8::/32 -all`},
		{"net.example/TXT#{}", `{"text":"{text which begins with a curly brace (the id too)}"}`},
		{"net.example/versioned/TXT@1234.56", `@1234.56`},
		{"net.example/versioned/TXT@0.1", `@0.1`},
		{fmt.Sprintf("net.example/versioned/TXT@%s", dataVersion), fmt.Sprintf(`@%s`, dataVersion)},
		{"net.example/kerberos1/A#1", `192.0.2.15`},
		{"net.example/kerberos1/AAAA#1", `{"ip": "2001:0db8::15"}`},
		{"net.example/kerberos2/A#", `192.0.2.25`},
		{"net.example/kerberos2/AAAA#", `2001:db8::25`},
		{"net.example/_tcp/_kerberos/-defaults-/SRV", `{"port": 88}`},
		{"net.example/_tcp/_kerberos/SRV#1", `{"target": "kerberos1"}`},
		{"net.example/_tcp/_kerberos/SRV#2", `="kerberos2"`},
		{"net.example/kerberos-master/CNAME", `{"target": "kerberos1"}`},
		{"net.example/mail/-defaults-/HINFO", `{"ttl": "2h"}`},
		{"net.example/mail/HINFO", `"amd64" "Linux"`},
		{"net.example/mail/HINFO#not-object-supported", `{"platform": "arm", "os": "Raspbian"}`},
		{"net.example/TYPE123", `\# 0`},
		{"net.example.case/TXT", `PR #1`},
		// TODO duplicate records (different but equivalent keys)
	} {
		rev1 = newEntry(t, prefix+entry.key, entry.value)
	}
	var rev2 int64
	for _, entry := range []struct {
		key, value string
	}{
		{"arpa.in-addr/192.0.2/-options-", `{"zone-append-domain": "example.net."}`},
		{"arpa.in-addr/192.0.2/SOA", `{"primary": "ns1", "mail": "horst.master"}`},
		{"arpa.in-addr/192.0.2/NS#a", `{"hostname": "ns1"}`},
		{"arpa.in-addr/192.0.2/NS#b", `ns2.example.net.`},
		{"arpa.in-addr/192.0.2/2/PTR", `="ns1"`},
		{"arpa.in-addr/192.0.2/3/PTR", `="ns2"`},
	} {
		rev2 = newEntry(t, prefix+entry.key, entry.value)
	}
	err = waitFor(t, "data loaded", func() bool { return currentRevision == rev2 }, 100*time.Millisecond, 10*time.Second)
	fatalOnErr(t, "wait for data loaded", err)
	request = pdnsRequest{"gibberish", nil}
	expectedResponse = map[string]any{"result": false, "log": Ignore{}}
	check(t, "gibberish", action, request, ve[any]{v: expectedResponse})
	request = pdnsRequest{"getAllDomainMetadata", objectType[any]{"name": "example.com"}}
	expectedResponse = map[string]any{"result": map[string]any{}}
	check(t, "getAllDomainMetadata", action, request, ve[any]{v: expectedResponse})
	request = pdnsRequest{"getAllDomains", objectType[any]{"include_disabled": true}}
	expectedResponse = map[string]any{"result": SliceContains{false, true, []any{
		map[string]any{"zone": "example.net.", "serial": float64(rev1)},
		map[string]any{"zone": "2.0.192.in-addr.arpa.", "serial": float64(rev2)},
	}}}
	check(t, "getAllDomains", action, request, ve[any]{v: expectedResponse})
	t.Run("lookup", func(t *testing.T) {
		for _, spec := range []struct {
			parameters objectType[any]
			result     any
		}{
			{objectType[any]{"qname": "example.net", "qtype": "SOA"}, []any{
				map[string]any{"qname": "example.net.", "qtype": "SOA", "content": fmt.Sprintf(`ns1.example.net. horst\.master.example.net. %d 3600 1800 604800 600`, rev1), "ttl": float64(3600), "auth": true},
			}},
			{objectType[any]{"qname": "example.net", "qtype": "NS"}, SliceContains{false, true, []any{
				map[string]any{"qname": "example.net.", "qtype": "NS", "content": "ns1.example.net.", "ttl": float64(3600), "auth": true},
				map[string]any{"qname": "example.net.", "qtype": "NS", "content": "ns2.example.net.", "ttl": float64(3600), "auth": true},
			}}},
			{objectType[any]{"qname": "ns1.example.net", "qtype": "A"}, []any{
				map[string]any{"qname": "ns1.example.net.", "qtype": "A", "content": "192.0.2.2", "ttl": float64(3600), "auth": true},
			}},
			{objectType[any]{"qname": "ns1.example.net", "qtype": "AAAA"}, []any{
				map[string]any{"qname": "ns1.example.net.", "qtype": "AAAA", "content": "2001:db8::2", "ttl": float64(3600), "auth": true},
			}},
			{objectType[any]{"qname": "ns1.example.net", "qtype": "ANY"}, SliceContains{false, true, []any{
				map[string]any{"qname": "ns1.example.net.", "qtype": "A", "content": "192.0.2.2", "ttl": float64(3600), "auth": true},
				map[string]any{"qname": "ns1.example.net.", "qtype": "AAAA", "content": "2001:db8::2", "ttl": float64(3600), "auth": true},
			}}},
			{objectType[any]{"qname": "ns2.example.net", "qtype": "A"}, []any{
				map[string]any{"qname": "ns2.example.net.", "qtype": "A", "content": "192.0.2.3", "ttl": float64(3600), "auth": true},
			}},
			{objectType[any]{"qname": "ns2.example.net", "qtype": "AAAA"}, []any{
				map[string]any{"qname": "ns2.example.net.", "qtype": "AAAA", "content": "2001:db8::3", "ttl": float64(3600), "auth": true},
			}},
			{objectType[any]{"qname": "example.net", "qtype": "MX"}, []any{
				map[string]any{"qname": "example.net.", "qtype": "MX", "content": "mail.example.net.", "priority": float64(10), "ttl": float64(7200), "auth": true},
			}},
			{objectType[any]{"qname": "mail.example.net", "qtype": "A"}, []any{
				map[string]any{"qname": "mail.example.net.", "qtype": "A", "content": "192.0.2.10", "ttl": float64(3600), "auth": true},
			}},
			{objectType[any]{"qname": "mail.example.net", "qtype": "AAAA"}, []any{
				map[string]any{"qname": "mail.example.net.", "qtype": "AAAA", "content": "2001:0db8::10", "ttl": float64(3600), "auth": true},
			}},
			{objectType[any]{"qname": "example.net", "qtype": "TXT"}, SliceContains{false, true, []any{
				map[string]any{"qname": "example.net.", "qtype": "TXT", "content": "v=spf1 ip4:192.0.2.0/24 ip6:2001:db8::/32 -all", "ttl": float64(3600), "auth": true},
				map[string]any{"qname": "example.net.", "qtype": "TXT", "content": "{text which begins with a curly brace (the id too)}", "ttl": float64(3600), "auth": true},
			}}},
			{objectType[any]{"qname": "versioned.example.net", "qtype": "TXT"}, []any{
				map[string]any{"qname": "versioned.example.net.", "qtype": "TXT", "content": fmt.Sprintf("@%s", dataVersion), "ttl": float64(3600), "auth": true},
			}},
			{objectType[any]{"qname": "kerberos1.example.net", "qtype": "AAAA"}, []any{
				map[string]any{"qname": "kerberos1.example.net.", "qtype": "AAAA", "content": "2001:db8::15", "ttl": float64(3600), "auth": true},
			}},
			{objectType[any]{"qname": "_kerberos._tcp.example.net", "qtype": "SRV"}, SliceContains{false, true, []any{
				map[string]any{"qname": "_kerberos._tcp.example.net.", "qtype": "SRV", "content": "0 88 kerberos1.example.net.", "ttl": float64(3600), "auth": true, "priority": float64(0)},
				map[string]any{"qname": "_kerberos._tcp.example.net.", "qtype": "SRV", "content": "0 88 kerberos2.example.net.", "ttl": float64(3600), "auth": true, "priority": float64(0)},
			}}},
			{objectType[any]{"qname": "kerberos-master.example.net", "qtype": "CNAME"}, []any{
				map[string]any{"qname": "kerberos-master.example.net.", "qtype": "CNAME", "content": "kerberos1.example.net.", "ttl": float64(3600), "auth": true},
			}},
			{objectType[any]{"qname": "mail.example.net", "qtype": "HINFO"}, []any{
				map[string]any{"qname": "mail.example.net.", "qtype": "HINFO", "content": `"amd64" "Linux"`, "ttl": float64(7200), "auth": true},
			}},
			{objectType[any]{"qname": "example.net", "qtype": "TYPE123"}, []any{
				map[string]any{"qname": "example.net.", "qtype": "TYPE123", "content": `\# 0`, "ttl": float64(3600), "auth": true},
			}},
			{objectType[any]{"qname": "gibberish.example.net", "qtype": "ANY"}, false},
			{objectType[any]{"qname": "2.0.192.in-addr.arpa", "qtype": "SOA"}, []any{
				map[string]any{"qname": "2.0.192.in-addr.arpa.", "qtype": "SOA", "content": fmt.Sprintf(`ns1.example.net. horst\.master.example.net. %d 3600 1800 604800 600`, rev2), "ttl": float64(3600), "auth": true},
			}},
			{objectType[any]{"qname": "2.0.192.in-addr.arpa", "qtype": "NS"}, SliceContains{false, true, []any{
				map[string]any{"qname": "2.0.192.in-addr.arpa.", "qtype": "NS", "content": "ns1.example.net.", "ttl": float64(3600), "auth": true},
				map[string]any{"qname": "2.0.192.in-addr.arpa.", "qtype": "NS", "content": "ns2.example.net.", "ttl": float64(3600), "auth": true},
			}}},
			{objectType[any]{"qname": "2.2.0.192.in-addr.arpa", "qtype": "PTR"}, []any{
				map[string]any{"qname": "2.2.0.192.in-addr.arpa.", "qtype": "PTR", "content": "ns1.example.net.", "ttl": float64(3600), "auth": true},
			}},
			{objectType[any]{"qname": "CaSe.eXample.Net", "qtype": "TXT"}, []any{
				map[string]any{"qname": "CaSe.eXample.Net.", "qtype": "TXT", "content": "PR #1", "ttl": float64(3600), "auth": true},
			}},
		} {
			check[pdnsRequest, any](t, val2str(spec.parameters), action, pdnsRequest{"lookup", spec.parameters}, ve[any]{v: map[string]any{"result": spec.result}})
		}
	})
	t.Log("finished")
}

type CtLogger struct {
	t    *testing.T
	name string
}

func (ctl CtLogger) Accept(log testcontainers.Log) {
	ctl.t.Logf("%s[%s]: %s", ctl.name, log.LogType, log.Content)
}

type ctInfo struct {
	Container testcontainers.Container
	Terminate func()
	Endpoint  string
}

func startContainer(t *testing.T, cr testcontainers.ContainerRequest, endpoint nat.Port) (*ctInfo, error) {
	t.Helper()
	ctx := context.Background()
	ct, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: cr,
		Started:          true,
	})
	if err != nil {
		return nil, err
	}
	ctInfo := &ctInfo{
		Container: ct,
		Terminate: func() {
			if err := ct.Terminate(ctx); err != nil {
				t.Errorf("failed to terminate container: %s", err)
			}
		},
	}
	if endpoint != "" {
		ctInfo.Endpoint, err = ct.PortEndpoint(ctx, endpoint, "")
		if err != nil {
			ctInfo.Terminate()
			return nil, fmt.Errorf("failed to get endpoint: %s", err)
		}
	}
	return ctInfo, nil
}

func startETCD(t *testing.T) (*ctInfo, error) {
	t.Helper()
	return startContainer(t, testcontainers.ContainerRequest{
		Image:          "quay.io/coreos/etcd:v3.5.26",
		ExposedPorts:   []string{"2379"},
		LogConsumerCfg: &testcontainers.LogConsumerConfig{Consumers: []testcontainers.LogConsumer{CtLogger{t, "ETCD"}}},
		Cmd: []string{
			"etcd",
			"--data-dir=/data",
			"--name=etcd",
			"--initial-advertise-peer-urls=http://etcd:2380",
			"--listen-peer-urls=http://0.0.0.0:2380",
			"--advertise-client-urls=http://etcd:2379",
			"--listen-client-urls=http://0.0.0.0:2379",
			"--initial-cluster=etcd=http://etcd:2380",
			"--auto-compaction-retention=1h",
		},
		WaitingFor: wait.ForLog("ready to serve client requests"),
	}, "2379")
}

type pe3Info struct {
	Terminate   func()
	HttpAddress *url.URL
	Prefix      string
}

func startPE3(t *testing.T, etcdEndpoint string, prefix string) pe3Info {
	t.Helper()
	httpAddress, _ := url.Parse("http://0.0.0.0:8053") // the port is fixed, it is set in pdns.conf too
	doneCtx, done := context.WithCancel(context.Background())
	osSignals := make(chan os.Signal, 1)
	go func() {
		defer done()
		main(VersionType{IsDevelopment: true}, getGitVersion(t), []string{"-standalone=" + httpAddress.String(), "-endpoints=" + etcdEndpoint, "-prefix=" + prefix, "-log-trace=main+pdns+etcd", "-log-debug=data"}, osSignals)
		t.Logf("pe3 finished")
	}()
	return pe3Info{
		func() {
			t.Logf("sending os.Interrupt to pe3")
			osSignals <- os.Interrupt
			<-doneCtx.Done()
			t.Logf("pe3 context done")
		},
		httpAddress,
		prefix,
	}
}

func getGitVersion(t *testing.T) string {
	t.Helper()
	v := "???"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				v = setting.Value
			case "vcs.modified":
				if setting.Value == "true" {
					v += "*"
				}
			}
		}
	}
	return v
}

func startPDNS(t *testing.T) (*ctInfo, error) {
	t.Helper()
	return startContainer(t, testcontainers.ContainerRequest{
		Image:          "powerdns/pdns-auth-49",
		NetworkMode:    "host",
		LogConsumerCfg: &testcontainers.LogConsumerConfig{Consumers: []testcontainers.LogConsumer{CtLogger{t, "PDNS49"}}},
		Files:          []testcontainers.ContainerFile{{HostFilePath: "../testdata/pdns.conf", ContainerFilePath: "/etc/powerdns/pdns.conf", FileMode: 0o555}},
		WaitingFor:     wait.ForLog("ready to distribute questions"),
	}, "5353/tcp")
}

func TestWithPDNS(t *testing.T) {
	defer recoverPanicsT(t)
	// ETCD
	etcd, err := startETCD(t)
	fatalOnErr(t, "start ETCD container", err)
	defer etcd.Terminate()
	t.Logf("ETCD endpoint (2379): %s", etcd.Endpoint)
	// PDNS-ETCD3
	sleepT(t, 1*time.Second)
	pe3 := startPE3(t, etcd.Endpoint, "")
	defer pe3.Terminate()
	t.Logf("PDNS-ETCD3 endpoint: %s", pe3.HttpAddress)
	err = waitFor(t, "PE3 ready", func() bool { return serving }, 100*time.Millisecond, 30*time.Second)
	fatalOnErr(t, "wait for PE3 ready", err)
	sleepT(t, 1*time.Second)
	// fill data
	var rev1 int64
	for _, entry := range []struct {
		key, value string
	}{
		{"-defaults-", `{"ttl": "1h"}`},
		{"-defaults-/SRV", `{"priority": 0, "weight": 0}`},
		{"-defaults-/SOA", `{"refresh": "1h", "retry": "30m", "expire": 604800, "neg-ttl": "10m"}`},
		{"net.example/SOA", `{"primary": "ns1", "mail": "horst.master"}`},
		{"net.example/NS#first", `{"hostname": "ns1"}`},
		{"net.example/NS#second", `="ns2"`},
		{"net.example/-options-/A", `{"ip-prefix": [192, 0, 2]}`},
		{"net.example/-options-/AAAA", `{"ip-prefix": "20010db8"}`},
		{"net.example/ns1/A", `=2`},
		{"net.example/ns1/AAAA", `="02"`},
		{"net.example/ns2/A", `{"ip": "192.0.2.3"}`},
		{"net.example/ns2/AAAA", `{"ip": [3]}`},
		{"net.example/-defaults-/MX", `{"ttl": "2h"}`},
		{"net.example/MX#1", `{"priority": 10, "target": "mail"}`},
		{"net.example/mail/A", `{"ip": [192,0,2,10]}`},
		{"net.example/mail/AAAA", `2001:0db8::10`},
		{"net.example/TXT#spf", `v=spf1 ip4:192.0.2.0/24 ip6:2001:db8::/32 -all`},
		{"net.example/TXT#{}", `{"text":"{text which begins with a curly brace (the id too)}"}`},
		{"net.example/versioned/TXT@1234.56", `@1234.56`},
		{"net.example/versioned/TXT@0.1", `@0.1`},
		{fmt.Sprintf("net.example/versioned/TXT@%s", dataVersion), fmt.Sprintf(`@%s`, dataVersion)},
		{"net.example/kerberos1/A#1", `192.0.2.15`},
		{"net.example/kerberos1/AAAA#1", `{"ip": "2001:0db8::15"}`},
		{"net.example/kerberos2/A#", `192.0.2.25`},
		{"net.example/kerberos2/AAAA#", `2001:db8::25`},
		{"net.example/_tcp/_kerberos/-defaults-/SRV", `{"port": 88}`},
		{"net.example/_tcp/_kerberos/SRV#1", `{"target": "kerberos1"}`},
		{"net.example/_tcp/_kerberos/SRV#2", `="kerberos2"`},
		{"net.example/kerberos-master/CNAME", `{"target": "kerberos1"}`},
		{"net.example/mail/-defaults-/HINFO", `{"ttl": "2h"}`},
		{"net.example/mail/HINFO", `"amd64" "Linux"`},
		{"net.example/mail/HINFO#not-object-supported", `{"platform": "arm", "os": "Raspbian"}`},
		{"net.example/TYPE123", `\# 0`},
		{"net.example/TYPE237", `\# 1 2a`},
		{"net.example.case/TXT", `PR #1`},
		// TODO duplicate records (different but equivalent keys)
	} {
		rev1 = newEntry(t, pe3.Prefix+entry.key, entry.value)
	}
	var rev2 int64
	for _, entry := range []struct {
		key, value string
	}{
		{"arpa.in-addr/192.0.2/-options-", `{"zone-append-domain": "example.net."}`},
		{"arpa.in-addr/192.0.2/SOA", `{"primary": "ns1", "mail": "horst.master"}`},
		{"arpa.in-addr/192.0.2/NS#a", `{"hostname": "ns1"}`},
		{"arpa.in-addr/192.0.2/NS#b", `ns2.example.net.`},
		{"arpa.in-addr/192.0.2/2/PTR", `="ns1"`},
		{"arpa.in-addr/192.0.2/3/PTR", `="ns2"`},
	} {
		rev2 = newEntry(t, pe3.Prefix+entry.key, entry.value)
	}
	err = waitFor(t, "data loaded", func() bool { return currentRevision == rev2 }, 100*time.Millisecond, 10*time.Second)
	fatalOnErr(t, "wait for data loaded", err)
	// PDNS
	pdns, err := startPDNS(t)
	fatalOnErr(t, "start PDNS container", err)
	defer pdns.Terminate()
	t.Logf("PDNS endpoint: %s", pdns.Endpoint)
	// queries
	dc := &dns.Client{
		Net:     "tcp",
		Timeout: 10 * time.Second,
	}
	type querySpec struct {
		name       string
		qtype      uint16
		answer     dns.Msg
		conditions map[string]Condition
	}
	conditions := map[string]Condition{
		`->MsgHdr>Response`:              CompareWith[bool]{true},
		`->MsgHdr>Authoritative`:         OtherDefault[bool]{Value: true},
		`->Answer`:                       SliceContains{Size: true},
		`->(Answer|Ns)@\d->Hdr>Class`:    OtherDefault[uint16]{Value: dns.ClassINET},
		`->Answer@\d->Hdr>Name`:          WhenDefault[string]{},
		`->Answer@\d->Hdr>Rrtype`:        WhenDefault[uint16]{},
		`->(Answer|Ns)@\d->Hdr>Rdlength`: Ignore{},
		`->Answer@\d->Hdr>Ttl`:           OtherDefault[uint32]{Value: 3600},
		`->Extra`:                        Ignore{},
	}
	qs := func(name string, qtype uint16, answer dns.Msg, extraConditions ...map[string]Condition) querySpec {
		qs := querySpec{name, qtype, answer, conditions}
		for _, newConditions := range extraConditions {
			qs.conditions = maps.Clone(qs.conditions)
			maps.Copy(qs.conditions, newConditions)
		}
		return qs
	}
	exampleNetSOA := func(ttl uint32) *dns.SOA {
		return &dns.SOA{Hdr: dns.RR_Header{Name: "example.net.", Rrtype: dns.TypeSOA, Ttl: ttl},
			Ns: "ns1.example.net.", Mbox: "horst\\.master.example.net.", Serial: uint32(rev1), Refresh: 3600, Retry: 1800, Expire: 604800, Minttl: 600}
	}
	v4arpaSOA := func(ttl uint32) *dns.SOA {
		return &dns.SOA{Hdr: dns.RR_Header{Name: "2.0.192.in-addr.arpa.", Rrtype: dns.TypeSOA, Ttl: ttl},
			Ns: "ns1.example.net.", Mbox: "horst\\.master.example.net.", Serial: uint32(rev2), Refresh: 3600, Retry: 1800, Expire: 604800, Minttl: 600}
	}
	for i, q := range []querySpec{
		qs("example.net.", dns.TypeSOA, dns.Msg{Answer: []dns.RR{
			exampleNetSOA(3600),
		}}),
		qs("example.net.", dns.TypeNS, dns.Msg{Answer: []dns.RR{
			&dns.NS{Ns: "ns1.example.net."},
			&dns.NS{Ns: "ns2.example.net."},
		}}),
		qs("ns1.example.net.", dns.TypeA, dns.Msg{Answer: []dns.RR{
			&dns.A{A: []byte{192, 0, 2, 2}},
		}}),
		qs("ns1.example.net.", dns.TypeAAAA, dns.Msg{Answer: []dns.RR{
			&dns.AAAA{AAAA: net.ParseIP("2001:db8::2")},
		}}),
		qs("ns1.example.net.", dns.TypeANY, dns.Msg{Answer: []dns.RR{
			&dns.A{Hdr: dns.RR_Header{Rrtype: dns.TypeA}, A: []byte{192, 0, 2, 2}},
			&dns.AAAA{Hdr: dns.RR_Header{Rrtype: dns.TypeAAAA}, AAAA: net.ParseIP("2001:db8::2")},
		}}),
		qs("ns2.example.net.", dns.TypeA, dns.Msg{Answer: []dns.RR{
			&dns.A{A: []byte{192, 0, 2, 3}},
		}}),
		qs("ns2.example.net.", dns.TypeAAAA, dns.Msg{Answer: []dns.RR{
			&dns.AAAA{AAAA: net.ParseIP("2001:db8::3")},
		}}),
		qs("example.net.", dns.TypeMX, dns.Msg{Answer: []dns.RR{
			&dns.MX{Hdr: dns.RR_Header{Ttl: 7200}, Preference: 10, Mx: "mail.example.net."},
		}}),
		qs("mail.example.net.", dns.TypeA, dns.Msg{Answer: []dns.RR{
			&dns.A{A: []byte{192, 0, 2, 10}},
		}}),
		qs("mail.example.net.", dns.TypeAAAA, dns.Msg{Answer: []dns.RR{
			&dns.AAAA{AAAA: net.ParseIP("2001:db8::10")},
		}}),
		qs("example.net.", dns.TypeTXT, dns.Msg{Answer: []dns.RR{
			&dns.TXT{Txt: []string{"v=spf1 ip4:192.0.2.0/24 ip6:2001:db8::/32 -all"}},
			&dns.TXT{Txt: []string{"{text which begins with a curly brace (the id too)}"}},
		}}),
		qs("versioned.example.net.", dns.TypeTXT, dns.Msg{Answer: []dns.RR{
			&dns.TXT{Txt: []string{fmt.Sprintf("@%s", dataVersion)}},
		}}),
		qs("kerberos1.example.net.", dns.TypeAAAA, dns.Msg{Answer: []dns.RR{
			&dns.AAAA{AAAA: net.ParseIP("2001:db8::15")},
		}}),
		qs("_kerberos._tcp.example.net.", dns.TypeSRV, dns.Msg{Answer: []dns.RR{
			&dns.SRV{Port: 88, Target: "kerberos1.example.net."},
			&dns.SRV{Port: 88, Target: "kerberos2.example.net."},
		}}),
		qs("kerberos-master.example.net.", dns.TypeCNAME, dns.Msg{Answer: []dns.RR{
			&dns.CNAME{Target: "kerberos1.example.net."},
		}}),
		qs("kerberos-master.example.net.", dns.TypeA, dns.Msg{Answer: []dns.RR{
			&dns.CNAME{ /*Hdr: dns.RR_Header{Rrtype: dns.TypeCNAME},*/ Target: "kerberos1.example.net."},
			&dns.A{A: []byte{192, 0, 2, 15}},
		}}),
		qs("mail.example.net.", dns.TypeHINFO, dns.Msg{Answer: []dns.RR{
			&dns.HINFO{Hdr: dns.RR_Header{Ttl: 7200}, Cpu: "amd64", Os: "Linux"},
		}}),
		qs("example.net.", 123, dns.Msg{Answer: []dns.RR{
			&dns.RFC3597{Rdata: ""},
		}}),
		qs("example.net.", 237, dns.Msg{Answer: []dns.RR{
			&dns.RFC3597{Rdata: "2a"},
		}}),
		qs("gibberish.example.net.", dns.TypeANY, dns.Msg{MsgHdr: dns.MsgHdr{Rcode: dns.RcodeNameError}, Ns: []dns.RR{
			exampleNetSOA(600),
		}}),
		qs("CaSe.eXample.Net.", dns.TypeTXT, dns.Msg{Answer: []dns.RR{
			&dns.TXT{Txt: []string{"PR #1"}},
		}}),
		qs("2.0.192.in-addr.arpa.", dns.TypeSOA, dns.Msg{Answer: []dns.RR{
			v4arpaSOA(3600),
		}}),
		qs("2.0.192.in-addr.arpa.", dns.TypeNS, dns.Msg{Answer: []dns.RR{
			&dns.NS{Ns: "ns1.example.net."},
			&dns.NS{Ns: "ns2.example.net."},
		}}),
		qs("2.2.0.192.in-addr.arpa.", dns.TypePTR, dns.Msg{Answer: []dns.RR{
			&dns.PTR{Ptr: "ns1.example.net."},
		}}),
	} {
		query := new(dns.Msg)
		query.Id = uint16(i + 1)
		q.answer.MsgHdr.Id = query.Id
		query.Question = make([]dns.Question, 1)
		query.Question[0] = dns.Question{Name: q.name, Qtype: q.qtype, Qclass: dns.ClassINET}
		q.answer.Question = query.Question
		c := q.conditions
		if c == nil {
			c = conditions
		}
		check(t, fmt.Sprintf("%s/%s", q.name, dns.TypeToString[q.qtype]), func(query *dns.Msg) (*dns.Msg, error) {
			msg, _, err := dc.Exchange(query, pdns.Endpoint)
			return msg, err
		}, query, ve[*dns.Msg]{v: &q.answer, c: c})
	}
}

func TestUnixListener(t *testing.T) {
	t.Skip("not implemented yet")
}

func TestHttpListener(t *testing.T) {
	t.Skip("not implemented yet")
}
