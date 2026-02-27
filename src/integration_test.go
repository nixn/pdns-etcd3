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
	"math/rand"
	"net"
	"net/url"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/miekg/dns"
	"github.com/testcontainers/testcontainers-go"
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

func putT(t *testing.T, prefix, key, value string) int64 {
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
	prefix := ""
	args = programArgs{
		ConfigFile:  &config,
		Endpoints:   &etcd.Endpoint,
		DialTimeout: &timeout,
		Prefix:      &prefix,
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
		request := pdnsRequest{"initialize", objectType[any]{"pdns-version": "3", "prefix": testPrefix, "log-trace": "main+pdns+etcd+data"}}
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

func startETCD(t *testing.T) (*ctInfo, error) {
	t.Helper()
	image := fmt.Sprintf("quay.io/coreos/etcd:v%s", getenvT("ETCD_VERSION", "3.6.7"))
	Logf(t, "Using ETCD image %s", image)
	return startContainer(t, testcontainers.ContainerRequest{
		Image:          image,
		Hostname:       "etcd",
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

func startPE3(t *testing.T, etcdEndpoint, prefix string, moreArgs ...string) pe3Info {
	t.Helper()
	httpAddress, _ := url.Parse("http://0.0.0.0:8053") // the port is fixed, it is set in pdns.conf too
	doneCtx, done := context.WithCancel(context.Background())
	osSignals := make(chan os.Signal, 1)
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

func startPDNS(t *testing.T, dynamicSettings map[string]string) (pdnsInfo, error) {
	t.Helper()
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
	cli = new(etcdClient)
	status = new(statusType)
	pe3 := startPE3(t, etcd.Endpoint, "", "-log-trace=main+etcd+pdns+data", "-pdns-version="+getenvT("PDNS_VERSION", fmt.Sprintf("%d", defaultPdnsVersion))[:1])
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
		return delOp(pe3.Prefix + key)
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
	nCPU := runtime.NumCPU()
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
		wg.Go(fmt.Sprintf("PDNS#%d queries", i+1), func(p_ any) {
			p := p_.(pdnsP)
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
