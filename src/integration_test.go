//go:build integration

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
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type etcdProc struct {
	cmd      *exec.Cmd
	dataDir  string
	endpoint string
}

var (
	etcdVersion *string
)

func TestMain(m *testing.M) {
	etcdVersion = flag.String("test-etcd-version", "3.6.6", "ETCD version for integration test, e.g. 3.6.6 (the default)")
	if v := os.Getenv("TEST_ETCD_VERSION"); v != "" {
		etcdVersion = &v
	}
	os.Exit(m.Run())
}

func pickFreeLocalPort() string {
	listen, _ := net.Listen("tcp", "127.0.0.1:0")
	defer listen.Close()
	return listen.Addr().String()
}

func startEtcd(t *testing.T, version string) *etcdProc {
	t.Helper()
	etcdBin := fetchEtcdBinaryCached(t, version)
	cmd := exec.Command(etcdBin, "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to run etcd: %v, output: %s", err, output)
	}
	t.Logf("etcd binary: %s", etcdBin)
	peerAddr := pickFreeLocalPort()
	peerURL := "http://" + peerAddr
	clientAddr := pickFreeLocalPort()
	clientURL := "http://" + clientAddr
	dir := t.TempDir()
	cmd = exec.Command(etcdBin,
		"--data-dir", filepath.Join(dir, "etcd-data"),
		"--listen-peer-urls", peerURL,
		"--listen-client-urls", clientURL,
		"--advertise-client-urls", clientURL,
		"--initial-advertise-peer-urls", peerURL,
		"--initial-cluster", "default="+peerURL)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err = cmd.Start(); err != nil {
		t.Fatalf("failed to start etcd: %v", err)
	}
	t.Logf("started ETCD [%d]", cmd.Process.Pid)
	go io.Copy(os.Stdout, stdout)
	go io.Copy(os.Stderr, stderr)
	deadline := time.Now().Add(10 * time.Second)
	healthURL := clientURL + "/health"
	for time.Now().Before(deadline) {
		res, err := http.Get(healthURL)
		if err == nil && res.StatusCode == http.StatusOK {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return &etcdProc{cmd, dir, clientAddr}
}

func (ep *etcdProc) Stop(t *testing.T) {
	t.Helper()
	if ep.cmd == nil || ep.cmd.Process == nil {
		return
	}
	t.Logf("stopping ETCD [%d]", ep.cmd.Process.Pid)
	_ = ep.cmd.Process.Kill()
	_ = ep.cmd.Wait()
}

func TestRequests(t *testing.T) {
	defer (handleExitInLogging(t))()
	defer recoverPanics(t)
	// start ETCD
	etcd := startEtcd(t, *etcdVersion)
	defer etcd.Stop(t)
	// start pdns-etcd3 (main function)
	inR, inW, _ := os.Pipe()
	defer func() {
		t.Log("closing input stream to pdns-etcd3")
		inW.Close()
	}()
	outR, outW, _ := os.Pipe()
	defer outR.Close() // this should be done automatically by pdns-etcd3, but just in case
	config := ""
	timeout, _ := time.ParseDuration("2s")
	prefix := ""
	args = programArgs{
		ConfigFile:  &config,
		Endpoints:   &etcd.endpoint,
		DialTimeout: &timeout,
		Prefix:      &prefix,
	}
	t.Logf("starting pdns-etcd3.serve() with ETCD endpoint %s", etcd.endpoint)
	go serve(newPdnsClient(0, inR, outW))
	pe3 := newComm[any](outR, inW)
	action := func(request pdnsRequest) (any, error) {
		t.Logf("request: %s", val2str(request))
		pe3.write(request)
		response, err := pe3.read()
		t.Logf("response, err: %s, %v", val2str(*response), err)
		return *response, err
	}
	testPrefix := "/DNS/"
	request := pdnsRequest{"initialize", objectType[any]{"pdns-version": "3", "prefix": testPrefix, "log-debug": "main+pdns+etcd+data"}}
	var expectedResponse any
	expectedResponse = map[string]any{"result": true, "log": Ignore{}}
	if !check(t, "initialize", action, request, ve[any]{v: expectedResponse}) {
		t.Fatalf("failed to initialize")
	}
	if prefix != testPrefix {
		t.Fatalf("prefix mismatch after initialize: expected %q, got %q", testPrefix, prefix)
	}
	newEntry := func(key, value string) int64 {
		if resp, err := put(prefix+key, value, 10*time.Second); err != nil {
			t.Fatalf("failed to put %q (%q): %v", key, value, err)
			return 0
		} else {
			return resp.Header.Revision
		}
	}
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
		rev1 = newEntry(entry.key, entry.value)
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
		rev2 = newEntry(entry.key, entry.value)
	}
	time.Sleep(1 * time.Second)
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
			{objectType[any]{"qname": "CaSe.example.net", "qtype": "TXT"}, []any{
				map[string]any{"qname": "CaSe.example.net.", "qtype": "TXT", "content": "PR #1", "ttl": float64(3600), "auth": true},
			}},
		} {
			check[pdnsRequest, any](t, val2str(spec.parameters), action, pdnsRequest{"lookup", spec.parameters}, ve[any]{v: map[string]any{"result": spec.result}})
		}
	})
	t.Log("finished")
}
