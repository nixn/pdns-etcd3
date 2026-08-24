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
	"io"
	"maps"
	"math/rand"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/miekg/dns"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func txnT(t *testing.T, ops ...clientv3.Op) int64 {
	t.Helper()
	if resp, err := cli.Txn(10*time.Second, ops...); err != nil {
		Fatalf(t, "failed to commit transaction (%d ops): %s", len(ops), err)
		return -1
	} else if !resp.Succeeded {
		Fatalf(t, "transaction did not succeed (%d ops)", len(ops))
		return -1
	} else {
		return resp.Header.Revision
	}
}

func putT(t *testing.T, prefix, key, value string) int64 { //nolint:unused
	t.Helper()
	if resp, err := cli.Put(prefix+key, value, 10*time.Second); err != nil {
		Fatalf(t, "failed to put %q: %s", prefix+key, err)
		return -1
	} else {
		return resp.Header.Revision
	}
}

func waitForRevision(t *testing.T, rev int64, desc string) {
	t.Helper()
	err := waitFor(t, desc, func() bool { return cli.CurrentRevision >= rev }, 10*time.Millisecond, 10*time.Second)
	fatalOnErr(t, "wait for "+desc, err)
}

func revs(rev int64, revs ...*int64) {
	for _, rp := range revs {
		*rp = rev
	}
}

func TestPipeRequests(t *testing.T) {
	defer recoverPanicsT(t)
	// start ETCD
	etcd, err := startETCD(t)
	fatalOnErr(t, "start ETCD container", err)
	defer etcd.Terminate()
	Logf(t, "ETCD endpoint: %s", etcd.Endpoint)
	sleepT(t, 1*time.Second)
	// start pdns-etcd3 (main function)
	Logf(t, "starting pdns-etcd3")
	cli = new(etcdClient)
	status = new(statusType)
	inR, inW, _ := os.Pipe()
	defer func() {
		t.Log("closing input stream to pdns-etcd3")
		closeNoError(inW)
	}()
	outR, outW, _ := os.Pipe()
	defer closeNoError(outR) // this should be done automatically by pdns-etcd3, but just in case
	config := ""
	timeout, _ := time.ParseDuration("5s")
	keepAliveTime := defaultDialKeepAliveTime
	keepAliveTimeout := defaultDialKeepAliveTimeout
	autoSync := defaultAutoSyncInterval
	permitWithoutStream := defaultPermitWithoutStream
	prefix := ""
	args = programArgs{
		ConfigFile:           &config,
		Endpoints:            &etcd.Endpoint,
		DialTimeout:          &timeout,
		DialKeepAliveTime:    &keepAliveTime,
		DialKeepAliveTimeout: &keepAliveTimeout,
		AutoSyncInterval:     &autoSync,
		PermitWithoutStream:  &permitWithoutStream,
		Prefix:               &prefix,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wg := new(WaitGroup).Init()
	go pipe(ctx, wg, inR, outW, false)
	pe3 := newComm[any](ctx, outR, inW)
	action := func(t *testing.T, request pdnsRequest) (any, error) {
		Logf(t, "request: %s", val2str(request))
		_ = pe3.write(request)
		response, err := pe3.read()
		Logf(t, "response: %s, err: %v", val2str(*response), err)
		return *response, err
	}
	{
		testPrefix := "/DNS/"
		request := pdnsRequest{"initialize", objectType[any]{"pdns-version": "3", "prefix": testPrefix, "log-level": "10;data.values=2"}}
		expectedResponse := map[string]any{"result": true, "log": Ignore{}}
		if !checkRun(t, "initialize", action, request, ve[any]{v: expectedResponse}, false) {
			Fatalf(t, "failed to initialize")
		}
		if prefix != testPrefix {
			Fatalf(t, "prefix mismatch after initialize: expected %q, got %q", testPrefix, prefix)
		}
	}
	err = waitFor(t, "populated", func() bool { return status.populated }, 10*time.Millisecond, 30*time.Second)
	fatalOnErr(t, "wait for populated", err)
	sleepT(t, 1*time.Second)
	put := func(key, value string) clientv3.Op {
		return putOp(prefix+key, value)
	}
	lookupTest := func(t *testing.T, qname, qtype string, result ...any) {
		checkRun[pdnsRequest, any](t, fmt.Sprintf("lookup %s %s", qname, qtype), action,
			pdnsRequest{Method: "lookup", Parameters: objectType[any]{"qname": qname, "qtype": qtype}},
			ve[any]{v: map[string]any{"result": SliceContains{All: true, Only: true, Elements: result}}},
			false)
	}
	rev1 := txnT(t,
		put("net.example/SOA", `{"primary": "ns1", "mail": "horst.master"}`),
		put("-defaults-/SOA", "---\n#this is yaml\nrefresh: 1h\nretry: 30m\nexpire: 604800\nneg-ttl: 10m\n"),
		put("-defaults-", `{"ttl": "1h"}`),
	)
	rev2 := txnT(t,
		put("arpa.in-addr/192.0.2/-options-", `{"zone-append-domain": "example.net."}`),
		put("arpa.in-addr/192.0.2/SOA", `{"primary": "ns1", "mail": "horst.master"}`),
	)
	rev3 := txnT(t,
		put("arpa.ip6/2.0.0.1.0.d.b.8/-options-", `{"zone-append-domain": "example.net."}`),
		put("arpa.ip6/2.0.0.1.0.d.b.8/SOA", `{"primary": "ns1", "mail": "horst.master"}`),
	)
	waitForRevision(t, rev3, "data loaded (SOAs)")
	t.Run("SOAs", func(t *testing.T) {
		for qname, rev := range map[string]int64{"example.net": rev1, "2.0.192.in-addr.arpa": rev2, "8.b.d.0.1.0.0.2.ip6.arpa": rev3} {
			lookupTest(t, qname, "SOA",
				map[string]any{"qname": qname + ".", "qtype": "SOA", "content": fmt.Sprintf(`ns1.example.net. horst\.master.example.net. %d 3600 1800 604800 600`, rev), "ttl": float64(3600), "auth": true},
			)
		}
	})
	t.Run("ns", func(t *testing.T) {
		revs(txnT(t,
			put("net.example/NS#first", `{"hostname": "ns"}`),
			put("net.example/-options-/A", "---\nip-prefix: [192, 0, 2]"),
			put("net.example/ns/A", "---\nip: 2"),
			put("net.example/-options-/AAAA", `{"ip-prefix": "20010db8"}`),
			put("net.example/ns/AAAA", `="02"`),
			put("arpa.in-addr/192.0.2/2/PTR", "`ns"),
			put("arpa.ip6/2.0.0.1.0.d.b.8/0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0/0.0.0.2/PTR", `ns`),
		), &rev1, &rev2, &rev3)
		waitForRevision(t, rev1, "ns data loaded")
		lookupTest(t, "example.net", "NS",
			map[string]any{"qname": "example.net.", "qtype": "NS", "content": "ns.example.net.", "ttl": float64(3600), "auth": true},
		)
		lookupTest(t, "ns.example.net", "ANY",
			map[string]any{"qname": "ns.example.net.", "qtype": "A", "content": "192.0.2.2", "ttl": float64(3600), "auth": true},
			map[string]any{"qname": "ns.example.net.", "qtype": "AAAA", "content": "2001:db8::2", "ttl": float64(3600), "auth": true},
		)
		for _, qname := range []string{"2.2.0.192.in-addr.arpa", "2.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa"} {
			lookupTest(t, qname, "PTR", map[string]any{"qname": qname + ".", "qtype": "PTR", "content": "ns.example.net.", "ttl": float64(3600), "auth": true})
		}
	})
}

type CtLogger struct {
	t    *testing.T
	name string
}

func (ctl CtLogger) Accept(log testcontainers.Log) {
	Logf(ctl.t, "%s[%s]: %s", ctl.name, log.LogType, log.Content)
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
				Errorf(t, "failed to terminate container: %s", err)
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

func startETCD(t *testing.T, netAliases ...map[string][]string) (*ctInfo, error) {
	t.Helper()
	// optional: attach to a docker network so other containers (e.g. a pipe-mode pe3 running
	// inside the PowerDNS container) can reach etcd by alias
	var nets []string
	var aliases map[string][]string
	if len(netAliases) > 0 && netAliases[0] != nil {
		aliases = netAliases[0]
		for n := range aliases {
			nets = append(nets, n)
		}
	}
	image := fmt.Sprintf("quay.io/coreos/etcd:v%s", getenvT("ETCD_VERSION", "3.6.7"))
	Logf(t, "Using ETCD image %s", image)
	return startContainer(t, testcontainers.ContainerRequest{
		Image:          image,
		Hostname:       "etcd",
		Networks:       nets,
		NetworkAliases: aliases,
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
		},
		WaitingFor: wait.ForLog("ready to serve client requests"),
	}, "2379")
}

type pe3Info struct {
	Terminate   func()
	HttpAddress *url.URL
	Prefix      string
}

// pe3HTTPPort returns the host port for the standalone HTTP listener. It is fixed
// by default (also used in the remote-connection-string PDNS setting), but
// PE3_TEST_HTTP_PORT overrides it for hosts where 8053 is already taken.
func pe3HTTPPort() string {
	if p := os.Getenv("PE3_TEST_HTTP_PORT"); p != "" {
		return p
	}
	return "8053"
}

func startPE3(t *testing.T, etcdEndpoint, prefix string, moreArgs ...string) pe3Info {
	t.Helper()
	httpAddress, _ := url.Parse("http://0.0.0.0:" + pe3HTTPPort())
	doneCtx, done := context.WithCancel(context.Background())
	osSignals := make(chan os.Signal, 1)
	cli = new(etcdClient)
	status = new(statusType)
	go func() {
		defer done()
		args := []string{"-standalone=" + httpAddress.String(), "-timeout=5s", "-endpoints=" + etcdEndpoint, "-prefix=" + prefix}
		args = append(args, moreArgs...)
		main(VersionType{IsDevelopment: true}, getGitVersion(t), args, osSignals, true)
		Logf(t, "pe3 finished")
	}()
	return pe3Info{
		func() {
			Logf(t, "sending os.Interrupt to pe3")
			osSignals <- os.Interrupt
			<-doneCtx.Done()
			Logf(t, "pe3 context done")
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

func linesReader(lines []string) *strings.Reader {
	s := ""
	for _, line := range lines {
		s += line + "\n"
	}
	return strings.NewReader(s)
}

type pdnsInfo struct {
	*ctInfo
	Version string
}

func startPDNS(t *testing.T, dynamicSettings map[string]string, netAliases ...map[string][]string) (pdnsInfo, error) {
	t.Helper()
	// optional: attach to a docker network (network name → aliases) so other containers can reach it
	var nets []string
	var aliases map[string][]string
	if len(netAliases) > 0 && netAliases[0] != nil {
		aliases = netAliases[0]
		for n := range aliases {
			nets = append(nets, n)
		}
	}
	var image string
	var fromDockerfile testcontainers.FromDockerfile
	repo := "localhost/pdns-etcd3/pdns"
	v := getenvT("PDNS_VERSION", "50")
	switch v {
	case "34", "40", "41":
		Logf(t, "Using PDNS image %s:%s (from testdata/pdns-%s/Dockerfile)", repo, v, v)
		fromDockerfile = testcontainers.FromDockerfile{
			Context:   "../testdata/pdns-" + v,
			Repo:      repo,
			Tag:       v,
			KeepImage: true,
			//PrintBuildLog: true,
		}
	case "44", "45", "46", "47", "48", "49", "50", "51":
		image = fmt.Sprintf("powerdns/pdns-auth-%s", v)
		Logf(t, "Using PDNS image %s", image)
	default:
		Fatalf(t, "invalid PDNS version: %q", v)
	}
	settings := []string{
		fmt.Sprintf("remote-connection-string=http:url=http://host.docker.internal:%s/client-id=%013s/pdns-version=%s/,post=yes,post_json=yes,timeout=10000", pe3HTTPPort(), strconv.FormatUint(rand.Uint64(), 32), v[:1]),
		"cache-ttl=0",
		"query-cache-ttl=0",
		"negquery-cache-ttl=0",
	}
	if v >= "40" {
		if v < "45" {
			settings = append(settings, "domain-metadata-cache-ttl=0")
		} else {
			settings = append(settings, "zone-metadata-cache-ttl=0")
		}
	}
	if v >= "44" {
		settings = append(settings, "consistent-backends=no")
	}
	if v >= "45" {
		settings = append(settings, "zone-cache-refresh-interval=0")
	}
	for setting, sinceVersion := range dynamicSettings {
		if v >= sinceVersion {
			settings = append(settings, setting)
		}
	}
	Logf(t, "PDNS settings: %v", settings)
	ctInfo, err := startContainer(t, testcontainers.ContainerRequest{
		Image:          image,
		FromDockerfile: fromDockerfile,
		Networks:       nets,
		NetworkAliases: aliases,
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.ExtraHosts = []string{"host.docker.internal:host-gateway"}
		},
		ExposedPorts:   []string{"53/tcp"},
		LogConsumerCfg: &testcontainers.LogConsumerConfig{Consumers: []testcontainers.LogConsumer{CtLogger{t, "PDNS"}}},
		Files: []testcontainers.ContainerFile{
			{HostFilePath: "../testdata/pdns.conf", ContainerFilePath: "/etc/powerdns/pdns.conf", FileMode: 0o555},
			{Reader: linesReader(settings), ContainerFilePath: "/etc/powerdns/pdns.d/settings.conf", FileMode: 0o555},
		},
		WaitingFor: wait.ForLog("ready to distribute questions|operating unthreaded").AsRegexp(),
	}, "53/tcp")
	return pdnsInfo{ctInfo, v}, err
}

func basicDataTxn(t *testing.T, prefix string) (int64, []clientv3.Op) {
	t.Helper()
	put := func(key, value string) clientv3.Op {
		return putOp(prefix+key, value)
	}
	putSOA1 := put("net.example/SOA", `{}`)
	putSOA2 := put("arpa.in-addr/192.0.2/SOA", `{}`)
	putSOA3 := put("arpa.ip6/2.0.0.1.0.d.b.8/SOA", `{}`)
	return txnT(t,
		put("-defaults-", `{ttl: "1h"}`),
		put("-defaults-/SOA", "---\n#this is yaml\nrefresh: 1h\nretry: 30m\nexpire: 604800\nneg-ttl: 10m\nprimary: ns1\nmail: horst.master\n"),
		put("-defaults-/SRV", `{priority: 10, weight: 1}`),
		put("arpa.in-addr/192.0.2/-options-", `{"zone-append-domain": "example.net."}`),
		put("arpa.ip6/2.0.0.1.0.d.b.8/-options-", `{"zone-append-domain": "example.net."}`),
		put("net.example/-options-/A", `{"ip-prefix": [192, 0, 2]}`),
		put("net.example/-options-/AAAA", `{"ip-prefix": "20010db8"}`),
		// SOAs
		putSOA1,
		putSOA2,
		putSOA3,
		// NS (first)
		put("net.example/NS#first", `="ns1"`),
		put("arpa.in-addr/192.0.2/NS#a", `="ns1"`),
		put("arpa.ip6/2.0.0.1.0.d.b.8/NS#1", `="ns1"`),
		// ns1
		put("net.example/ns1/A", `=2`),
		put("net.example/ns1/AAAA", `=2`),
		put("arpa.in-addr/192.0.2/2/PTR", `="ns1"`),
		put("arpa.ip6/2.0.0.1.0.d.b.8/0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0/0.0.0.2/PTR", `="ns1"`),
	), []clientv3.Op{putSOA1, putSOA2, putSOA3}
}

type querySpecT struct {
	name       string
	qtype      uint16
	answer     dns.Msg
	conditions map[string]Condition
}

var defaultConditions = map[string]Condition{
	`->MsgHdr>Response`:                    CompareWith[bool]{true},
	`->MsgHdr>Authoritative`:               CompareWith[bool]{true}, // OtherDefault does not work here, because the zero value is a valid response value
	`->(Answer|Ns)`:                        SliceContains{All: true, Only: true},
	`->(Answer|Ns|Extra)@\d->Hdr>Class`:    OtherDefault[uint16]{Value: dns.ClassINET},
	`->Answer@\d->Hdr>Name`:                WhenDefault[string]{}, // do not apply to Extra, because the RRs there are of other names! // TODO use OnDefaultSameAs(->Question@0>Name) instead (only on default, because it could be another name, like in CNAME'd answers)
	`->(Answer|Extra)@\d->Hdr>Rrtype`:      WhenDefault[uint16]{}, // applied to Extra, too, because the type is already given in element type and checked by reflection
	`->(Answer|Ns|Extra)@\d->Hdr>Rdlength`: Ignore{},
	`->(Answer|Ns|Extra)@\d->Hdr>Ttl`:      OtherDefault[uint32]{Value: 3600},
	`->Extra`:                              Ignore{},
}

func QueryTest(t *testing.T, pdnsEndpoint string, qs querySpecT, timeout time.Duration, quiet bool) time.Duration {
	t.Helper()
	q := new(dns.Msg)
	q.Id = uint16(rand.Uint32() & 0xffff)
	qs.answer.Id = q.Id
	q.Question = make([]dns.Question, 1)
	q.Question[0] = dns.Question{Name: qs.name, Qtype: qs.qtype, Qclass: dns.ClassINET}
	qs.answer.Question = q.Question
	c := qs.conditions
	if c == nil {
		c = defaultConditions
	}
	var duration time.Duration
	checkT(t, func(t *testing.T, query *dns.Msg) (*dns.Msg, error) {
		dc := &dns.Client{
			Net:     "tcp",
			Timeout: timeout,
		}
		if !quiet {
			Logf(t, "sending query to PDNS: %v", query.Question)
		}
		msg, dur, err := dc.Exchange(query, pdnsEndpoint)
		duration = dur
		if err == nil {
			if !quiet {
				Logf(t, "PDNS response (in %s):\n%s", dur, msg)
			}
			if len(msg.Answer) == 0 {
				if len(msg.Extra) > 0 {
					if !quiet {
						Logf(t, "Answer seems to be in Extra, moving")
					}
					msg.Answer = msg.Extra
					msg.Extra = nil
				}
			} else if qs.qtype == dns.TypeANY && len(msg.Extra) > 0 { // len(msg.Answer) is > 0!
				if !quiet {
					Logf(t, "ANY query, and Answer seems to be partially split into Extra, merging")
				}
				msg.Answer = append(msg.Answer, msg.Extra...)
				msg.Extra = nil
			}
		}
		return msg, err
	}, q, ve[*dns.Msg]{v: &qs.answer, c: c}, quiet)
	return duration
}

func querySpec(name string, qtype uint16, answer dns.Msg, extraConditions ...map[string]Condition) querySpecT {
	qs := querySpecT{name, qtype, answer, defaultConditions}
	for _, newConditions := range extraConditions {
		qs.conditions = maps.Clone(qs.conditions)
		maps.Copy(qs.conditions, newConditions)
	}
	return qs
}

func TestWithPDNS(t *testing.T) {
	defer recoverPanicsT(t)
	// ETCD
	etcd, err := startETCD(t)
	fatalOnErr(t, "start ETCD container", err)
	defer etcd.Terminate()
	Logf(t, "ETCD endpoint (2379): %s", etcd.Endpoint)
	// PDNS-ETCD3
	sleepT(t, 1*time.Second)
	pe3 := startPE3(t, etcd.Endpoint, "", "-log-level=10;data.values=2", "-pdns-version="+getenvT("PDNS_VERSION", fmt.Sprintf("%d", defaultPdnsVersion))[:1])
	defer pe3.Terminate()
	Logf(t, "PDNS-ETCD3 endpoint: %s", pe3.HttpAddress)
	err = waitFor(t, "PE3 ready", func() bool { return status.serving }, 10*time.Millisecond, 30*time.Second)
	fatalOnErr(t, "wait for PE3 ready", err)
	sleepT(t, 1*time.Second)
	// fill data
	put := func(key, value string) clientv3.Op {
		return putOp(pe3.Prefix+key, value)
	}
	del := func(key string) clientv3.Op {
		return delOp(pe3.Prefix+key, false)
	}
	withCleanup := func(t *testing.T, puts map[string]string, action func(), postOps []clientv3.Op, rs ...*int64) int64 {
		var ps, ds []clientv3.Op
		for k, v := range puts {
			ps = append(ps, put(k, v))
			ds = append(ds, del(k))
		}
		revs(txnT(t, ps...), rs...)
		action()
		ds = append(ds, postOps...)
		return txnT(t, ds...)
	}
	// fill with basic data to have the minimal number of entries to keep logs small when reloading zones
	rev1, putSOA := basicDataTxn(t, pe3.Prefix)
	var rev2, rev3 int64
	revs(rev1, &rev2, &rev3)
	waitForRevision(t, rev1, "basic data loaded")
	// PDNS
	pdns, err := startPDNS(t, map[string]string{
		"resolver=127.0.0.1":   "41",
		"expand-alias=yes":     "41",
		"dname-processing=yes": "40",
	})
	fatalOnErr(t, "start PDNS container", err)
	defer pdns.Terminate()
	Logf(t, "PDNS endpoint: %s", pdns.Endpoint)
	// queries
	qs := querySpec
	soa := func(name string, rev *int64) func(ttl uint32) *dns.SOA {
		return func(ttl uint32) *dns.SOA {
			return &dns.SOA{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeSOA, Ttl: ttl},
				Ns: "ns1.example.net.", Mbox: "horst\\.master.example.net.", Serial: uint32(*rev), Refresh: 3600, Retry: 1800, Expire: 604800, Minttl: 600}
		}
	}
	exampleNet := "example.net"
	v4arpa := "2.0.192.in-addr.arpa"
	v6arpa := "8.b.d.0.1.0.0.2.ip6.arpa"
	exampleNetSOA := soa(exampleNet+".", &rev1)
	v4arpaSOA := soa(v4arpa+".", &rev2)
	v6arpaSOA := soa(v6arpa+".", &rev3)
	queryTest := func(t *testing.T, qs querySpecT) {
		QueryTest(t, pdns.Endpoint, qs, 10*time.Second, false)
	}
	t.Run("SOA", func(t *testing.T) {
		for zone, soa := range map[string]func(uint32) *dns.SOA{
			exampleNet: exampleNetSOA,
			v4arpa:     v4arpaSOA,
			v6arpa:     v6arpaSOA,
		} {
			t.Run(zone, func(t *testing.T) {
				queryTest(t, qs(zone+".", dns.TypeSOA, dns.Msg{Answer: []dns.RR{soa(3600)}}))
			})
		}
	})
	t.Run("NS", func(t *testing.T) {
		revs(withCleanup(t, map[string]string{
			// NS (second)
			"net.example/NS#second":         `="ns2" // the second one`,
			"arpa.in-addr/192.0.2/NS#b":     `="ns2"`,
			"arpa.ip6/2.0.0.1.0.d.b.8/NS#2": `="ns2"`,
			// ns2
			"net.example/ns2/A":          `=3 // nice, huh?`,
			"net.example/ns2/AAAA":       `3`,
			"arpa.in-addr/192.0.2/3/PTR": `= /* nasty place for a comment */ /* and a second one */ "ns2"`,
			"arpa.ip6/2.0.0.1.0.d.b.8/0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0/0.0.0.3/PTR": `="ns2"`,
		}, func() {
			waitForRevision(t, rev1, "NS (second) data loaded")
			for _, zone := range []string{exampleNet, v4arpa, v6arpa} {
				t.Run(zone, func(t *testing.T) {
					queryTest(t, qs(zone+".", dns.TypeNS, dns.Msg{Answer: []dns.RR{
						&dns.NS{Ns: "ns1.example.net."},
						&dns.NS{Ns: "ns2.example.net."},
					}, Extra: []dns.RR{
						&dns.A{Hdr: dns.RR_Header{Name: "ns1.example.net."}, A: []byte{192, 0, 2, 2}},
						&dns.A{Hdr: dns.RR_Header{Name: "ns2.example.net."}, A: []byte{192, 0, 2, 3}},
						&dns.AAAA{Hdr: dns.RR_Header{Name: "ns1.example.net."}, AAAA: net.ParseIP("2001:db8::2")},
						&dns.AAAA{Hdr: dns.RR_Header{Name: "ns2.example.net."}, AAAA: net.ParseIP("2001:db8::3")},
					}}, map[string]Condition{`->Extra`: SliceContains{All: false, Only: true}}))
				})
			}
		}, putSOA, &rev1, &rev2, &rev3), &rev1, &rev2, &rev3)
		waitForRevision(t, rev1, "NS (second) data removed")
	})
	t.Run("ANY", func(t *testing.T) {
		queryTest(t, qs("ns1.example.net.", dns.TypeANY, dns.Msg{Answer: []dns.RR{
			&dns.A{Hdr: dns.RR_Header{Rrtype: dns.TypeA}, A: []byte{192, 0, 2, 2}},
			&dns.AAAA{Hdr: dns.RR_Header{Rrtype: dns.TypeAAAA}, AAAA: net.ParseIP("2001:db8::2")},
		}}))
	})
	t.Run("(NXDOMAIN)", func(t *testing.T) {
		queryTest(t, qs("non-existent.example.net.", dns.TypeANY, dns.Msg{
			MsgHdr: dns.MsgHdr{Rcode: dns.RcodeNameError},
			Ns:     []dns.RR{exampleNetSOA(600)},
		}))
	})
	t.Run("PTR", func(t *testing.T) {
		for _, q := range []querySpecT{
			qs("2.2.0.192.in-addr.arpa.", dns.TypePTR, dns.Msg{Answer: []dns.RR{
				&dns.PTR{Ptr: "ns1.example.net."},
			}}),
			qs("2.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa.", dns.TypePTR, dns.Msg{Answer: []dns.RR{
				&dns.PTR{Ptr: "ns1.example.net."},
			}}),
		} {
			queryTest(t, q)
		}
	})
	t.Run("MX", func(t *testing.T) {
		revs(withCleanup(t, map[string]string{
			"net.example/-defaults-/MX": `{/*way too long*/"ttl": "2h"}`,
			"net.example/MX#1":          "{priority: 5, // single line comment\ntarget: \"mail\"}",
			"net.example/mail/A":        `{ip: [192,0,2,10]}`,
			"net.example/mail/AAAA":     `2001:0db8::10`,
		}, func() {
			waitForRevision(t, rev1, "MX data loaded")
			queryTest(t, qs("example.net.", dns.TypeMX, dns.Msg{Answer: []dns.RR{
				&dns.MX{Hdr: dns.RR_Header{Ttl: 7200}, Preference: 5, Mx: "mail.example.net."},
			}, Extra: []dns.RR{
				&dns.A{Hdr: dns.RR_Header{Name: "mail.example.net."}, A: []byte{192, 0, 2, 10}},
				&dns.AAAA{Hdr: dns.RR_Header{Name: "mail.example.net."}, AAAA: net.ParseIP("2001:db8::10")},
			}}, map[string]Condition{`->Extra`: SliceContains{All: true, Only: true}}))
		}, putSOA[:1], &rev1), &rev1)
		waitForRevision(t, rev1, "MX data removed")
	})
	t.Run("TXT", func(t *testing.T) {
		revs(withCleanup(t, map[string]string{
			"net.example/txt/TXT#plain":         `plain string`,
			"net.example/txt/TXT#plain-nows":    `plain-string-no-whitespace`,
			"net.example/txt/TXT#plain-complex": `"a \"complex\" plain \\string"`,
			"net.example/txt/TXT#plain-3":       `"plain" "one" "\\two" "and \"more\""`,
			"net.example/txt/TXT#{j5}":          `{"text":"{text with curly braces (the id too)}"}`,
			"net.example/txt/TXT#{bq}":          "`{text with curly braces}",
			"net.example/txt/TXT#[]":            `{"text":["array", 1, "\\two", "and \"more\""]}`,
			"net.example/txt/TXT#42":            `=42`,
			"net.example/txt/TXT#12.34":         `=12.34`,
		}, func() {
			waitForRevision(t, rev1, "TXT data loaded")
			queryTest(t, qs("txt.example.net.", dns.TypeTXT, dns.Msg{Answer: []dns.RR{
				&dns.TXT{Txt: []string{"plain string"}},
				&dns.TXT{Txt: []string{"plain-string-no-whitespace"}},
				&dns.TXT{Txt: []string{"plain", "one", `\\two`, `and \"more\"`}},
				&dns.TXT{Txt: []string{`a \"complex\" plain \\string`}},
				&dns.TXT{Txt: []string{"{text with curly braces (the id too)}"}},
				&dns.TXT{Txt: []string{"{text with curly braces}"}},
				&dns.TXT{Txt: []string{"array", "1", `\\two`, `and \"more\"`}},
				&dns.TXT{Txt: []string{"42"}},
				&dns.TXT{Txt: []string{"12.34"}},
			}}))
		}, putSOA[:1], &rev1), &rev1)
		waitForRevision(t, rev1, "TXT data removed")
	})
	t.Run("versioned", func(t *testing.T) {
		revs(withCleanup(t, map[string]string{
			"net.example/versioned/TXT@1234.56":                      `@1234.56`,
			"net.example/versioned/TXT@0.1":                          `@0.1`,
			fmt.Sprintf("net.example/versioned/TXT@%s", dataVersion): fmt.Sprintf(`@%s`, dataVersion),
		}, func() {
			waitForRevision(t, rev1, "versioned data loaded")
			queryTest(t, qs("versioned.example.net.", dns.TypeTXT, dns.Msg{Answer: []dns.RR{
				&dns.TXT{Txt: []string{fmt.Sprintf("@%s", dataVersion)}},
			}}))
		}, putSOA[:1], &rev1), &rev1)
		waitForRevision(t, rev1, "versioned data removed")
	})
	t.Run("SRV", func(t *testing.T) {
		revs(withCleanup(t, map[string]string{
			"net.example/-defaults-/#1":                 `{ip: "15"}`,
			"net.example/kerberos1/A#1":                 `_`,
			"net.example/kerberos1/AAAA#1":              `_`,
			"net.example/kerberos2/A#":                  `25`,
			"net.example/kerberos2/AAAA#":               `25`,
			"net.example/_tcp/_kerberos/-defaults-/SRV": `{"port": 88}`,
			"net.example/_tcp/_kerberos/SRV#1":          `{target: "kerberos1", weight: 2}`,
			"net.example/_tcp/_kerberos/SRV#2":          `="kerberos2"`,
			"net.example/_tcp/_kerberos/SRV#invalid":    "---\ntarget: invalid\nport: 70000",
		}, func() {
			waitForRevision(t, rev1, "SRV data loaded")
			queryTest(t, qs("_kerberos._tcp.example.net.", dns.TypeSRV, dns.Msg{Answer: []dns.RR{
				&dns.SRV{Priority: 10, Weight: 2, Port: 88, Target: "kerberos1.example.net."},
				&dns.SRV{Priority: 10, Weight: 1, Port: 88, Target: "kerberos2.example.net."},
			}, Extra: []dns.RR{
				&dns.A{Hdr: dns.RR_Header{Name: "kerberos1.example.net."}, A: []byte{192, 0, 2, 15}},
				&dns.A{Hdr: dns.RR_Header{Name: "kerberos2.example.net."}, A: []byte{192, 0, 2, 25}},
				&dns.AAAA{Hdr: dns.RR_Header{Name: "kerberos1.example.net."}, AAAA: net.ParseIP("2001:db8::15")},
				&dns.AAAA{Hdr: dns.RR_Header{Name: "kerberos2.example.net."}, AAAA: net.ParseIP("2001:db8::25")},
			}}, map[string]Condition{"->Extra": SliceContains{All: true, Only: true}}))
		}, putSOA[:1], &rev1), &rev1)
		waitForRevision(t, rev1, "SRV data removed")
	})
	t.Run("CNAME", func(t *testing.T) {
		revs(withCleanup(t, map[string]string{
			"net.example/cname.external/CNAME": `="something.example.org."`,
			"net.example/cname.internal/CNAME": `="ns1"`,
		}, func() {
			waitForRevision(t, rev1, "CNAME data loaded")
			t.Run("direct", func(t *testing.T) {
				queryTest(t, qs("internal.cname.example.net.", dns.TypeCNAME, dns.Msg{Answer: []dns.RR{
					&dns.CNAME{Target: "ns1.example.net."},
				}}, map[string]Condition{"->Extra": SliceContains{All: true, Only: true}}))
			})
			t.Run("external/A", func(t *testing.T) {
				queryTest(t, qs("external.cname.example.net.", dns.TypeA, dns.Msg{Answer: []dns.RR{
					&dns.CNAME{Target: "something.example.org."},
				}}, map[string]Condition{"->Extra": SliceContains{All: true, Only: true}}))
			})
			t.Run("internal/A", func(t *testing.T) {
				queryTest(t, qs("internal.cname.example.net.", dns.TypeA, dns.Msg{Answer: []dns.RR{
					&dns.CNAME{Target: "ns1.example.net."},
					&dns.A{Hdr: dns.RR_Header{Name: "ns1.example.net."}, A: []byte{192, 0, 2, 2}},
				}}, map[string]Condition{"->Extra": SliceContains{All: true, Only: true}}))
			})
		}, putSOA[:1], &rev1), &rev1)
		waitForRevision(t, rev1, "CNAME data removed")
	})
	t.Run("DNAME", func(t *testing.T) {
		if pdns.Version[0] == '3' {
			t.Skip("skipping DNAME test, DNAME processing is not available in PDNSv3")
		}
		revs(withCleanup(t, map[string]string{
			"net.example/DNAME":         "example.org.",
			"org.example/SOA":           `{}`,
			"org.example/something/TXT": "DNAME works",
		}, func() {
			waitForRevision(t, rev1, "DNAME data loaded")
			queryTest(t, qs("something.example.net.", dns.TypeTXT, dns.Msg{Answer: []dns.RR{
				&dns.DNAME{Target: "example.org."},
				&dns.CNAME{Hdr: dns.RR_Header{Name: "something.example.net."}, Target: "something.example.org."},
				&dns.TXT{Hdr: dns.RR_Header{Name: "something.example.org."}, Txt: []string{"DNAME works"}},
			}}))
		}, putSOA[:1], &rev1), &rev1)
		waitForRevision(t, rev1, "DNAME data removed")
	})
	t.Run("HINFO", func(t *testing.T) {
		revs(withCleanup(t, map[string]string{
			"net.example/hinfo/HINFO": `"amd64" "Linux"`,
			fmt.Sprintf("net.example/hinfo/HINFO#not-object-supported@%s", dataVersion): `{"platform": "arm", "os": "Raspbian"}`,
		}, func() {
			waitForRevision(t, rev1, "HINFO data loaded")
			queryTest(t, qs("hinfo.example.net.", dns.TypeHINFO, dns.Msg{Answer: []dns.RR{
				&dns.HINFO{Cpu: "amd64", Os: "Linux"},
			}}))
		}, putSOA[:1], &rev1), &rev1)
		waitForRevision(t, rev1, "HINFO data removed")
	})
	t.Run("TYPExxx", func(t *testing.T) {
		revs(withCleanup(t, map[string]string{
			"net.example/custom/TYPE123": `\# 0`,
			"net.example/custom/TYPE237": `\# 1 2a`,
		}, func() {
			waitForRevision(t, rev1, "TYPExxx data loaded")
			for qtype, data := range map[uint16]string{
				123: "",
				237: "2a",
			} {
				queryTest(t, qs("custom.example.net.", qtype, dns.Msg{Answer: []dns.RR{
					&dns.RFC3597{Rdata: data},
				}}))
			}
		}, putSOA[:1], &rev1), &rev1)
		waitForRevision(t, rev1, "TYPExxx data removed")
	})
	t.Run("*", func(t *testing.T) {
		revs(withCleanup(t, map[string]string{
			"net.example/wildcard.*/TXT": `wildcard`,
		}, func() {
			waitForRevision(t, rev1, "wildcard data loaded")
			queryTest(t, qs("something.wildcard.example.net.", dns.TypeTXT, dns.Msg{Answer: []dns.RR{
				&dns.TXT{Hdr: dns.RR_Header{Name: "something.wildcard.example.net."}, Txt: []string{"wildcard"}},
			}}))
		}, putSOA[:1], &rev1), &rev1)
		waitForRevision(t, rev1, "wildcard data removed")
	})
	t.Run("CaSe", func(t *testing.T) {
		revs(withCleanup(t, map[string]string{
			"net.example/case/TXT": `PR #1`,
		}, func() {
			waitForRevision(t, rev1, "CaSe data loaded")
			queryTest(t, qs("CaSe.eXample.Net.", dns.TypeTXT, dns.Msg{Answer: []dns.RR{
				&dns.TXT{Hdr: dns.RR_Header{Name: "CaSe.eXample.Net."}, Txt: []string{"PR #1"}},
			}}))
		}, putSOA[:1], &rev1), &rev1)
		waitForRevision(t, rev1, "CaSe data removed")
	})
	t.Run("ALIAS", func(t *testing.T) {
		if pdns.Version < "41" {
			t.Skip("skipping ALIAS test, expanding ALIAS is not available in PDNS v3.x and does not work properly for integration test in v4.0")
		}
		revs(withCleanup(t, map[string]string{
			"net.example/non-alias/A":    `10`,
			"net.example/non-alias/AAAA": `10`,
			"net.example/ALIAS":          `target`,
			"net.example/target/A":       `12`,
			"net.example/target/AAAA":    `12`,
			"net.example/alias/ALIAS":    `target`,
		}, func() {
			waitForRevision(t, rev1, "ALIAS data loaded")
			t.Run("non-alias", func(t *testing.T) {
				queryTest(t, qs("non-alias.example.net.", dns.TypeA, dns.Msg{Answer: []dns.RR{
					&dns.A{Hdr: dns.RR_Header{Name: "non-alias.example.net."}, A: []byte{192, 0, 2, 10}},
				}}))
				queryTest(t, qs("non-alias.example.net.", dns.TypeAAAA, dns.Msg{Answer: []dns.RR{
					&dns.AAAA{Hdr: dns.RR_Header{Name: "non-alias.example.net."}, AAAA: net.ParseIP("2001:db8::10")},
				}}))
			})
			t.Run("(apex)", func(t *testing.T) {
				queryTest(t, qs("example.net.", dns.TypeA, dns.Msg{Answer: []dns.RR{
					&dns.A{Hdr: dns.RR_Header{Name: "example.net."}, A: []byte{192, 0, 2, 12}},
				}}))
				queryTest(t, qs("example.net.", dns.TypeAAAA, dns.Msg{Answer: []dns.RR{
					&dns.AAAA{Hdr: dns.RR_Header{Name: "example.net."}, AAAA: net.ParseIP("2001:db8::12")},
				}}))
			})
			t.Run("alias", func(t *testing.T) {
				queryTest(t, qs("alias.example.net.", dns.TypeA, dns.Msg{Answer: []dns.RR{
					&dns.A{Hdr: dns.RR_Header{Name: "alias.example.net."}, A: []byte{192, 0, 2, 12}},
				}}))
				queryTest(t, qs("alias.example.net.", dns.TypeAAAA, dns.Msg{Answer: []dns.RR{
					&dns.AAAA{Hdr: dns.RR_Header{Name: "alias.example.net."}, AAAA: net.ParseIP("2001:db8::12")},
				}}))
			})
		}, putSOA[:1], &rev1), &rev1)
		waitForRevision(t, rev1, "ALIAS data removed")
	})
	// TODO add tests for metadata after adding support for `pdnsutil metadata` command
}

// primaryModeSetting returns the PDNS config setting that enables primary (master)
// operation for the given 2-digit PDNS version string: "master=yes" before 4.5,
// "primary=yes" from 4.5 on. PDNS 5.0 removed the deprecated "master" alias and FATALs
// on it, so the two are mutually exclusive — only the version-appropriate one is set.
func primaryModeSetting(pdnsVersion string) string {
	if pdnsVersion < "45" {
		return "master=yes"
	}
	return "primary=yes"
}

// skipIfPDNSBelow40 skips AXFR tests on PowerDNS < 4.0: AXFR-out via the remote-backend `list`
// method is not supported on the legacy PowerDNS 3.4 protocol (only the bracketing SOA is sent).
func skipIfPDNSBelow40(t *testing.T) {
	t.Helper()
	if v := getenvT("PDNS_VERSION", "50"); v < "40" {
		t.Skipf("AXFR-out via the remote-backend list method needs PowerDNS 4.0+; PDNS %s uses the legacy protocol", v)
	}
}

func TestPDNSAXFR(t *testing.T) {
	defer recoverPanicsT(t)
	skipIfPDNSBelow40(t)
	// ETCD
	etcd, err := startETCD(t)
	fatalOnErr(t, "start ETCD container", err)
	defer etcd.Terminate()
	Logf(t, "ETCD endpoint (2379): %s", etcd.Endpoint)
	// PDNS-ETCD3
	sleepT(t, 1*time.Second)
	pe3 := startPE3(t, etcd.Endpoint, "", "-log-level=10;data.values=2", "-pdns-version="+getenvT("PDNS_VERSION", fmt.Sprintf("%d", defaultPdnsVersion))[:1])
	defer pe3.Terminate()
	Logf(t, "PDNS-ETCD3 endpoint: %s", pe3.HttpAddress)
	err = waitFor(t, "PE3 ready", func() bool { return status.serving }, 10*time.Millisecond, 30*time.Second)
	fatalOnErr(t, "wait for PE3 ready", err)
	sleepT(t, 1*time.Second)
	// seed a small zone example.net. (key prefix net.example): SOA + NS (apex) + A records
	put := func(key, value string) clientv3.Op {
		return putOp(pe3.Prefix+key, value)
	}
	rev := txnT(t,
		put("-defaults-", `{ttl: "1h"}`),
		put("-defaults-/SOA", "---\nrefresh: 1h\nretry: 30m\nexpire: 604800\nneg-ttl: 10m\nprimary: ns1\nmail: horst.master\n"),
		put("net.example/-options-/A", `{"ip-prefix": [192, 0, 2]}`),
		put("net.example/SOA", `{}`),
		put("net.example/NS#first", `="ns1"`),
		put("net.example/ns1/A", `=2`), // ns1.example.net. A 192.0.2.2
		put("net.example/www/A", `=1`), // www.example.net. A 192.0.2.1
	)
	waitForRevision(t, rev, "zone data loaded")
	// PDNS with AXFR-OUT enabled (allow the test client to transfer) + primary mode
	// (version-appropriate master/primary — PDNS 5.0 FATALs on the removed "master" alias).
	pdns, err := startPDNS(t, map[string]string{
		"allow-axfr-ips=0.0.0.0/0,::/0":                   "34",
		primaryModeSetting(getenvT("PDNS_VERSION", "50")): "34",
	})
	fatalOnErr(t, "start PDNS container", err)
	defer pdns.Terminate()
	Logf(t, "PDNS endpoint: %s", pdns.Endpoint)
	// perform the AXFR
	zone := "example.net."
	tr := &dns.Transfer{
		DialTimeout: 10 * time.Second,
		ReadTimeout: 10 * time.Second,
	}
	m := new(dns.Msg)
	m.SetAxfr(zone)
	ch, err := tr.In(m, pdns.Endpoint)
	fatalOnErr(t, "start AXFR", err)
	var rrs []dns.RR
	for env := range ch {
		if env.Error != nil {
			Fatalf(t, "AXFR envelope error: %s", env.Error)
		}
		rrs = append(rrs, env.RR...)
	}
	Logf(t, "AXFR transferred %d RRs", len(rrs))
	for _, rr := range rrs {
		Logf(t, "  %s", rr)
	}
	// assertions
	if len(rrs) < 2 {
		Fatalf(t, "AXFR returned too few records: %d", len(rrs))
	}
	if _, ok := rrs[0].(*dns.SOA); !ok {
		Errorf(t, "AXFR must start with SOA, got %s", rrs[0])
	}
	if _, ok := rrs[len(rrs)-1].(*dns.SOA); !ok {
		Errorf(t, "AXFR must end with SOA, got %s", rrs[len(rrs)-1])
	}
	var soaCount, nsCount, aCount int
	var foundWWW, foundNS1 bool
	for _, rr := range rrs {
		switch v := rr.(type) {
		case *dns.SOA:
			soaCount++
			if v.Ns != "ns1.example.net." {
				Errorf(t, "SOA primary mismatch: %q", v.Ns)
			}
		case *dns.NS:
			nsCount++
			if v.Ns != "ns1.example.net." {
				Errorf(t, "unexpected NS target: %q", v.Ns)
			}
		case *dns.A:
			aCount++
			switch v.Hdr.Name {
			case "www.example.net.":
				foundWWW = true
				if v.A.String() != "192.0.2.1" {
					Errorf(t, "www A mismatch: %s", v.A)
				}
			case "ns1.example.net.":
				foundNS1 = true
				if v.A.String() != "192.0.2.2" {
					Errorf(t, "ns1 A mismatch: %s", v.A)
				}
			}
		}
	}
	if soaCount < 2 {
		Errorf(t, "expected at least 2 SOA records (start+end), got %d", soaCount)
	}
	if nsCount < 1 {
		Errorf(t, "expected at least one NS record, got %d", nsCount)
	}
	if !foundWWW {
		Errorf(t, "expected www.example.net. A 192.0.2.1 in transfer (saw %d A records)", aCount)
	}
	if !foundNS1 {
		Errorf(t, "expected ns1.example.net. A 192.0.2.2 in transfer (saw %d A records)", aCount)
	}
}

// TestPDNSAXFRPresigned proves a PRE-SIGNED DNSSEC zone transfers over AXFR with its
// DNSSEC records intact: the AXFR envelope must contain the stored *dns.DNSKEY and
// *dns.RRSIG records (plus the bracketing SOA), and the served SOA serial must equal
// the pinned X-PE3-FIXED-SERIAL. It is a close variant of TestPDNSAXFR.
//
// Naming: the test MUST be prefixed "TestPDNS" so CI's `-run PDNS` matrix job runs it
// across the PDNS-version matrix; an unprefixed name would be silently skipped.
//
// What pe3 does (and what this test exercises): pe3 does NOT sign anything. The signed
// records (DNSKEY/RRSIG/NSEC) are stored in etcd as ordinary plain-string entries under
// their name + qtype; these qtypes are not object-supported and have no plain-string
// parser, so their content is passed through to PowerDNS VERBATIM (see
// doc/ETCD-structure.md "Pre-signed DNSSEC" and data.go::processValuesEntry, which calls
// SetContent on the raw string for unparsed qtypes). The PRESIGNED=1 metadata tells
// PowerDNS to serve those RRSIG/NSEC/DNSKEY records as-is instead of signing on the fly,
// and X-PE3-FIXED-SERIAL pins the SOA serial so it matches the value a real signer would
// have baked into RRSIG(SOA).
//
// ASSUMPTIONS (validated by CI; documented per task requirements):
//  1. The DNSSEC RDATA strings below are cryptographically DUMMY but
//     SYNTACTICALLY VALID presentation format. This is sufficient because the task only
//     requires that the records transfer intact — cryptographic validation by a secondary
//     is NOT required. pe3 stores/serves them verbatim and PowerDNS forwards them as-is
//     over AXFR; no signature is verified anywhere in this path. (The strings were
//     verified to round-trip through miekg/dns — the same parser the test client uses —
//     into *dns.DNSKEY / *dns.RRSIG / *dns.NSEC.)
//  2. For the REMOTE backend, marking a zone presigned is done entirely via the
//     getDomainMetadata passthrough: PRESIGNED=1 is returned to PowerDNS, which then
//     serves backend-supplied DNSSEC records. No server-level "dnssec" pdns.conf option
//     gates presigned AXFR for the remote backend (presigned-ness is per-zone metadata),
//     so none is added. Metadata caching is already disabled in startPDNS, so PowerDNS
//     consults the PRESIGNED metadata fresh.
//  3. All DNSSEC RDATA strings here begin with an alphanumeric character (a digit for
//     DNSKEY, a letter for RRSIG/NSEC), so they are safe as plain strings without the
//     backtick marker (per the ETCD-structure warning about non-alphanumeric leading
//     characters).
//
// First failure mode if an assumption is wrong (what CI tells us): if PowerDNS needs more
// than PRESIGNED metadata to serve a presigned zone over AXFR (e.g. it drops the
// RRSIG/DNSKEY records), the DNSKEY/RRSIG assertions below fail with a clear message.
func TestPDNSAXFRPresigned(t *testing.T) {
	defer recoverPanicsT(t)
	skipIfPDNSBelow40(t)
	// The serial baked into RRSIG(SOA) by a (hypothetical) signer; pe3 must serve exactly
	// this as the SOA serial via X-PE3-FIXED-SERIAL so the answer stays self-consistent.
	const fixedSerial uint32 = 2026061601
	// ETCD
	etcd, err := startETCD(t)
	fatalOnErr(t, "start ETCD container", err)
	defer etcd.Terminate()
	Logf(t, "ETCD endpoint (2379): %s", etcd.Endpoint)
	// PDNS-ETCD3
	sleepT(t, 1*time.Second)
	pe3 := startPE3(t, etcd.Endpoint, "", "-log-level=10;data.values=2", "-pdns-version="+getenvT("PDNS_VERSION", fmt.Sprintf("%d", defaultPdnsVersion))[:1])
	defer pe3.Terminate()
	Logf(t, "PDNS-ETCD3 endpoint: %s", pe3.HttpAddress)
	err = waitFor(t, "PE3 ready", func() bool { return status.serving }, 10*time.Millisecond, 30*time.Second)
	fatalOnErr(t, "wait for PE3 ready", err)
	sleepT(t, 1*time.Second)
	// seed zone example.net. (same shape as TestPDNSAXFR) PLUS pre-signed DNSSEC records
	// stored verbatim, plus PRESIGNED + X-PE3-FIXED-SERIAL metadata.
	put := func(key, value string) clientv3.Op {
		return putOp(pe3.Prefix+key, value)
	}
	// Dummy-but-syntactically-valid DNSSEC RDATA (presentation format, content only — the
	// owner/class/type/ttl come from the etcd key + default ttl). Verified to round-trip
	// through miekg/dns into the corresponding *dns.* types.
	const (
		// DNSKEY: flags=257 (KSK) protocol=3 algorithm=8 (RSASHA256) publickey(base64)
		dnskeyRDATA = "257 3 8 AwEAAcKvAYr0Z8h3hZ3cQv0p9Wb0nKZ3sZ1jKpV3pQ8mC2x1aXQ9pZ4dN0kT8xY7vL5wRb2cF0aG6hJ4mN8pQ2sT5uW7yZ0bD3eF6gH8iJ1kL3mN5oP7qR9sT2uV4wX6yZ8aB0cD2eF4gH6iJ8kL0mN2oP4qR6sT8uV0w"
		// RRSIG covering SOA: type-covered algo labels orig-ttl expiration inception keytag signer signature(base64)
		rrsigSOA = "SOA 8 2 3600 20270101000000 20260101000000 12345 example.net. abcdefABCDEF0123456789+/aGdHjKlMnOpQrStUvWxYzAbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcdefABCDEFGHIJKLMNOPqrstuvwxYZabcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQ="
		// RRSIG covering the apex DNSKEY RRset
		rrsigDNSKEY = "DNSKEY 8 2 3600 20270101000000 20260101000000 12345 example.net. ZZZZdefABCDEF0123456789+/aGdHjKlMnOpQrStUvWxYzAbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcdefABCDEFGHIJKLMNOPqrstuvwxYZabcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQ="
		// RRSIG covering www's A RRset (labels=3 for www.example.net.)
		rrsigA = "A 8 3 3600 20270101000000 20260101000000 12345 example.net. YYYYdefABCDEF0123456789+/aGdHjKlMnOpQrStUvWxYzAbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcdefABCDEFGHIJKLMNOPqrstuvwxYZabcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQ="
		// NSEC at the apex (next name + covered types)
		nsecApex = "www.example.net. A NS SOA RRSIG NSEC DNSKEY"
	)
	rev := txnT(t,
		put("-defaults-", `{ttl: "1h"}`),
		put("-defaults-/SOA", "---\nrefresh: 1h\nretry: 30m\nexpire: 604800\nneg-ttl: 10m\nprimary: ns1\nmail: horst.master\n"),
		put("net.example/-options-/A", `{"ip-prefix": [192, 0, 2]}`),
		put("net.example/SOA", `{}`),
		put("net.example/NS#first", `="ns1"`),
		put("net.example/ns1/A", `=2`), // ns1.example.net. A 192.0.2.2
		put("net.example/www/A", `=1`), // www.example.net. A 192.0.2.1
		// pre-signed DNSSEC records (stored verbatim; not object-supported, no parser).
		put("net.example/DNSKEY", dnskeyRDATA),       // example.net. DNSKEY
		put("net.example/RRSIG#soa", rrsigSOA),       // example.net. RRSIG (SOA)
		put("net.example/RRSIG#dnskey", rrsigDNSKEY), // example.net. RRSIG (DNSKEY)
		put("net.example/NSEC", nsecApex),            // example.net. NSEC
		put("net.example/www/RRSIG", rrsigA),         // www.example.net. RRSIG (A)
		// metadata: mark the zone presigned and pin the SOA serial to the signer's value.
		put("net.example/"+metadataKey+keySeparator+"PRESIGNED#1", "1"),
		put("net.example/"+metadataKey+keySeparator+MetaFixedSerial+"#1", strconv.FormatUint(uint64(fixedSerial), 10)),
	)
	waitForRevision(t, rev, "presigned zone data loaded")
	// PDNS primary mode with AXFR-OUT enabled (same gating as TestPDNSAXFR). PRESIGNED is
	// delivered via the getdomainmetadata passthrough — no extra server setting needed.
	pdns, err := startPDNS(t, map[string]string{
		"allow-axfr-ips=0.0.0.0/0,::/0":                   "34",
		primaryModeSetting(getenvT("PDNS_VERSION", "50")): "34",
	})
	fatalOnErr(t, "start PDNS container", err)
	defer pdns.Terminate()
	Logf(t, "PDNS endpoint: %s", pdns.Endpoint)
	// perform the AXFR
	zone := "example.net."
	tr := &dns.Transfer{
		DialTimeout: 10 * time.Second,
		ReadTimeout: 10 * time.Second,
	}
	m := new(dns.Msg)
	m.SetAxfr(zone)
	ch, err := tr.In(m, pdns.Endpoint)
	fatalOnErr(t, "start AXFR", err)
	var rrs []dns.RR
	for env := range ch {
		if env.Error != nil {
			Fatalf(t, "AXFR envelope error: %s", env.Error)
		}
		rrs = append(rrs, env.RR...)
	}
	Logf(t, "AXFR transferred %d RRs", len(rrs))
	for _, rr := range rrs {
		Logf(t, "  %s", rr)
	}
	// assertions
	if len(rrs) < 2 {
		Fatalf(t, "AXFR returned too few records: %d", len(rrs))
	}
	if _, ok := rrs[0].(*dns.SOA); !ok {
		Errorf(t, "AXFR must start with SOA, got %s", rrs[0])
	}
	if _, ok := rrs[len(rrs)-1].(*dns.SOA); !ok {
		Errorf(t, "AXFR must end with SOA, got %s", rrs[len(rrs)-1])
	}
	var soaCount, dnskeyCount, rrsigCount int
	var serialOK bool
	for _, rr := range rrs {
		switch v := rr.(type) {
		case *dns.SOA:
			soaCount++
			if v.Serial == fixedSerial {
				serialOK = true
			} else {
				Errorf(t, "SOA serial mismatch: got %d, want pinned X-PE3-FIXED-SERIAL %d", v.Serial, fixedSerial)
			}
		case *dns.DNSKEY:
			dnskeyCount++
		case *dns.RRSIG:
			rrsigCount++
		}
	}
	if soaCount < 2 {
		Errorf(t, "expected at least 2 SOA records (start+end), got %d", soaCount)
	}
	if !serialOK {
		Errorf(t, "no SOA carried the pinned serial %d", fixedSerial)
	}
	if dnskeyCount < 1 {
		Errorf(t, "expected at least one DNSKEY record in presigned AXFR, got %d", dnskeyCount)
	}
	if rrsigCount < 1 {
		Errorf(t, "expected at least one RRSIG record in presigned AXFR, got %d", rrsigCount)
	}
}

// TestPDNSAXFRTSIG proves TSIG-secured AXFR end-to-end: a TSIG-signed transfer
// SUCCEEDS, and an UNSIGNED transfer is REFUSED when access is gated only by TSIG
// (no allow-axfr-ips). It is a close variant of TestPDNSAXFR.
//
// Naming: the test MUST be prefixed "TestPDNS" so CI's `-run PDNS` matrix job runs
// it across the PDNS-version matrix; an unprefixed name would be silently skipped.
//
// TSIG key-name consistency (the central correctness concern): the same FQDN string
// "axfrkey." is used in all THREE places that must agree —
//  1. the etcd key holding the secret:  <prefix>-tsig-/axfrkey.
//  2. the zone metadata value:          TSIG-ALLOW-AXFR = ["axfrkey."]
//  3. the dns client TsigSecret map key + SetTsig name: "axfrkey."
//
// miekg/dns requires the TsigSecret map key to be a canonical FQDN (lowercase, with
// trailing dot) — see dns.Transfer.TsigSecret docs — so "axfrkey." is mandatory on the
// client side; we mirror that dotted form everywhere for consistency.
//
// HEDGE / ASSUMPTION (CI validates): PowerDNS canonicalizes TSIG names as DNSNames and
// it is not 100%-certain from outside whether it sends the `getTSIGKey` `name` parameter
// (and looks up the etcd key) WITH or WITHOUT the trailing dot. To be robust against
// both, the secret is seeded into etcd under BOTH "axfrkey." and "axfrkey" (harmless —
// -tsig- entries are never stored in the tree nor affect any serial). If CI shows only
// one form is consulted, the other seed is simply unused.
func TestPDNSAXFRTSIG(t *testing.T) {
	defer recoverPanicsT(t)
	skipIfPDNSBelow40(t)
	// TSIG material: a fixed, valid HMAC-SHA256 secret (base64 of exactly 32 bytes).
	const (
		tsigKeyName = "axfrkey."                                     // canonical FQDN, used identically in all 3 places
		tsigAlgo    = "hmac-sha256"                                  // etcd/PDNS algorithm token (no trailing dot)
		tsigSecret  = "cGUzLWF4ZnItdHNpZy1zZWNyZXQtMzJieXRlcy1rZXk=" // base64 of 32 bytes
	)
	// ETCD
	etcd, err := startETCD(t)
	fatalOnErr(t, "start ETCD container", err)
	defer etcd.Terminate()
	Logf(t, "ETCD endpoint (2379): %s", etcd.Endpoint)
	// PDNS-ETCD3
	sleepT(t, 1*time.Second)
	pe3 := startPE3(t, etcd.Endpoint, "", "-log-level=10;data.values=2", "-pdns-version="+getenvT("PDNS_VERSION", fmt.Sprintf("%d", defaultPdnsVersion))[:1])
	defer pe3.Terminate()
	Logf(t, "PDNS-ETCD3 endpoint: %s", pe3.HttpAddress)
	err = waitFor(t, "PE3 ready", func() bool { return status.serving }, 10*time.Millisecond, 30*time.Second)
	fatalOnErr(t, "wait for PE3 ready", err)
	sleepT(t, 1*time.Second)
	// seed zone example.net. (same shape as TestPDNSAXFR) PLUS the TSIG key and the
	// TSIG-ALLOW-AXFR metadata gating AXFR by that key name.
	put := func(key, value string) clientv3.Op {
		return putOp(pe3.Prefix+key, value)
	}
	rev := txnT(t,
		put("-defaults-", `{ttl: "1h"}`),
		put("-defaults-/SOA", "---\nrefresh: 1h\nretry: 30m\nexpire: 604800\nneg-ttl: 10m\nprimary: ns1\nmail: horst.master\n"),
		put("net.example/-options-/A", `{"ip-prefix": [192, 0, 2]}`),
		put("net.example/SOA", `{}`),
		put("net.example/NS#first", `="ns1"`),
		put("net.example/ns1/A", `=2`), // ns1.example.net. A 192.0.2.2
		put("net.example/www/A", `=1`), // www.example.net. A 192.0.2.1
		// TSIG key: <prefix>-tsig-/<name> = "<algorithm> <base64-secret>"
		// (read on demand by getTSIGKey; never stored in the data tree). Seed both the
		// dotted and undotted name forms so the test is robust to PDNS canonicalization.
		put(tsigKey+keySeparator+tsigKeyName, tsigAlgo+" "+tsigSecret),                          // -tsig-/axfrkey.
		put(tsigKey+keySeparator+strings.TrimSuffix(tsigKeyName, "."), tsigAlgo+" "+tsigSecret), // -tsig-/axfrkey
		// zone metadata TSIG-ALLOW-AXFR (key form <zone>/-metadata-/<KEY>#<id>, value verbatim).
		put("net.example/"+metadataKey+keySeparator+"TSIG-ALLOW-AXFR#1", tsigKeyName), // = "axfrkey."
	)
	waitForRevision(t, rev, "zone + TSIG data loaded")
	// PDNS primary mode, AXFR gated by TSIG ONLY (deliberately NO allow-axfr-ips, so an
	// unsigned transfer must be refused; a TSIG-signed one is allowed via TSIG-ALLOW-AXFR).
	// Version-appropriate master/primary (PDNS 5.0 FATALs on the removed "master" alias).
	// remote-dnssec=yes is REQUIRED for TSIG: PowerDNS's remote backend gates getTSIGKey
	// behind the backend "dnssec" flag (remotebackend.cc: `if (!d_dnssec) return false;`),
	// so without it PowerDNS never calls getTSIGKey and denies the signed AXFR (NOTAUTH).
	pdns, err := startPDNS(t, map[string]string{
		primaryModeSetting(getenvT("PDNS_VERSION", "50")): "34",
		"remote-dnssec=yes": "34",
	})
	fatalOnErr(t, "start PDNS container", err)
	defer pdns.Terminate()
	Logf(t, "PDNS endpoint: %s", pdns.Endpoint)
	zone := "example.net."

	// --- Positive: TSIG-signed AXFR must SUCCEED ---
	t.Run("signed", func(t *testing.T) {
		tr := &dns.Transfer{
			DialTimeout: 10 * time.Second,
			ReadTimeout: 10 * time.Second,
			TsigSecret:  map[string]string{tsigKeyName: tsigSecret},
		}
		m := new(dns.Msg)
		m.SetAxfr(zone)
		m.SetTsig(tsigKeyName, dns.HmacSHA256, 300, time.Now().Unix())
		ch, err := tr.In(m, pdns.Endpoint)
		fatalOnErr(t, "start signed AXFR", err)
		var rrs []dns.RR
		for env := range ch {
			if env.Error != nil {
				Fatalf(t, "signed AXFR envelope error: %s", env.Error)
			}
			rrs = append(rrs, env.RR...)
		}
		Logf(t, "signed AXFR transferred %d RRs", len(rrs))
		for _, rr := range rrs {
			Logf(t, "  %s", rr)
		}
		// must be SOA-bracketed and contain the seeded records
		if len(rrs) < 2 {
			Fatalf(t, "signed AXFR returned too few records: %d", len(rrs))
		}
		if _, ok := rrs[0].(*dns.SOA); !ok {
			Errorf(t, "signed AXFR must start with SOA, got %s", rrs[0])
		}
		if _, ok := rrs[len(rrs)-1].(*dns.SOA); !ok {
			Errorf(t, "signed AXFR must end with SOA, got %s", rrs[len(rrs)-1])
		}
		var soaCount, nsCount int
		var foundWWW, foundNS1 bool
		for _, rr := range rrs {
			switch v := rr.(type) {
			case *dns.SOA:
				soaCount++
			case *dns.NS:
				nsCount++
				if v.Ns != "ns1.example.net." {
					Errorf(t, "unexpected NS target: %q", v.Ns)
				}
			case *dns.A:
				switch v.Hdr.Name {
				case "www.example.net.":
					foundWWW = v.A.String() == "192.0.2.1"
				case "ns1.example.net.":
					foundNS1 = v.A.String() == "192.0.2.2"
				}
			}
		}
		if soaCount < 2 {
			Errorf(t, "expected at least 2 SOA records (start+end), got %d", soaCount)
		}
		if nsCount < 1 {
			Errorf(t, "expected at least one NS record, got %d", nsCount)
		}
		if !foundWWW {
			Errorf(t, "expected www.example.net. A 192.0.2.1 in signed transfer")
		}
		if !foundNS1 {
			Errorf(t, "expected ns1.example.net. A 192.0.2.2 in signed transfer")
		}
	})

	// --- Negative: UNSIGNED AXFR must be REFUSED (the meaningful test) ---
	t.Run("unsigned", func(t *testing.T) {
		tr := &dns.Transfer{
			DialTimeout: 10 * time.Second,
			ReadTimeout: 10 * time.Second,
		}
		m := new(dns.Msg)
		m.SetAxfr(zone)
		ch, err := tr.In(m, pdns.Endpoint)
		if err != nil {
			// connection-level refusal already counts as "not succeeded"
			Logf(t, "unsigned AXFR refused at start (expected): %s", err)
			return
		}
		// drain the channel: a refused/unauthorized AXFR yields an envelope error
		// and/or no usable zone data (notably no closing SOA). Any of these means
		// "did not succeed".
		var rrs []dns.RR
		var sawError bool
		for env := range ch {
			if env.Error != nil {
				sawError = true
				Logf(t, "unsigned AXFR envelope error (expected): %s", env.Error)
				continue
			}
			rrs = append(rrs, env.RR...)
		}
		Logf(t, "unsigned AXFR yielded %d RRs (sawError=%v)", len(rrs), sawError)
		// Success would be a complete, SOA-bracketed transfer with the zone records.
		// Assert we did NOT get that.
		soaBracketed := len(rrs) >= 2
		if soaBracketed {
			_, firstSOA := rrs[0].(*dns.SOA)
			_, lastSOA := rrs[len(rrs)-1].(*dns.SOA)
			soaBracketed = firstSOA && lastSOA
		}
		if !sawError && soaBracketed {
			Errorf(t, "unsigned AXFR unexpectedly SUCCEEDED (%d RRs, SOA-bracketed); TSIG gating not enforced", len(rrs))
		}
	})
}

func TestUnixListener(t *testing.T) {
	t.Skip("not implemented yet")
}

func TestHttpListener(t *testing.T) {
	t.Skip("not implemented yet")
}

func TestParallelRequests(t *testing.T) {
	defer recoverPanicsT(t)
	// ETCD
	etcd, err := startETCD(t)
	fatalOnErr(t, "start ETCD container", err)
	defer etcd.Terminate()
	Logf(t, "ETCD endpoint (2379): %s", etcd.Endpoint)
	// PDNS-ETCD3
	pe3 := startPE3(t, etcd.Endpoint, "", "-pdns-version="+getenvT("PDNS_VERSION", fmt.Sprintf("%d", defaultPdnsVersion))[:1])
	defer pe3.Terminate()
	Logf(t, "PDNS-ETCD3 endpoint: %s", pe3.HttpAddress)
	err = waitFor(t, "PE3 ready", func() bool { return status.serving }, 10*time.Millisecond, 30*time.Second)
	fatalOnErr(t, "wait for PE3 ready", err)
	rev, _ := basicDataTxn(t, pe3.Prefix)
	waitForRevision(t, rev, "basic data loaded")
	pdns := make([]pdnsInfo, 0)
	nCPU := min(runtime.NumCPU(), 8) // limit to a useful number of instances, because it is more flaky the more CPUs are available (see PR #4)
	t.Logf("Using %d parallel PDNS (single-threaded) instances", nCPU+1)
	for i := 0; i <= nCPU; i++ {
		pdnsN, err := startPDNS(t, map[string]string{
			"receiver-threads=1":      "34",
			"distributor-threads=1":   "34",
			"max-tcp-connections=200": "34",
		})
		fatalOnErr(t, fmt.Sprintf("start PDNS#%d container", i+1), err)
		defer pdnsN.Terminate()
		Logf(t, "PDNS#%d endpoint: %s", i+1, pdnsN.Endpoint)
		pdns = append(pdns, pdnsN)
	}
	wg := new(WaitGroup).Init()
	queryCount := struct {
		count atomic.Int32
		par   struct{ cur, max atomic.Int32 }
		dur   struct{ min, max atomic.Int64 }
	}{}
	runs := 3
	since := time.Now()
	for i, pdns := range pdns {
		Logf(t, "starting PDNS#%d queries", i+1)
		type pdnsP struct {
			n            int
			pdnsEndpoint string
		}
		wg.Go(fmt.Sprintf("PDNS#%d queries", i+1), func(args ...any) {
			p := args[0].(pdnsP)
			for i := 0; i < runs; i++ {
				since := time.Now()
				for j, qs := range []querySpecT{
					querySpec("example.net.", dns.TypeSOA, dns.Msg{Answer: []dns.RR{
						&dns.SOA{Hdr: dns.RR_Header{Name: "example.net.", Rrtype: dns.TypeSOA, Ttl: 3600},
							Ns: "ns1.example.net.", Mbox: "horst\\.master.example.net.", Serial: uint32(rev), Refresh: 3600, Retry: 1800, Expire: 604800, Minttl: 600},
					}}),
					querySpec("example.net.", dns.TypeNS, dns.Msg{Answer: []dns.RR{
						&dns.NS{Ns: "ns1.example.net."},
					}, Extra: []dns.RR{
						&dns.A{Hdr: dns.RR_Header{Name: "ns1.example.net."}, A: []byte{192, 0, 2, 2}},
						&dns.AAAA{Hdr: dns.RR_Header{Name: "ns1.example.net."}, AAAA: net.ParseIP("2001:db8::2")},
					}}, map[string]Condition{`->Extra`: SliceContains{All: false, Only: true}}),
					querySpec("ns1.example.net.", dns.TypeA, dns.Msg{Answer: []dns.RR{
						&dns.A{Hdr: dns.RR_Header{Name: "ns1.example.net."}, A: []byte{192, 0, 2, 2}},
					}}),
					querySpec("2.2.0.192.in-addr.arpa.", dns.TypePTR, dns.Msg{Answer: []dns.RR{
						&dns.PTR{Ptr: "ns1.example.net."},
					}}),
					querySpec("2.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa.", dns.TypePTR, dns.Msg{Answer: []dns.RR{
						&dns.PTR{Ptr: "ns1.example.net."},
					}}),
				} {
					ns := []int{p.n, i + 1, j + 1}
					Logf(t, "PDNS#%v: starting query test (%s/%s)", ns, qs.name, dns.TypeToString[qs.qtype])
					queryCount.count.Add(1)
					cur := queryCount.par.cur.Add(1)
					queryCount.par.max.CompareAndSwap(cur-1, cur)
					dur := int64(QueryTest(t, p.pdnsEndpoint, qs, 10*time.Second, true))
					queryCount.dur.min.CompareAndSwap(0, dur) // only done once (on first result)
					queryCount.dur.min.CompareAndSwap(queryCount.dur.min.Load(), min(dur, queryCount.dur.min.Load()))
					queryCount.dur.max.CompareAndSwap(queryCount.dur.max.Load(), max(dur, queryCount.dur.max.Load()))
					queryCount.par.cur.Add(-1)
					Logf(t, "PDNS#%v: finished query test (%s/%s) in %s", ns, qs.name, dns.TypeToString[qs.qtype], time.Duration(dur))
				}
				dur := time.Since(since)
				Logf(t, "PDNS#%d run %d finished in %s", p.n, i+1, dur)
			}
		}, pdnsP{i + 1, pdns.Endpoint})
	}
	wg.Wait()
	overall := time.Since(since)
	Logf(t, "finished (queries: %d) (max parallel readers: %d, requests: %d, queries: %d) (duration min: %s, max: %s, overall: %s, run average: %s)",
		queryCount.count.Load(),
		dataRoot.readers.max.Load(), requestsCount.max.Load(), queryCount.par.max.Load(),
		time.Duration(queryCount.dur.min.Load()), time.Duration(queryCount.dur.max.Load()), overall, time.Duration(int64(overall)/int64(runs)))
	var pr func(*dataNode)
	pr = func(data *dataNode) {
		Logf(t, "-- %s: %d", data.getName().asKey(false), data.readers.max.Load())
		for _, child := range data.children {
			pr(child)
		}
	}
	pr(dataRoot)
	if dataRoot.readers.max.Load() < int32(nCPU)/2 {
		t.Errorf("too less parallel requests (CPUs: %d, max parallel requests: %d", nCPU, dataRoot.readers.max.Load())
	}
}

func selectByVersion[T any](version string, options map[string]T) T {
	var selectedVersion string
	for ver := range options {
		if version >= ver && ver > selectedVersion {
			selectedVersion = ver
		}
	}
	if selectedVersion == "" {
		var t T
		return t
	}
	return options[selectedVersion]
}

func execCommand(t *testing.T, ct testcontainers.Container, cmd []string) int {
	Logf(t, "executing command: %v", cmd)
	code, reader, err := ct.Exec(context.Background(), cmd)
	fatalOnErr(t, "exec failed", err)
	buf := new(strings.Builder)
	_, err = io.Copy(buf, reader)
	fatalOnErr(t, "copy command output", err)
	if buf.Len() == 0 {
		Logf(t, "command %v exited with code %d, no output", cmd, code)
	} else {
		Logf(t, "command %v exited with code %d, output:\n%s", cmd, code, buf.String())
	}
	return code
}

func TestMetadata(t *testing.T) {
	defer recoverPanicsT(t)
	// ETCD
	etcd, err := startETCD(t)
	fatalOnErr(t, "start ETCD container", err)
	defer etcd.Terminate()
	Logf(t, "ETCD endpoint (2379): %s", etcd.Endpoint)
	// PDNS-ETCD3
	pe3 := startPE3(t, etcd.Endpoint, "DNS/", "-pdns-version="+getenvT("PDNS_VERSION", fmt.Sprintf("%d", defaultPdnsVersion))[:1], "-log-level=10;data.values=2")
	defer pe3.Terminate()
	Logf(t, "PDNS-ETCD3 endpoint: %s", pe3.HttpAddress)
	err = waitFor(t, "PE3 ready", func() bool { return status.serving }, 10*time.Millisecond, 30*time.Second)
	fatalOnErr(t, "wait for PE3 ready", err)
	rev, _ := basicDataTxn(t, pe3.Prefix)
	waitForRevision(t, rev, "basic data loaded")
	// PDNS
	pdns, err := startPDNS(t, nil)
	fatalOnErr(t, "start PDNS container", err)
	defer pdns.Terminate()
	Logf(t, "PDNS endpoint: %s", pdns.Endpoint)
	if !checkRun(t, "set", func(t *testing.T, _ struct{}) (any, error) {
		domain, tld, key := "example", "net", "X-PE3-TEST"
		fqdn := fmt.Sprintf("%s.%s", domain, tld)
		if code := execCommand(t, pdns.Container, selectByVersion(pdns.Version, map[string][]string{
			"34": {"pdnssec", "set-meta", fqdn, key, "a", "b"},
			"40": {"pdnsutil", "set-meta", fqdn, key, "a", "b"},
			"50": {"pdnsutil", "metadata", "set", fqdn, key, "a", "b"},
		})); code != 0 {
			return nil, fmt.Errorf("command returned code %d", code)
		}
		return dataRoot.children[tld].children[domain].metadata[key], nil
	}, struct{}{}, ve[any]{v: SliceContains{Ordered: false, All: true, Only: true, Elements: []any{"a", "b"}}}, false) {
		Fatalf(t, "set failed")
	}
	checkRun(t, "add", func(t *testing.T, _ struct{}) (any, error) {
		if pdns.Version < "41" {
			t.Skip("skipping add, present only as of PDNS 4.1")
		}
		domain, tld, key := "example", "net", "X-PE3-TEST"
		fqdn := fmt.Sprintf("%s.%s", domain, tld)
		if code := execCommand(t, pdns.Container, selectByVersion(pdns.Version, map[string][]string{
			"34": {"pdnssec", "add-meta", fqdn, key, "c", "d"},
			"40": {"pdnsutil", "add-meta", fqdn, key, "c", "d"},
			"50": {"pdnsutil", "metadata", "add", fqdn, key, "c", "d"},
		})); code != 0 {
			return nil, fmt.Errorf("command returned code %d", code)
		}
		return dataRoot.children[tld].children[domain].metadata[key], nil
	}, struct{}{}, ve[any]{v: SliceContains{Ordered: false, All: true, Only: true, Elements: []any{"a", "b", "c", "d"}}}, false)
	if !checkRun(t, "replace", func(t *testing.T, _ struct{}) (any, error) {
		domain, tld, key := "example", "net", "X-PE3-TEST"
		fqdn := fmt.Sprintf("%s.%s", domain, tld)
		if code := execCommand(t, pdns.Container, selectByVersion(pdns.Version, map[string][]string{
			"34": {"pdnssec", "set-meta", fqdn, key, "x", "y"},
			"40": {"pdnsutil", "set-meta", fqdn, key, "x", "y"},
			"50": {"pdnsutil", "metadata", "set", fqdn, key, "x", "y"},
		})); code != 0 {
			return nil, fmt.Errorf("command returned code %d", code)
		}
		return dataRoot.children[tld].children[domain].metadata[key], nil
	}, struct{}{}, ve[any]{v: SliceContains{Ordered: false, All: true, Only: true, Elements: []any{"x", "y"}}}, false) {
		Fatalf(t, "replace failed")
	}
	checkRun(t, "get", func(t *testing.T, _ struct{}) (any, error) {
		domain, tld, key := "example", "net", "X-PE3-TEST"
		fqdn := fmt.Sprintf("%s.%s", domain, tld)
		if code := execCommand(t, pdns.Container, selectByVersion(pdns.Version, map[string][]string{
			"34": {"pdnssec", "get-meta", fqdn, key},
			"40": {"pdnsutil", "get-meta", fqdn, key},
			"50": {"pdnsutil", "metadata", "get", fqdn, key},
		})); code != 0 {
			return nil, fmt.Errorf("command returned code %d", code)
		}
		return dataRoot.children[tld].children[domain].metadata[key], nil
	}, struct{}{}, ve[any]{v: SliceContains{Ordered: false, All: true, Only: true, Elements: []any{"x", "y"}}}, true)
}

// containerIPOnNetwork returns the container's IP address on the named docker network.
func containerIPOnNetwork(t *testing.T, ct testcontainers.Container, netName string) string {
	t.Helper()
	ins, err := ct.Inspect(context.Background())
	fatalOnErr(t, "inspect container", err)
	ep, ok := ins.NetworkSettings.Networks[netName]
	if !ok || ep == nil {
		Fatalf(t, "container has no endpoint on network %q", netName)
	}
	return ep.IPAddress
}

// startBindSecondary starts an ISC BIND9 container configured as a secondary (slave) for
// `zone`, transferring from `primaryIP` over the shared docker network `netName`. The image's
// default CMD logs to a file, so override it with `-g` (foreground + log to stderr) so
// testcontainers can wait on / surface the logs.
func startBindSecondary(t *testing.T, netName, primaryIP, zone string) (*ctInfo, error) {
	t.Helper()
	zoneName := strings.TrimSuffix(zone, ".")
	namedConf := fmt.Sprintf(`options {
    directory "/var/cache/bind";
    recursion no;
    dnssec-validation no;
    listen-on { any; };
    listen-on-v6 { none; };
    allow-query { any; };
};
zone "%s" {
    type secondary;
    primaries { %s; };
    file "%s.db";
    allow-notify { %s; };
};
`, zoneName, primaryIP, zoneName, primaryIP)
	return startContainer(t, testcontainers.ContainerRequest{
		Image:          "internetsystemsconsortium/bind9:9.20",
		Cmd:            []string{"-g", "-c", "/etc/bind/named.conf"}, // -g: foreground + log to stderr
		Networks:       []string{netName},
		NetworkAliases: map[string][]string{netName: {"secondary"}},
		ExposedPorts:   []string{"53/tcp"},
		LogConsumerCfg: &testcontainers.LogConsumerConfig{Consumers: []testcontainers.LogConsumer{CtLogger{t, "BIND"}}},
		Files: []testcontainers.ContainerFile{
			{Reader: strings.NewReader(namedConf), ContainerFilePath: "/etc/bind/named.conf", FileMode: 0o644},
		},
		WaitingFor: wait.ForLog("running").WithStartupTimeout(60 * time.Second),
	}, "53/tcp")
}

// TestPDNSAXFRSecondary spins up a REAL secondary DNS server (ISC BIND9) in its own
// container and verifies the full primary→secondary flow over a shared docker network:
// (1) BIND transfers the zone from PowerDNS+pe3 via AXFR on startup and serves it, and
// (2) after the zone changes in etcd, the secondary picks up the update (via NOTIFY —
// pdns_control notify-host — and/or the SOA refresh).
func TestPDNSAXFRSecondary(t *testing.T) {
	defer recoverPanicsT(t)
	skipIfPDNSBelow40(t)
	ctx := context.Background()
	// shared network so the primary (PowerDNS) and the secondary (BIND) can reach each other
	nw, err := network.New(ctx)
	fatalOnErr(t, "create docker network", err)
	defer func() { _ = nw.Remove(ctx) }()
	netName := nw.Name

	etcd, err := startETCD(t)
	fatalOnErr(t, "start ETCD container", err)
	defer etcd.Terminate()
	sleepT(t, 1*time.Second)
	pe3 := startPE3(t, etcd.Endpoint, "", "-log-level=10;data.values=2", "-pdns-version="+getenvT("PDNS_VERSION", fmt.Sprintf("%d", defaultPdnsVersion))[:1])
	defer pe3.Terminate()
	fatalOnErr(t, "wait for PE3 ready", waitFor(t, "PE3 ready", func() bool { return status.serving }, 10*time.Millisecond, 30*time.Second))
	sleepT(t, 1*time.Second)

	// seed example.net. with a short SOA refresh so the secondary re-checks the serial quickly
	put := func(key, value string) clientv3.Op { return putOp(pe3.Prefix+key, value) }
	rev := txnT(t,
		put("-defaults-", `{ttl: "1h"}`),
		put("-defaults-/SOA", "---\nrefresh: 10s\nretry: 10s\nexpire: 604800\nneg-ttl: 10m\nprimary: ns1\nmail: horst.master\n"),
		put("net.example/-options-/A", `{"ip-prefix": [192, 0, 2]}`),
		put("net.example/SOA", `{}`),
		put("net.example/NS#first", `="ns1"`),
		put("net.example/ns1/A", `=2`), // ns1.example.net. A 192.0.2.2
		put("net.example/www/A", `=1`), // www.example.net. A 192.0.2.1
	)
	waitForRevision(t, rev, "zone data loaded")

	// primary: PowerDNS + pe3, AXFR allowed, primary mode, joined to the shared network
	pdns, err := startPDNS(t, map[string]string{
		"allow-axfr-ips=0.0.0.0/0,::/0":                   "34",
		primaryModeSetting(getenvT("PDNS_VERSION", "50")): "34",
	}, map[string][]string{netName: {"primary"}})
	fatalOnErr(t, "start PDNS container", err)
	defer pdns.Terminate()
	primaryIP := containerIPOnNetwork(t, pdns.Container, netName)
	Logf(t, "primary (PowerDNS) IP on %s: %s", netName, primaryIP)

	// secondary: BIND9 slaving example.net. from the primary
	bind, err := startBindSecondary(t, netName, primaryIP, "example.net.")
	fatalOnErr(t, "start BIND secondary", err)
	defer bind.Terminate()
	Logf(t, "secondary (BIND) endpoint: %s", bind.Endpoint)

	queryA := func(name string) (*dns.Msg, error) {
		m := new(dns.Msg)
		m.SetQuestion(name, dns.TypeA)
		c := &dns.Client{Net: "tcp", Timeout: 5 * time.Second}
		r, _, e := c.Exchange(m, bind.Endpoint)
		return r, e
	}

	// (1) initial AXFR-in: poll the secondary until it serves the transferred zone
	fatalOnErr(t, "secondary serves zone after initial AXFR",
		waitFor(t, "secondary served www.example.net after AXFR", func() bool {
			r, e := queryA("www.example.net.")
			return e == nil && r.Rcode == dns.RcodeSuccess && len(r.Answer) > 0
		}, 500*time.Millisecond, 30*time.Second))
	r, e := queryA("www.example.net.")
	fatalOnErr(t, "query secondary for www", e)
	if a, ok := r.Answer[0].(*dns.A); !ok || a.A.String() != "192.0.2.1" {
		Errorf(t, "secondary served wrong A for www.example.net: %v", r.Answer)
	} else {
		Logf(t, "secondary correctly serves the transferred zone (www.example.net. A %s)", a.A)
	}

	// (2) update propagation: add a record (bumps the serial), notify the secondary, expect re-transfer
	rev2 := txnT(t, put("net.example/www2/A", `=3`)) // www2.example.net. A 192.0.2.3
	waitForRevision(t, rev2, "updated zone data loaded")
	secondaryIP := containerIPOnNetwork(t, bind.Container, netName)
	if code, _, e := pdns.Container.Exec(ctx, []string{"pdns_control", "notify-host", "example.net", secondaryIP}); e != nil || code != 0 {
		Logf(t, "pdns_control notify-host returned code=%d err=%v (falling back to SOA refresh)", code, e)
	} else {
		Logf(t, "sent NOTIFY to secondary %s via pdns_control notify-host", secondaryIP)
	}
	fatalOnErr(t, "secondary picked up the update",
		waitFor(t, "secondary served www2.example.net after update", func() bool {
			r, e := queryA("www2.example.net.")
			return e == nil && r.Rcode == dns.RcodeSuccess && len(r.Answer) > 0
		}, 500*time.Millisecond, 40*time.Second))
	Logf(t, "secondary picked up the update (www2.example.net. present)")
}

// TestPDNSNotifiedSerialPersisted verifies the notified serial lives in etcd, not process
// memory: a value written by putNotifiedSerial is read back by fresh on-demand reads
// (getNotifiedSerial / getAllNotifiedSerials). That process-independent persistence is what
// makes automatic NOTIFY work in pipe mode, where getUpdatedMasters and setNotified run in
// separate short-lived processes. (Named TestPDNS* so CI's -run PDNS job executes it.)
func TestPDNSNotifiedSerialPersisted(t *testing.T) {
	defer recoverPanicsT(t)
	etcd, err := startETCD(t)
	fatalOnErr(t, "start ETCD container", err)
	defer etcd.Terminate()
	sleepT(t, 1*time.Second)
	pe3 := startPE3(t, etcd.Endpoint, "", "-pdns-version="+getenvT("PDNS_VERSION", fmt.Sprintf("%d", defaultPdnsVersion))[:1])
	defer pe3.Terminate()
	fatalOnErr(t, "wait for PE3 ready", waitFor(t, "PE3 ready", func() bool { return status.serving }, 10*time.Millisecond, 30*time.Second))

	id := domainID("example.net.")
	const serial = uint32(2026061699)
	if got := getNotifiedSerial(id); got != 0 {
		Errorf(t, "expected notified serial 0 before any write, got %d", got)
	}
	fatalOnErr(t, "putNotifiedSerial", putNotifiedSerial(id, serial))
	// read back via on-demand etcd reads (no in-memory caching involved)
	if got := getNotifiedSerial(id); got != serial {
		Errorf(t, "getNotifiedSerial = %d, want %d", got, serial)
	}
	if m := getAllNotifiedSerials(); m[id] != serial {
		Errorf(t, "getAllNotifiedSerials[%d] = %d, want %d", id, m[id], serial)
	}
	Logf(t, "notified serial persisted in etcd and read back on demand (id=%d serial=%d)", id, serial)
}

// buildPE3Binary builds a static pe3 binary (linux/amd64) to be mounted into and spawned by the
// PowerDNS container in pipe mode.
func buildPE3Binary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "pdns-etcd3")
	cmd := exec.Command("go", "build", "-o", bin, "..") // module root is the parent of ./src
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if out, err := cmd.CombinedOutput(); err != nil {
		Fatalf(t, "building pe3 binary failed: %s\n%s", err, out)
	}
	Logf(t, "built pe3 binary at %s", bin)
	return bin
}

// seedPipeZone writes example.net. (prefix DNS/) directly into etcd via a raw client — in pipe
// mode there is no in-process pe3, so the test seeds etcd itself. A short SOA refresh lets the
// secondary re-check the serial quickly.
func seedPipeZone(t *testing.T, ec *clientv3.Client) {
	t.Helper()
	kvs := [][2]string{
		{"DNS/-defaults-", `{ttl: "1h"}`},
		{"DNS/-defaults-/SOA", "---\nrefresh: 10s\nretry: 10s\nexpire: 604800\nneg-ttl: 10m\nprimary: ns1\nmail: horst.master\n"},
		{"DNS/net.example/-options-/A", `{"ip-prefix": [192, 0, 2]}`},
		{"DNS/net.example/SOA", `{}`},
		{"DNS/net.example/NS#first", `="ns1"`},
		{"DNS/net.example/ns1/A", `=2`}, // ns1.example.net. A 192.0.2.2
		{"DNS/net.example/www/A", `=1`}, // www.example.net. A 192.0.2.1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, kv := range kvs {
		if _, err := ec.Put(ctx, kv[0], kv[1]); err != nil {
			Fatalf(t, "seed put %q: %s", kv[0], err)
		}
	}
}

// startPDNSPipe starts PowerDNS configured to run the pe3 BINARY in PIPE mode (one process per
// request thread), connecting to etcd over the shared network. This is the real-deployment shape
// (PowerDNS spawns pe3 per request) used to validate that primary operation — including the
// etcd-persisted notified serial — works in pipe mode, not just standalone.
func startPDNSPipe(t *testing.T, netName, binPath, etcdAddr string) (pdnsInfo, error) {
	t.Helper()
	v := getenvT("PDNS_VERSION", "50")
	image := fmt.Sprintf("powerdns/pdns-auth-%s", v)
	settings := []string{
		fmt.Sprintf("remote-connection-string=pipe:command=/pdns-etcd3,pdns-version=%s,endpoints=%s,prefix=DNS/", v[:1], etcdAddr),
		"distributor-threads=1", // pipe mode requires a single distributor (one pe3 process per thread)
		"cache-ttl=0",
		"query-cache-ttl=0",
		"negquery-cache-ttl=0",
		"allow-axfr-ips=0.0.0.0/0,::/0",
		primaryModeSetting(v),
	}
	if v >= "44" {
		settings = append(settings, "consistent-backends=no")
	}
	if v >= "45" {
		settings = append(settings, "zone-cache-refresh-interval=0")
	}
	Logf(t, "PDNS (pipe) settings: %v", settings)
	ctInfo, err := startContainer(t, testcontainers.ContainerRequest{
		Image:          image,
		Networks:       []string{netName},
		NetworkAliases: map[string][]string{netName: {"primary"}},
		ExposedPorts:   []string{"53/tcp"},
		LogConsumerCfg: &testcontainers.LogConsumerConfig{Consumers: []testcontainers.LogConsumer{CtLogger{t, "PDNS"}}},
		Files: []testcontainers.ContainerFile{
			{HostFilePath: "../testdata/pdns.conf", ContainerFilePath: "/etc/powerdns/pdns.conf", FileMode: 0o555},
			{Reader: linesReader(settings), ContainerFilePath: "/etc/powerdns/pdns.d/settings.conf", FileMode: 0o555},
			{HostFilePath: binPath, ContainerFilePath: "/pdns-etcd3", FileMode: 0o755},
		},
		WaitingFor: wait.ForLog("ready to distribute questions|operating unthreaded").AsRegexp().WithStartupTimeout(120 * time.Second),
	}, "53/tcp")
	return pdnsInfo{ctInfo, v}, err
}

// TestPDNSAXFRSecondaryPipe is the PIPE-mode end-to-end test: PowerDNS spawns the pe3 binary per
// request (the operator's real deployment shape). It verifies that a real ISC BIND9 secondary
// transfers the zone via AXFR and picks up a later change — proving primary mode (and the
// etcd-persisted notified serial, which is shared across the separate spawned processes) works in
// pipe mode, not only standalone.
func TestPDNSAXFRSecondaryPipe(t *testing.T) {
	defer recoverPanicsT(t)
	v := getenvT("PDNS_VERSION", "50")
	if v < "44" {
		t.Skipf("pipe-mode e2e targets the modern powerdns/pdns-auth image; PDNS %s uses an older/non-default protocol (the pipe protocol itself is covered by TestPipeRequests)", v)
	}
	ctx := context.Background()
	nw, err := network.New(ctx)
	fatalOnErr(t, "create docker network", err)
	defer func() { _ = nw.Remove(ctx) }()
	netName := nw.Name

	etcd, err := startETCD(t, map[string][]string{netName: {"etcd"}})
	fatalOnErr(t, "start ETCD container", err)
	defer etcd.Terminate()

	// In pipe mode there is no in-process pe3; PowerDNS spawns the binary. Seed etcd directly.
	ec, err := clientv3.New(clientv3.Config{Endpoints: []string{etcd.Endpoint}, DialTimeout: 10 * time.Second})
	fatalOnErr(t, "etcd client", err)
	defer func() { _ = ec.Close() }()
	seedPipeZone(t, ec)

	pdns, err := startPDNSPipe(t, netName, buildPE3Binary(t), "etcd:2379")
	fatalOnErr(t, "start PDNS (pipe) container", err)
	defer pdns.Terminate()
	primaryIP := containerIPOnNetwork(t, pdns.Container, netName)
	Logf(t, "primary (PowerDNS, pipe mode) IP on %s: %s", netName, primaryIP)

	bind, err := startBindSecondary(t, netName, primaryIP, "example.net.")
	fatalOnErr(t, "start BIND secondary", err)
	defer bind.Terminate()

	queryA := func(name string) (*dns.Msg, error) {
		m := new(dns.Msg)
		m.SetQuestion(name, dns.TypeA)
		c := &dns.Client{Net: "tcp", Timeout: 5 * time.Second}
		r, _, e := c.Exchange(m, bind.Endpoint)
		return r, e
	}

	// (1) initial AXFR-in, served by pe3 processes that PowerDNS spawns per request (pipe)
	fatalOnErr(t, "secondary serves zone after initial AXFR (pipe)",
		waitFor(t, "secondary served www.example.net after AXFR (pipe)", func() bool {
			r, e := queryA("www.example.net.")
			return e == nil && r.Rcode == dns.RcodeSuccess && len(r.Answer) > 0
		}, 500*time.Millisecond, 40*time.Second))
	Logf(t, "pipe mode: secondary served the transferred zone (AXFR-out via spawned pe3 works)")

	// (2) change etcd → serial bumps → notify the secondary. The notified serial is read/written
	// in etcd by separate spawned pe3 processes; this only works because it is persisted in etcd.
	uctx, ucancel := context.WithTimeout(context.Background(), 15*time.Second)
	_, perr := ec.Put(uctx, "DNS/net.example/www2/A", `=3`) // www2.example.net. A 192.0.2.3
	ucancel()
	fatalOnErr(t, "etcd update put", perr)
	secondaryIP := containerIPOnNetwork(t, bind.Container, netName)
	if code, _, e := pdns.Container.Exec(ctx, []string{"pdns_control", "notify-host", "example.net", secondaryIP}); e != nil || code != 0 {
		Logf(t, "pdns_control notify-host returned code=%d err=%v (falling back to SOA refresh)", code, e)
	} else {
		Logf(t, "sent NOTIFY to secondary %s via pdns_control notify-host", secondaryIP)
	}
	fatalOnErr(t, "secondary picked up the update (pipe)",
		waitFor(t, "secondary served www2.example.net after update (pipe)", func() bool {
			r, e := queryA("www2.example.net.")
			return e == nil && r.Rcode == dns.RcodeSuccess && len(r.Answer) > 0
		}, 500*time.Millisecond, 40*time.Second))
	Logf(t, "pipe mode: secondary picked up the update (primary mode works end-to-end in pipe)")
}
