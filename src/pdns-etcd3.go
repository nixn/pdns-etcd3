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
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
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

var (
	log        = newLog(nil, "main", "pdns", "etcd", "data") // TODO timings
	args       programArgs
	standalone bool
	dataRoot   *dataNode
)

var (
	// these vars may be used in integration tests, so don't bail if not used
	serving   = false
	connected = false
	populated = false
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

func startReadRequests(ctx context.Context, wg *sync.WaitGroup, client *pdnsClient) <-chan pdnsRequest {
	ch := make(chan pdnsRequest)
	wgGo(wg, func() {
		defer recoverPanics(func(v any) bool {
			recoverFunc(v, "readRequests()", false)
			return false
		})
		defer close(ch)
		wgGo(wg, func() {
			<-ctx.Done()
			closeNoError(client.in)
		})
		for {
			if request, err := client.Comm.read(); err != nil {
				if err == io.EOF {
					client.log.pdns().Trace("EOF on input stream, terminating")
					return
				}
				client.log.pdns().Panicf("failed to decode request: %s", err)
			} else {
				client.log.pdns(request).Debug("received new request")
				ch <- *request
			}
		}
	})
	return ch
}

func handleRequest(request *pdnsRequest, client *pdnsClient) {
	client.log.main(request).Debug("handling request")
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
	client.log.main("dur", dur, "err", err, "val", result).Trace("result")
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
		itemData, _ := dataRoot.getChild(name, true)
		zoneData := itemData.findZone()
		if event.Type == clientv3.EventTypeDelete && qtype == "SOA" && id == "" && entryType == normalEntry && zoneData != nil && zoneData.parent != nil {
			// deleting the SOA record deletes the zone, so the parent zone must be reloaded instead. this results in a full data reload for top-level zones.
			zoneData = zoneData.parent.findZone()
		}
		if zoneData == nil {
			zoneData = dataRoot
		}
		itemData.rUnlockUpwards(zoneData)
		qname := zoneData.getQname()
		for _, dn := range reloadZones {
			if zoneData.subdomainDepth(dn) >= 0 {
				zoneData.rUnlockUpwards(nil)
				log.data("event", qname, "scheduled", dn.getQname()).Trace("zone already scheduled for reload (possibly ancestor)")
				continue EVENTS
			}
		}
		log.data(qname).Debug("scheduling zone for reload")
		reloadZones[qname] = zoneData
	}
	log.data(Keys(reloadZones)).Debug("reloading zones")
	for _, dn := range reloadZones {
		qname := dn.getQname()
		log.data().Tracef("reloading zone %q", qname)
		since := time.Now()
		getResponse, err := get(*args.Prefix+dn.prefixKey(), true, &revision)
		if err != nil {
			dn.rUnlockUpwards(nil)
			log.data().Warnf("failed to get data for zone %q (not reloading): %s", qname, err)
			continue
		}
		log.data().Debugf("reloading zone %q", qname)
		func() {
			dn.mutex.RUnlock()
			if dn.parent != nil {
				defer dn.parent.rUnlockUpwards(nil)
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
	main(programVersion, gitVersion, os.Args[1:], make(chan os.Signal, 1))
	log.main().Trace("main() returned normally")
}

func main(programVersion VersionType, gitVersion string, cmdLineArgs []string, osSignals chan os.Signal) {
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
	standaloneArg := flag.String("standalone", "", `Use a standalone mode determined by the given URL (unix:///path/to/socket[?relative=<bool>] or http://<listen-address>:<listen-port>)`)
	args = programArgs{
		ConfigFile:  flag.String(configFileParam, "", "Use the given configuration file for the ETCD connection (overrides -endpoints)"),
		Endpoints:   flag.String(endpointsParam, defaultEndpointIPv6+"|"+defaultEndpointIPv4, "Use the endpoints configuration for ETCD connection"),
		DialTimeout: flag.Duration(dialTimeoutParam, defaultDialTimeout, "ETCD dial timeout"),
		Prefix:      flag.String(prefixParam, "", "Global key prefix"),
	}
	pdnsVersionArg := flag.String(pdnsVersionParam, "", "default PDNS version")
	logging := map[logrus.Level]*string{}
	for _, level := range logrus.AllLevels {
		logging[level] = flag.String(logParamPrefix+level.String(), "", fmt.Sprintf("Set logging level %s to the given components (separated by +)", level))
	}
	if err := flag.CommandLine.Parse(cmdLineArgs); err != nil { // same as flag.Parse(), but we can pass the arguments instead of being fixed to os.Args[1:] (needed for integration testing)
		log.main().Panicf("failed to parse command line arguments: %s", err)
	}
	for level, components := range logging {
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
	wg := new(sync.WaitGroup)
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
		if messages, err := setupClient(); err != nil {
			log.main().Panicf("setupClient() failed: %s", err)
		} else {
			log.main(messages).Debug("setupClient messages")
		}
		defer closeClient()
		if err = populateData(ctx, wg); err != nil {
			log.main().Panicf("populateData() failed: %s", err)
		}
		standalone(ctx, wg, u)
	} else {
		pipe(ctx, wg, os.Stdin, os.Stdout)
	}
	log.main().Debug("{main} request handler returned normally, stopping work")
	cancel()
	log.main().Trace("{main} waiting for child routines to finish (15s)")
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer waitCancel()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		log.main().Trace("{main} all finished, exiting")
	case <-waitCtx.Done():
		log.main().Error("{main} timeout while waiting on routines, exiting forcefully")
	}
}

func populateData(ctx context.Context, wg *sync.WaitGroup) error {
	log.main().Debug("populating data")
	getResponse, err := get(*args.Prefix, true, nil)
	if err != nil {
		return fmt.Errorf("get() failed: %s", err)
	}
	currentRevision = getResponse.Revision
	func() {
		dataRoot = newDataNode(nil, "", "")
		dataRoot.mutex.Lock()
		defer dataRoot.mutex.Unlock()
		dataRoot.reload(getResponse.DataChan)
		log.main("#records", dataRoot.recordsCount(), "#zones", dataRoot.zonesCount(), "revision", getResponse.Revision).Debug("loaded data")
	}()
	populated = true
	log.main().Debug("starting data watcher")
	wgGo(wg, func() {
		defer recoverPanics(func(v any) bool {
			recoverFunc(v, "watchData()", false)
			return false
		})
		watchData(ctx, *args.Prefix)
		log.main().Trace("watchData() returned normally")
	})
	return nil
}

func pipe(ctx context.Context, wg *sync.WaitGroup, in io.ReadCloser, out io.WriteCloser) {
	initialized := func(client *pdnsClient) []string {
		clientMessages, err := setupClient()
		if err != nil {
			client.Fatal(fmt.Errorf("setupClient() failed: %s", err))
		}
		log.etcd().Debugf("connected")
		if err := populateData(ctx, wg); err != nil {
			client.Fatal(fmt.Errorf("populateData() failed: %s", err))
		}
		return clientMessages
	}
	defer closeClient()
	serve(ctx, wg, newPdnsClient(ctx, "*", in, out), &initialized)
}

func serve(ctx context.Context, wg *sync.WaitGroup, client *pdnsClient, initialized *func(*pdnsClient) []string) {
	defer closeNoError(client.out)
	reqChan := startReadRequests(ctx, wg, client)
	if initialized != nil {
		// first request must be 'initialize'
		client.log.pdns().Trace("waiting for initial message")
		select {
		case <-ctx.Done():
			client.log.pdns().Trace("canceled while waiting for initial message")
			return
		case initRequest := <-reqChan:
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
		}
		logMessages := (*initialized)(client)
		client.Respond(makeResponse(true, logMessages...))
	}
	serving = true
REQUESTS:
	for {
		select {
		case <-ctx.Done():
			break REQUESTS
		case request, ok := <-reqChan:
			if !ok {
				break REQUESTS
			}
			handleRequest(&request, client)
		}
	}
}

func makeResponse(result any, msgs ...string) objectType[any] {
	response := objectType[any]{"result": result}
	if len(msgs) > 0 {
		response["log"] = msgs
	}
	return response
}
