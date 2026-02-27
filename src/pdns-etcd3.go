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
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	clientv3 "go.etcd.io/etcd/client/v3"
)

type programArgs struct {
	ConfigFile  *string
	Endpoints   *string
	DialTimeout *time.Duration
	Prefix      *string
}

func (pa programArgs) String() string {
	return fmt.Sprintf("ConfigFile=%s, Endpoints=%s, DialTimeout=%s, Prefix=%s", val2str(pa.ConfigFile), val2str(pa.Endpoints), val2str(pa.DialTimeout), val2str(pa.Prefix))
}

type statusType struct {
	populated, serving bool
}

var (
	// TODO encapsulate (most of?) global vars into a "main struct" (needed to do this with status and cli for integration tests already, perhaps better for the whole thing?)
	log        = newLog(nil, "main", "pdns", "etcd", "data") // TODO timings
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

func readParameters(params objectType[string], client *pdnsClient) error {
	for k, v := range params {
		var err error
	SWITCH:
		switch {
		case !standalone && k == configFileParam:
			*args.ConfigFile = v
		case !standalone && k == endpointsParam:
			*args.Endpoints = v
		case !standalone && k == dialTimeoutParam:
			mdt := minimumDialTimeout
			err = setDurationParameterFunc(args.DialTimeout, &mdt)(v)
		case !standalone && k == prefixParam:
			*args.Prefix = v
		case k == pdnsVersionParam:
			err = setPdnsVersionParameter(&client.PdnsVersion)(v)
		case strings.HasPrefix(k, logParamPrefix):
			for _, level := range logrus.AllLevels {
				if k == logParamPrefix+level.String() {
					if !standalone {
						log.setLoggingLevel(v, level)
					}
					client.log.setLoggingLevel(v, level)
					break SWITCH
				}
			}
			err = fmt.Errorf("invalid log level parameter: %s", k)
		case k == "path":
			// ignore
		default:
			client.log.main().Warnf("unknown parameter %q", k)
		}
		if err != nil {
			return fmt.Errorf("failed to set parameter %q: %s", k, err)
		}
	}
	return nil
}

func startReadRequests(ctx context.Context, wg *WaitGroup, client *pdnsClient) <-chan pdnsRequest {
	ch := make(chan pdnsRequest)
	wg.Go(fmt.Sprintf("readRequests [%s]", client.ID), func(_ any) {
		defer recoverPanics(func(v any) bool {
			recoverFunc(v, "readRequests()", false)
			return false
		})
		defer close(ch)
		done := make(chan struct{})
		defer close(done)
		wg.Go(fmt.Sprintf("readRequests [%s] done", client.ID), func(_ any) {
			select {
			case <-ctx.Done():
				client.log.pdns().Tracef("{readRequests done} context canceled, closing input")
				closeNoError(client.in)
			case <-done:
				client.log.pdns().Trace("{readRequests done} done")
			}
		}, nil)
		for {
			client.log.pdns().Trace("{readRequests} waiting for next request")
			if request, err := client.Comm.read(); err != nil {
				if err == io.EOF {
					client.log.pdns().Trace("{readRequests} EOF on input stream, terminating")
					return
				}
				if errors.Is(err, fs.ErrClosed) {
					client.log.pdns().Trace("{readRequests} input stream closed, terminating")
					return
				}
				client.log.pdns(err).Panicf("{readRequests} failed to decode request: %s", err)
			} else {
				client.log.pdns(request).Debug("{readRequests} received new request")
				ch <- *request
			}
		}
	}, nil)
	return ch
}

func handleRequest(request *pdnsRequest, client *pdnsClient) {
	client.log.main(request).Trace("handling request")
	since := time.Now()
	var result any
	var err error
	switch strings.ToLower(request.Method) {
	case "lookup":
		result, err = lookup(request.Parameters, client)
	case "getalldomainmetadata":
		result = map[string]any{}
	case "getdomainmetadata":
		result = []string{}
	case "getalldomains":
		result = dataRoot.allDomains([]domainInfo{}) // must not be nil, for empty answers it would not be marshalled into `[]`
	default:
		result, err = false, fmt.Errorf("unknown/unimplemented request: %s", request)
	}
	if err == nil {
		client.Respond(makeResponse(result))
	} else {
		client.Respond(makeResponse(result, err.Error()))
	}
	dur := time.Since(since)
	client.log.main("dur", dur, "err", err, "request", request, "result", result).Debug("request and result")
}

func handleEvents(revision int64, events []*clientv3.Event) {
	log.etcd("rev", revision).Debugf("handling events (%d)", len(events))
	since := time.Now()
	reloadZones := map[string]*dataNode{}
EVENTS:
	for i, event := range events {
		entryKey := string(event.Kv.Key)
		log.etcd(event).Tracef("handling event %d: %s %q", i+1, event.Type, entryKey)
		name, entryType, qtype, id, version, err := parseEntryKey(entryKey)
		// check version first, because a new version could change the key syntax (but not prefix and version suffix)
		if version != nil && !dataVersion.IsCompatibleTo(*version, false) {
			log.data().Tracef("ignoring event on version-incompatible entry %q", entryKey)
			continue
		}
		if err != nil {
			log.data(err.Error()).Errorf("failed to parse entry key %q, ignoring event", entryKey)
			continue
		}
		itemData, _ := dataRoot.getChild(name, false)
		zoneData := itemData.findZone()
		if event.Type == clientv3.EventTypeDelete && qtype == "SOA" && id == "" && entryType == normalEntry && zoneData != nil && zoneData.parent != nil {
			// deleting the SOA record deletes the zone, so the parent zone must be reloaded instead. this results in a full data reload for top-level zones.
			zoneData = zoneData.parent.findZone()
		}
		if zoneData == nil {
			zoneData = dataRoot
		}
		itemData.rUnlockUpwards(zoneData, false)
		qname := zoneData.getQname()
		subdomains := make([]string, 0, len(reloadZones))
		for dnKey, dn := range reloadZones {
			if zoneData.subdomainDepth(dn) >= 0 {
				zoneData.rUnlockUpwards(nil, false)
				log.data("event", qname, "scheduled", dn.getQname()).Trace("event zone already scheduled for reload (possibly ancestor)")
				continue EVENTS
			}
			if dn.subdomainDepth(zoneData) > 0 {
				dn.rUnlockUpwards(nil, false)
				log.data("event", qname, "scheduled", dn.getQname()).Trace("scheduled zone is a subdomain of event zone, marking for replace")
				subdomains = append(subdomains, dnKey)
			}
		}
		for _, k := range subdomains {
			delete(reloadZones, k)
		}
		log.data("event", qname, "replacing", subdomains).Debug("scheduling event zone for reload")
		reloadZones[qname] = zoneData
	}
	log.data(Keys(reloadZones)).Debug("reloading zones")
	for _, dn := range reloadZones {
		qname := dn.getQname()
		log.data().Tracef("reloading zone %q", qname)
		since := time.Now()
		getResponse, err := cli.Get(*args.Prefix+dn.prefixKey(), true, &revision, *args.DialTimeout)
		if err != nil {
			dn.rUnlockUpwards(nil, false)
			log.data().Warnf("failed to get data for zone %q (not reloading): %s", qname, err)
			continue
		}
		log.data().Debugf("reloading zone %q", qname)
		func() {
			dn.mutex.RUnlock()
			if dn.parent != nil {
				defer dn.parent.rUnlockUpwards(nil, false)
			}
			dn.mutex.Lock()
			defer dn.mutex.Unlock()
			dn.reload(getResponse.DataChan)
		}()
		dur := time.Since(since)
		log.data("#records", dn.recordsCount(), "#zones", dn.zonesCount(), "duration", dur).Debugf("reloaded zone %q", qname)
	}
	dur := time.Since(since)
	log.data("duration", dur).Debug("reloaded zones")
}

// Main is the "moved" program entrypoint, but with git version argument (which is set in real main package)
func Main(programVersion VersionType, gitVersion string) {
	main(programVersion, gitVersion, os.Args[1:], make(chan os.Signal, 1), false)
	log.main().Trace("main() returned normally")
}

var (
	standaloneArg  = flag.String("standalone", "", `Use a standalone mode determined by the given URL (unix:///path/to/socket[?relative=<bool>] or http://<listen-address>:<listen-port>)`)
	configFileArg  = flag.String(configFileParam, "", "Use the given configuration file for the ETCD connection (overrides -endpoints)")
	endpointsArg   = flag.String(endpointsParam, defaultEndpointIPv6+"|"+defaultEndpointIPv4, "Use the endpoints configuration for ETCD connection")
	dialTimeoutArg = flag.Duration(dialTimeoutParam, defaultDialTimeout, "ETCD dial timeout")
	prefixArg      = flag.String(prefixParam, "", "Global key prefix")
	pdnsVersionArg = flag.String(pdnsVersionParam, "", "default PDNS version")
	loggingArgs    = func() map[logrus.Level]*string {
		args := map[logrus.Level]*string{}
		for _, level := range logrus.AllLevels {
			args[level] = flag.String(logParamPrefix+level.String(), "", fmt.Sprintf("Set logging level %s to the given components (separated by +)", level))
		}
		return args
	}()
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
	log.main().Printf("pdns-etcd3 %s, Copyright © 2016-2026 nix <https://keybase.io/nixn>", releaseVersion)
	// handle arguments // TODO handle more arguments, f.e. 'show-defaults' standalone command
	args = programArgs{
		ConfigFile:  configFileArg,
		Endpoints:   endpointsArg,
		DialTimeout: dialTimeoutArg,
		Prefix:      prefixArg,
	}
	if err := flag.CommandLine.Parse(cmdLineArgs); err != nil { // same as flag.Parse(), but we can pass the arguments instead of being fixed to os.Args[1:] (needed for integration testing)
		log.main().Panicf("failed to parse command line arguments: %s", err)
	}
	for level, components := range loggingArgs {
		if len(*components) > 0 {
			log.setLoggingLevel(*components, level)
		}
	}
	if *pdnsVersionArg != "" {
		log.main().Debugf("setting default PDNS version to %s", *pdnsVersionArg)
		if err := setPdnsVersionParameter(&defaultPdnsVersion)(*pdnsVersionArg); err != nil {
			log.main("arg", *pdnsVersionArg).Panicf("failed to set default PDNS version: %s", err)
		}
	}
	signal.Notify(osSignals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		log.main().Trace("{signal} waiting for OS signals (HUP, INT, TERM, QUIT)")
		sig := <-osSignals
		log.main().Debugf("{signal} caught signal %s, shutting down", sig)
		cancel()
	}()
	wg := new(WaitGroup).Init()
	standalone = *standaloneArg != ""
	if standalone {
		u, err := url.Parse(*standaloneArg)
		if err != nil {
			log.main().Panicf("failed to parse standalone URL: %s", err)
		}
		standalone, ok := standalones[u.Scheme]
		if !ok {
			log.main("scheme", u.Scheme).Panic("unknown scheme in standalone URL")
		}
		if messages, err := cli.Setup(&args); err != nil {
			log.main().Panicf("setupClient() failed: %s", err)
		} else {
			log.main(messages).Debug("setupClient messages")
		}
		defer cli.Close()
		if err = populateData(ctx, wg, trackReaders); err != nil {
			log.main().Panicf("populateData() failed: %s", err)
		}
		standalone(ctx, wg, u)
	} else {
		r, w, err := os.Pipe()
		if err != nil {
			log.main().Panicf("failed to create os.Pipe(): %s", err)
		}
		defer closeNoError(r)
		defer closeNoError(w)
		go func() { // do not use wg for the stdin wrapper, because io.Copy does not stop on closing w; just let the system stop it when closing the program
			_, _ = io.Copy(w, os.Stdin)
		}()
		pipe(ctx, wg, r, os.Stdout, trackReaders)
	}
	log.main().Debug("request handler returned normally, stopping work")
	cancel()
	nr, _ := wg.State(false)
WAIT:
	for n, N := 1, 3; nr > 0 && n <= N; n++ {
		var names []string
		nr, names = wg.State(true)
		log.main(names).Tracef("waiting for child routines (%d) to finish [%d/%d]", nr, n, N)
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
				log.main(names).Error("timeout while waiting on routines, exiting forcefully")
			}
		}
	}
	if nr == 0 {
		log.main().Trace("all routines finished, exiting normally")
	}
}

func populateData(ctx context.Context, wg *WaitGroup, trackReaders bool) error {
	log.main().Debug("populating data")
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
		log.main("#records", dataRoot.recordsCount(), "#zones", dataRoot.zonesCount(), "revision", getResponse.Revision).Debug("loaded data")
	}()
	status.populated = true
	log.main().Debug("starting data watcher")
	wg.Go("watchData", func(_ any) {
		defer recoverPanics(func(v any) bool {
			recoverFunc(v, "watchData()", false)
			return false
		})
		cli.WatchData(ctx, *args.Prefix)
		log.main().Trace("watchData() returned normally")
	}, nil)
	return nil
}

func pipe(ctx context.Context, wg *WaitGroup, in io.ReadCloser, out io.WriteCloser, trackReaders bool) {
	initialized := func(client *pdnsClient) []string {
		clientMessages, err := cli.Setup(&args)
		if err != nil {
			client.Fatal(fmt.Errorf("setupClient() failed: %s", err))
		}
		log.etcd().Debugf("connected")
		if err := populateData(ctx, wg, trackReaders); err != nil {
			client.Fatal(fmt.Errorf("populateData() failed: %s", err))
		}
		return clientMessages
	}
	defer cli.Close()
	serve(ctx, wg, newPdnsClient(ctx, "*", in, out), &initialized, &status.serving)
}

func serve(ctx context.Context, wg *WaitGroup, client *pdnsClient, initialized *func(*pdnsClient) []string, serving *bool) {
	reqChan := startReadRequests(ctx, wg, client)
	if initialized != nil {
		// first request must be 'initialize'
		client.log.pdns().Trace("waiting for initialize request")
		initRequest, ok := <-reqChan
		if !ok {
			client.log.pdns().Trace("requests channel closed")
			return
		}
		if initRequest.Method != "initialize" {
			client.log.pdns("method", initRequest.Method).Panic("wrong request method (waited for 'initialize')")
		}
		client.log.main("parameters", initRequest.Parameters).Info("initializing")
		params := objectType[string]{}
		for k, v := range initRequest.Parameters {
			params[k] = v.(string)
		}
		err := readParameters(params, client)
		if err != nil {
			client.Fatal(err)
		}
		client.log.main().Debugf("successfully read parameters")
		logMessages := (*initialized)(client)
		client.Respond(makeResponse(true, logMessages...))
	}
	if serving != nil {
		*serving = true
	}
	for {
		client.log.pdns().Trace("waiting for next request")
		request, ok := <-reqChan
		if !ok {
			client.log.pdns().Trace("requests channel closed")
			break
		}
		handleRequest(&request, client)
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
