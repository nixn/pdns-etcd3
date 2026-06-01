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
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

type programArgs struct {
	ConfigFile           *string
	Endpoints            *string
	DialTimeout          *time.Duration
	DialKeepAliveTime    *time.Duration
	DialKeepAliveTimeout *time.Duration
	AutoSyncInterval     *time.Duration
	PermitWithoutStream  *bool
	Prefix               *string
}

func (pa programArgs) String() string {
	return val2str(pa)
}

type statusType struct {
	populated, serving bool
}

var (
	// TODO encapsulate (most of?) global vars into a "main struct" (needed to do this with status and cli for integration tests already, perhaps better for the whole thing?)
	args       programArgs
	standalone bool
	dataRoot   *dataNode
	cli        = &etcdClient{}
	status     = &statusType{}
)

func parseBoolean(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "y", "yes", "1", "t", "true", "on":
		return true, nil
	case "n", "no", "0", "f", "false", "off":
		return false, nil
	default:
		// nolint:misspell
		return false, fmt.Errorf("not a boolean string (y[es]/n[o], 1/0, t[rue]/f[alse], on/off)")
	}
}

type setParameterFunc func(value string) error

func setBooleanParameterFunc(param *bool) setParameterFunc {
	return func(value string) error {
		v, err := parseBoolean(value)
		if err != nil {
			return err
		}
		*param = v
		return nil
	}
}

func setPdnsVersionParameter(param *uint) setParameterFunc {
	return func(value string) error {
		switch value {
		case "3":
			*param = 3
		case "4":
			*param = 4
		case "5":
			*param = 5
		default:
			return fmt.Errorf("invalid pdns version: %s", value)
		}
		return nil
	}
}

func setDurationParameterFunc(param *time.Duration, minValue *time.Duration) setParameterFunc {
	return func(value string) error {
		dur, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("failed to parse value as duration: %s", err)
		}
		if minValue != nil && dur < *minValue {
			return fmt.Errorf("duration value %q is less than minimum allowed (%s)", dur, minValue)
		}
		*param = dur
		return nil
	}
}

func setLogLevels(value string) {
	for _, v := range strings.Split(value, ";") {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		componentStr, levelStr, hasSeparator := strings.Cut(v, "=")
		if !hasSeparator {
			levelStr = componentStr
			componentStr = ""
		}
		// TODO allow logrus names as values (fatal, error, warn(ing), info)
		if level64, err := strconv.ParseInt(levelStr, 10, 8); err != nil {
			RootLog.Warnf()(nil, "invalid log level %q: %s", levelStr, err)("component", componentStr)
		} else {
			component := strings.Split(componentStr, ".")
			if len(component) == 1 && component[0] == "" {
				component = nil
			}
			RootLog.Importantf()(nil, "setting log level")("component", component, "level", level64)
			RootLog.ChildLog(component...).SetLevel(int(level64))
		}
	}
}

func readParameters(params objectType[string], client *pdnsClient) error {
	for k, v := range params {
		var err error
		switch {
		case !standalone && k == configFileParam:
			*args.ConfigFile = v
		case !standalone && k == endpointsParam:
			*args.Endpoints = v
		case !standalone && k == dialTimeoutParam:
			mdt := minimumDialTimeout
			err = setDurationParameterFunc(args.DialTimeout, &mdt)(v)
		case !standalone && k == dialKeepAliveTimeParam:
			err = setDurationParameterFunc(args.DialKeepAliveTime, nil)(v)
		case !standalone && k == dialKeepAliveTimeoutParam:
			err = setDurationParameterFunc(args.DialKeepAliveTimeout, nil)(v)
		case !standalone && k == autoSyncIntervalParam:
			err = setDurationParameterFunc(args.AutoSyncInterval, nil)(v)
		case !standalone && k == permitWithoutStreamParam:
			err = setBooleanParameterFunc(args.PermitWithoutStream)(v)
		case !standalone && k == prefixParam:
			*args.Prefix = v
		case k == pdnsVersionParam:
			client.Logf(2, "pdns", "init")("setting pdns-version to %s", v)()
			err = setPdnsVersionParameter(&client.PdnsVersion)(v)
		case !standalone && k == logLevelParam:
			setLogLevels(v)
		case standalone && k == clientIDParam:
			client.Logf(2, "pdns", "init")("setting inner client ID")(v)
			client.ID.SetClientID(v)
		case k == "path": // TODO standalone?
			// ignore
		default:
			client.Warnf("main", "init")("unknown parameter %q", k)()
		}
		if err != nil {
			return fmt.Errorf("failed to set parameter %q: %s", k, err)
		}
	}
	return nil
}

func startReadRequests(ctx context.Context, wg *WaitGroup, client *pdnsClient) <-chan pdnsRequest {
	ch := make(chan pdnsRequest)
	wg.Go(fmt.Sprintf("readRequests [%s]", client.ID), func(...any) {
		defer recoverPanics(func(v any) bool {
			recoverFunc(v, "readRequests()", false)
			return false
		})
		defer close(ch)
		done := make(chan struct{})
		defer close(done)
		wg.Go(fmt.Sprintf("readRequests [%s] done", client.ID), func(...any) {
			select {
			case <-ctx.Done():
				client.Logf(4, "pdns", "read", "{done}")("context canceled, closing input")()
				closeNoError(client.in)
			case <-done:
				client.Logf(4, "pdns", "read", "{done}")("done")()
			}
		})
		for {
			client.Logf(2, "pdns", "read")("waiting for next request")()
			if request, err := client.Comm.read(); err != nil {
				if err == io.EOF {
					client.Logf(3, "pdns", "read")("EOF on input stream, terminating")()
					return
				}
				if errors.Is(err, fs.ErrClosed) {
					client.Logf(3, "pdns", "read")("input stream closed, terminating")()
					return
				}
				client.Fatalf("pdns", "read")("failed to decode request: %s", err)()
			} else {
				client.Logf(1, "pdns", "read")("received new request")(request)
				ch <- *request
			}
		}
	})
	return ch
}

func (cr *pdnsClientRequest) handleRequest(ctx context.Context) {
	cr.Logf(2, "main")("handling request")(cr.Request)
	since := time.Now()
	var result any
	var err error
	switch strings.ToLower(cr.Request.Method) {
	case "lookup":
		result, err = cr.lookup()
	case "getalldomainmetadata":
		result, err = cr.getAllDomainMetadata()
	case "getdomainmetadata":
		result, err = cr.getDomainMetadata()
	case "setdomainmetadata":
		result, err = cr.setDomainMetadata(ctx)
	case "getalldomains":
		result = dataRoot.allDomains([]domainInfo{}) // must not be nil, for empty answers it would not be marshaled into `[]`
	case "getdomaininfo":
		result, err = cr.getDomainInfo()
	default:
		result, err = false, fmt.Errorf("unknown/unimplemented request: %s", val2str(cr.Request))
	}
	if err == nil {
		cr.Client.Respond(makeResponse(result))
	} else {
		cr.Client.Respond(makeResponse(result, err.Error()))
	}
	dur := time.Since(since)
	cr.Logf(1, "main")("request and result")("request", cr.Request, "result", result, "dur", dur, "err", err)
}

func handleEvents(revision int64, events []*clientv3.Event) {
	debug1 := RootLog.Logf(1, "etcd", "events")
	debug2 := RootLog.Logf(2, "etcd", "events")
	debug3 := RootLog.Logf(3, "etcd", "events")
	lockDebug := RootLog.Logf(4, "data", "locking")
	debug1(nil, "handling events")("#", len(events), "rev", revision)
	since := time.Now()
	reloadZones := map[string]*dataNode{}
	lockedZones := map[string]bool{}
EVENTS:
	for i, event := range events {
		entryKey := string(event.Kv.Key)
		debug2(nil, "handling event %d: %s %q", i+1, event.Type, entryKey)(event)
		name, entryType, qtype, id, version, err := parseEntryKey(entryKey)
		// check version first, because a new version could change the key syntax (but not prefix and version suffix)
		if version != nil && !dataVersion.IsCompatibleTo(*version, false) {
			debug3(nil, "ignoring event on version-incompatible entry %q", entryKey)()
			continue
		}
		if err != nil {
			RootLog.Errorf("etcd", "events")(nil, "failed to parse entry key %q, ignoring event: %s", entryKey, err)()
			continue
		}
		if entryType == lockEntry && event.Type != clientv3.EventTypeDelete {
			debug3(nil, "ignoring non-DELETE events for lock entries")(event.Type.String(), entryKey)
			continue
		}
		lockDebug(nil, "handleEvents: RLocking up to %q", Supplier1(name.asKey, true))()
		itemData, _ := dataRoot.getChild(name, false)
		lockDebug(nil, "handleEvents: RLocked %q", itemData.prefixKey)(itemData.LockCounts)
		zoneData := itemData.findZone()
		if event.Type == clientv3.EventTypeDelete && qtype == "SOA" && id == "" && entryType == normalEntry && zoneData != nil && zoneData.parent != nil {
			// deleting the SOA record deletes the zone, so the parent zone must be reloaded instead. this results in a full data reload for top-level zones. // TODO restrict to the (new) zone if only a new zone was added
			zoneData = zoneData.parent.findZone()
		}
		if zoneData == nil {
			zoneData = dataRoot
		}
		lockDebug(nil, "handleEvents: RUnlocking %q up to %q", itemData.prefixKey, zoneData.prefixKey)("item", itemData.LockCounts, "zone", zoneData.LockCounts)
		itemData.rUnlockUpwards(zoneData, false)
		qname := zoneData.getQname()
		if _, ok := lockedZones[qname]; !ok {
			debug3(nil, "getting lock data for zone %q", qname)()
			lockResponse, err := cli.Get(*args.Prefix+zoneData.prefixKey()+lockKey, true, &revision, *args.DialTimeout)
			if err != nil {
				RootLog.Warnf("etcd", "events")(nil, "failed to get lock data for zone %q: %s", qname, err)()
				lockDebug(nil, "handleEvents: RUnlocking %q", zoneData.prefixKey)(zoneData.LockCounts)
				zoneData.rUnlockUpwards(nil, false)
				continue EVENTS
			}
			if _, ok := <-lockResponse.DataChan; ok {
				debug2(nil, "marking zone as locked")(qname)
				lockedZones[qname] = true
			} else {
				debug2(nil, "marking zone as not locked")(qname)
				lockedZones[qname] = false
			}
		}
		if lockedZones[qname] {
			debug2(nil, "event zone locked, ignoring")(qname)
			lockDebug(nil, "handleEvents: RUnlocking %q", zoneData.prefixKey)(zoneData.LockCounts)
			zoneData.rUnlockUpwards(nil, false)
			continue EVENTS
		}
		subdomains := make([]string, 0, len(reloadZones))
		for dnKey, dn := range reloadZones {
			if zoneData.subdomainDepth(dn) >= 0 {
				debug2(nil, "event zone already scheduled for reload (possibly ancestor)")("event", qname, "scheduled", dn.getQname)
				lockDebug(nil, "handleEvents: RUnlocking %q", zoneData.prefixKey)(zoneData.LockCounts)
				zoneData.rUnlockUpwards(nil, false)
				continue EVENTS
			}
			if dn.subdomainDepth(zoneData) > 0 {
				lockDebug(nil, "handleEvents: RUnlocking %q", dn.prefixKey)(dn.LockCounts)
				dn.rUnlockUpwards(nil, false)
				debug2(nil, "scheduled zone is a subdomain of event zone, marking for replace")("event", qname, "scheduled", dn.getQname)
				subdomains = append(subdomains, dnKey)
			}
		}
		for _, k := range subdomains {
			delete(reloadZones, k)
		}
		debug1(nil, "scheduling event zone %q for reload", qname)("replacing", subdomains)
		reloadZones[qname] = zoneData
	}
	debug1(nil, "reloading zones")(Supplier1(Keys, reloadZones))
	for _, dn := range reloadZones {
		qname := dn.getQname()
		debug2(nil, "reloading zone %q", qname)()
		since := time.Now()
		getResponse, err := cli.Get(*args.Prefix+dn.prefixKey(), true, &revision, *args.DialTimeout)
		if err != nil {
			lockDebug(nil, "handleEvents: RUnlocking %q", dn.prefixKey)(dn.LockCounts)
			dn.rUnlockUpwards(nil, false)
			RootLog.Warnf("etcd", "events")(nil, "failed to get data for zone %q, not reloading: %s", qname, err)()
			continue
		}
		func() {
			lockDebug(nil, "handleEvents: RUnlocking %q directly", dn.prefixKey)(dn.LockCounts)
			dn.mutex.RUnlock()
			if dn.parent != nil {
				defer dn.parent.rUnlockUpwards(nil, false)
				defer lockDebug(nil, "handleEvents: RUnlocking %q", dn.parent.prefixKey)(dn.parent.LockCounts)
			}
			lockDebug(nil, "handleEvents: WLocking %q directly", dn.prefixKey)()
			dn.mutex.Lock()
			lockDebug(nil, "handleEvents: WLocked %q directly", dn.prefixKey)(dn.LockCounts)
			defer dn.mutex.Unlock()
			defer lockDebug(nil, "handleEvents: WUnlocking %q directly", dn.prefixKey)(dn.LockCounts)
			oldZoneRev := dn.zoneRev()
			dn.reload(getResponse.DataChan)
			if newZoneRev := dn.zoneRev(); newZoneRev < oldZoneRev {
				// TODO add test for this
				debug1(nil, "zone revision jumped backwards, fixing it")("zone", qname, "old", oldZoneRev, "new", newZoneRev)
				dn.maxRev = oldZoneRev + 1
				key := *args.Prefix + dn.prefixKey() + metadataKey + keySeparator + MetaMinimumSerial
				if _, err = cli.Put(key, fmt.Sprintf("%d", dn.zoneRev()), *args.DialTimeout); err != nil {
					RootLog.Errorf("etcd", "events")(nil, "failed to put to ETCD: %s", err)(key)
				}
			}
		}()
		dur := time.Since(since)
		debug2(nil, "reloaded zone %q", qname)("#records", dn.recordsCount, "#zones", dn.zonesCount, "dur", dur, "zoneRev", dn.zoneRev)
	}
	dur := time.Since(since)
	debug1(nil, "reloaded zones")("dur", dur)
}

// Main is the "moved" program entrypoint, but with git version argument (which is set in real main package)
func Main(programVersion VersionType, gitVersion string) {
	main(programVersion, gitVersion, os.Args[1:], make(chan os.Signal, 1), false)
	RootLog.Logf(3, "main")(nil, "main() returned normally")()
}

var (
	standaloneArg           = flag.String("standalone", "", `Use a standalone mode determined by the given URL (unix:///path/to/socket[?relative=<bool>] or http://<listen-address>:<listen-port>)`)
	configFileArg           = flag.String(configFileParam, "", "Use the given configuration file for the ETCD connection (overrides -endpoints)")
	endpointsArg            = flag.String(endpointsParam, defaultEndpointIPv6+"|"+defaultEndpointIPv4, "Use the endpoints configuration for ETCD connection")
	dialTimeoutArg          = flag.Duration(dialTimeoutParam, defaultDialTimeout, "ETCD dial timeout")
	dialKeepAliveTimeArg    = flag.Duration(dialKeepAliveTimeParam, defaultDialKeepAliveTime, "ETCD dial keep-alive ping interval (0 to disable)")
	dialKeepAliveTimeoutArg = flag.Duration(dialKeepAliveTimeoutParam, defaultDialKeepAliveTimeout, "ETCD dial keep-alive ping timeout")
	autoSyncIntervalArg     = flag.Duration(autoSyncIntervalParam, defaultAutoSyncInterval, "ETCD member list auto-sync interval (0 to disable)")
	permitWithoutStreamArg  = flag.Bool(permitWithoutStreamParam, defaultPermitWithoutStream, "send ETCD client keep-alive pings even with no active RPC stream")
	prefixArg               = flag.String(prefixParam, "", "Global key prefix")
	pdnsVersionArg          = flag.String(pdnsVersionParam, "", "default PDNS version")
	logLevelArg             = flag.String(logLevelParam, "", "Set logging level(s) ([<component>[.<component>]...=]<level>[,...])")
)

func main(programVersion VersionType, gitVersion string, cmdLineArgs []string, osSignals chan os.Signal, trackReaders bool) {
	// recoverPanics must be used here, because integration test calls main(...) directly, not Main(...)
	defer recoverPanics(func(v any) bool {
		return !recoverFunc(v, "main()", true)
	})
	releaseVersion := fmt.Sprintf("%s+%s", programVersion, dataVersion)
	if "v"+releaseVersion != gitVersion {
		releaseVersion += fmt.Sprintf("[%s]", gitVersion)
	}
	RootLog.Infof()(nil, "pdns-etcd3 %s, Copyright © 2016-2026 nix <https://keybase.io/nixn>", releaseVersion)()
	// handle arguments // TODO handle more arguments, f.e. 'show-defaults' standalone command
	args = programArgs{
		ConfigFile:           configFileArg,
		Endpoints:            endpointsArg,
		DialTimeout:          dialTimeoutArg,
		DialKeepAliveTime:    dialKeepAliveTimeArg,
		DialKeepAliveTimeout: dialKeepAliveTimeoutArg,
		AutoSyncInterval:     autoSyncIntervalArg,
		PermitWithoutStream:  permitWithoutStreamArg,
		Prefix:               prefixArg,
	}
	if err := flag.CommandLine.Parse(cmdLineArgs); err != nil { // same as flag.Parse(), but we can pass the arguments instead of being fixed to os.Args[1:] (needed for integration testing)
		RootLog.Fatalf("main", "init")(nil, "failed to parse command line arguments: %s", err)()
	}
	if *logLevelArg != "" {
		setLogLevels(*logLevelArg)
	}
	if *pdnsVersionArg != "" {
		// TODO remove this, because now the HTTP clients can pass parameters, too
		RootLog.Logf(1, "main", "init")(nil, "setting default PDNS version")(*pdnsVersionArg)
		if err := setPdnsVersionParameter(&defaultPdnsVersion)(*pdnsVersionArg); err != nil {
			RootLog.Fatalf("main", "init")(nil, "failed to set default PDNS version: %s", err)("arg", *pdnsVersionArg)
		}
	}
	signal.Notify(osSignals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		RootLog.Logf(3, "main", "{signal}")(nil, "waiting for OS signals (HUP, INT, TERM, QUIT)")()
		sig := <-osSignals
		RootLog.Logf(1, "main", "{signal}")(nil, "caught signal %s, shutting down", sig)()
		cancel()
	}()
	wg := new(WaitGroup).Init()
	standalone = *standaloneArg != ""
	if standalone {
		u, err := url.Parse(*standaloneArg)
		if err != nil {
			RootLog.Fatalf("main", "init")(nil, "failed to parse standalone URL: %s", err)()
		}
		standalone, ok := standalones[u.Scheme]
		if !ok {
			RootLog.Fatalf("main", "init")(nil, "unknown scheme in standalone URL")(u.Scheme)
		}
		if messages, err := cli.Setup(&args); err != nil {
			RootLog.Fatalf("main", "init")(nil, "client setup failed: %s", err)()
		} else {
			RootLog.Logf(1, "main", "init")(nil, "client setup messages")(messages)
		}
		defer cli.Close()
		if err = populateData(ctx, wg, trackReaders); err != nil {
			RootLog.Fatalf("main", "init")(nil, "populating data failed: %s", err)()
		}
		standalone(ctx, wg, u)
	} else {
		r, w, err := os.Pipe()
		if err != nil {
			RootLog.Fatalf("main", "init")(nil, "failed to create os.Pipe(): %s", err)()
		}
		defer closeNoError(r)
		defer closeNoError(w)
		go func() { // do not use wg for the stdin wrapper, because io.Copy does not stop on closing w; just let the system stop it when closing the program
			_, _ = io.Copy(w, os.Stdin)
		}()
		pipe(ctx, wg, r, os.Stdout, trackReaders)
	}
	RootLog.Logf(4, "main")(nil, "request handler returned normally, stopping work")()
	cancel()
	nr, _ := wg.State(false)
WAIT:
	for n, N := 1, 3; nr > 0 && n <= N; n++ {
		var names []string
		nr, names = wg.State(true)
		RootLog.Logf(3, "main", "done")(nil, "waiting for child routines to finish [%d/%d]", n, N)("#", nr, names)
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
		done := make(chan struct{})
		go func() {
			defer waitCancel()
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
			break WAIT
		case <-waitCtx.Done():
			if n == N {
				nr, names = wg.State(true)
				RootLog.Errorf("main", "done")(nil, "timeout while waiting on child routines, exiting forcefully")(names)
			}
		}
	}
	if nr == 0 {
		RootLog.Logf(3, "main", "done")(nil, "all child routines finished, exiting normally")()
	}
}

func populateData(ctx context.Context, wg *WaitGroup, trackReaders bool) error {
	debug1 := RootLog.Logf(1, "data", "init")
	debug1(nil, "populating data")()
	getResponse, err := cli.Get(*args.Prefix, true, nil, *args.DialTimeout)
	if err != nil {
		return fmt.Errorf("get() failed: %s", err)
	}
	cli.CurrentRevision = getResponse.Revision
	func() {
		dataRoot = newDataNode(nil, "", "", trackReaders)
		dataRoot.mutex.Lock()
		defer dataRoot.mutex.Unlock()
		dataRoot.reload(getResponse.DataChan)
		debug1(nil, "loaded data")("#records", dataRoot.recordsCount, "#zones", dataRoot.zonesCount, "revision", getResponse.Revision)
	}()
	status.populated = true
	debug1(nil, "starting data watcher")()
	wg.Go("watchData", func(...any) {
		defer recoverPanics(func(v any) bool {
			recoverFunc(v, "watchData()", false)
			return false
		})
		cli.WatchData(ctx, *args.Prefix)
		RootLog.Logf(4, "data", "{watch}")(nil, "watchData() returned normally")()
	})
	return nil
}

type pipeClientID struct{}

func (id pipeClientID) String() string {
	return "*"
}

func (id pipeClientID) SetClientID(string) {}

func pipe(ctx context.Context, wg *WaitGroup, in io.ReadCloser, out io.WriteCloser, trackReaders bool) {
	initialized := func(client *pdnsClient) []string {
		clientMessages, err := cli.Setup(&args)
		if err != nil {
			client.Fatal(fmt.Errorf("setupClient() failed: %s", err))
		}
		RootLog.Logf(1, "etcd", "init")(nil, "connected")()
		if err := populateData(ctx, wg, trackReaders); err != nil {
			client.Fatal(fmt.Errorf("populateData() failed: %s", err))
		}
		return clientMessages
	}
	defer cli.Close()
	serve(ctx, wg, newPdnsClient(ctx, pipeClientID{}, in, out), &initialized, &status.serving)
}

func serve(ctx context.Context, wg *WaitGroup, client *pdnsClient, initialized *func(*pdnsClient) []string, serving *bool) {
	reqChan := startReadRequests(ctx, wg, client)
	if initialized != nil {
		// first request must be 'initialize'
		client.Logf(2, "pdns", "init")("waiting for initialize request")()
		initRequest, ok := <-reqChan
		if !ok {
			client.Logf(3, "pdns")("requests channel closed")()
			return
		}
		if initRequest.Method != "initialize" {
			client.Fatalf("pdns", "init")("wrong request method (waited for 'initialize')")(initRequest)
		}
		client.Logf(1, "pdns", "init")("initializing")("parameters", initRequest.Parameters)
		params := objectType[string]{}
		for k, v := range initRequest.Parameters {
			params[k] = v.(string)
		}
		err := readParameters(params, client)
		if err != nil {
			client.Fatal(err)
		}
		client.Logf(2, "pdns", "init")("successfully read parameters")()
		logMessages := (*initialized)(client)
		client.Respond(makeResponse(true, logMessages...))
	}
	if serving != nil {
		*serving = true
	}
	for nextRequestID := uint64(1); ; nextRequestID++ {
		client.Logf(2, "pdns")("waiting for next request")("nextRequestID", nextRequestID)
		request, ok := <-reqChan
		if !ok {
			client.Logf(3, "pdns")("requests channel closed")("nextRequestID", nextRequestID)
			break
		}
		cr := &pdnsClientRequest{client, nextRequestID, &request}
		cr.handleRequest(ctx)
	}
	if serving != nil {
		*serving = false
	}
}

func makeResponse(result any, msgs ...string) objectType[any] {
	response := objectType[any]{"result": result}
	if len(msgs) > 0 {
		response["log"] = msgs
	}
	return response
}
