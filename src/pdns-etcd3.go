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
	"net"
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

var (
	log        = newLog("", "main", "etcd", "data") // TODO timings
	args       programArgs
	standalone bool
	dataRoot   *dataNode
)

func parseBoolean(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "y", "yes", "1", "t", "true", "on":
		return true, nil
	case "n", "no", "0", "f", "false", "off":
		return false, nil
	default:
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

func startReadRequests(client *pdnsClient) <-chan pdnsRequest {
	ch := make(chan pdnsRequest)
	go func() {
		defer close(ch)
		for {
			if request, err := client.Comm.read(); err != nil {
				if err == io.EOF {
					client.log.pdns().Debug("EOF on input stream, terminating")
					return
				}
				client.log.pdns().Fatalf("failed to decode request: %s", err)
			} else {
				client.log.pdns("request", request).Debug("received new request")
				ch <- *request
			}
		}
	}()
	return ch
}

func handleRequest(request *pdnsRequest, client *pdnsClient) {
	client.log.main("request", request).Debug("handling request")
	since := time.Now()
	var result interface{}
	var err error
	switch strings.ToLower(request.Method) {
	case "lookup":
		result, err = lookup(request.Parameters, client)
	case "getalldomainmetadata":
		result, err = map[string]any{}, nil
	case "getalldomains":
		result = dataRoot.allDomains(nil)
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

func handleEvent(event *clientv3.Event) {
	entryKey := string(event.Kv.Key)
	log.etcd("event", event).Debugf("handling event on %q", entryKey)
	since := time.Now()
	name, entryType, qtype, id, version, err := parseEntryKey(entryKey)
	// check version first, because a new version could change the key syntax (but not prefix and version suffix)
	if version != nil && !dataVersion.IsCompatibleTo(*version, false) {
		log.data().Tracef("ignoring event on version-incompatible entry %q", entryKey)
		return
	}
	if err != nil {
		log.data().Errorf("failed to parse entry key %q (ignoring event): %s", entryKey, err)
		return
	}
	itemData := dataRoot.getChild(name, true)
	zoneData := itemData.findZone()
	if event.Type == clientv3.EventTypeDelete && qtype == "SOA" && id == "" && entryType == normalEntry && zoneData != nil && zoneData.parent != nil {
		// deleting the SOA record deletes the zone, so the parent zone must be reloaded instead. this results in a full data reload for top-level zones.
		zoneData = zoneData.parent.findZone()
	}
	if zoneData == nil {
		zoneData = dataRoot
	}
	itemData.rUnlockUpwards(zoneData)
	getResponse, err := get(*args.Prefix+zoneData.prefixKey(), true, &event.Kv.ModRevision)
	if err != nil {
		zoneData.rUnlockUpwards(nil)
		log.data().WithError(err).Warnf("failed to get data for zone %q, not updating", zoneData.getQname())
		return
	}
	qname := zoneData.getQname()
	log.data().Debugf("reloading zone %q", qname)
	zoneData.mutex.RUnlock()
	if zoneData.parent != nil {
		defer zoneData.parent.rUnlockUpwards(nil)
	}
	zoneData.mutex.Lock()
	defer zoneData.mutex.Unlock()
	zoneData.reload(getResponse.DataChan)
	dur := time.Since(since)
	log.data("#records", zoneData.recordsCount(), "#zones", zoneData.zonesCount(), "data-revision", maxOf(event.Kv.ModRevision, event.Kv.CreateRevision), "event-duration", dur).Debugf("reloaded zone %q", qname)
}

// Main is the "moved" program entrypoint, but with git version argument (which is set in real main package)
func Main(programVersion VersionType, gitVersion string, cmdLineArgs []string) {
	releaseVersion := programVersion.String() + "+" + dataVersion.String()
	if "v"+releaseVersion != gitVersion {
		releaseVersion += fmt.Sprintf("[%s]", gitVersion)
	}
	log.main().Printf("pdns-etcd3 %s, Copyright © 2016-2026 nix <https://keybase.io/nixn>", releaseVersion)
	// handle arguments // TODO handle more arguments, f.e. 'show-defaults' standalone command
	unixSocketPath := flag.String("unix", "", `Create a unix socket at given path and run in Unix Connector mode ("standalone")`)
	args = programArgs{
		ConfigFile:  flag.String(configFileParam, "", "Use the given configuration file for the ETCD connection (overrides -endpoints)"),
		Endpoints:   flag.String(endpointsParam, defaultEndpointIPv6+"|"+defaultEndpointIPv4, "Use the endpoints configuration for ETCD connection"),
		DialTimeout: flag.Duration(dialTimeoutParam, defaultDialTimeout, "ETCD dial timeout"),
		Prefix:      flag.String(prefixParam, "", "Global key prefix"),
	}
	logging := map[logrus.Level]*string{}
	for _, level := range logrus.AllLevels {
		logging[level] = flag.String(logParamPrefix+level.String(), "", fmt.Sprintf("Set logging level %s to the given components (separated by +)", level))
	}
	_ = flag.CommandLine.Parse(cmdLineArgs) // same as flag.Parse(), but we can pass the arguments instead of being fixed to os.Args[1:] (needed for integration testing)
	standalone = unixSocketPath != nil && *unixSocketPath != ""
	if standalone {
		for level, components := range logging {
			if len(*components) > 0 {
				log.setLoggingLevel(*components, level)
			}
		}
		//goland:noinspection GoDfaNilDereference
		socket, err := net.Listen("unix", *unixSocketPath)
		if err != nil {
			log.main().Fatalf("Failed to create a unix socket at %s: %s", *unixSocketPath, err)
		}
		defer func(socket net.Listener) { _ = socket.Close() }(socket)
		err = os.Chmod(*unixSocketPath, 0777)
		if err != nil {
			log.main().Warnf("Failed to chmod unix socket to 0777: %s", err)
		}
		go unix(socket)
	} else {
		go pipe()
	}
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, os.Kill, syscall.SIGTERM)
	log.main().Debugf("{main} waiting for shutdown signal")
	sig := <-c
	log.main().Debugf("{main} caught signal %s, shutting down", sig)
	// TODO implement graceful shutdown. when calling fatal (or log.Fatal), the deferred functions are not executed :-(
}

func populateData(caller string) (context.CancelFunc, error) {
	log.main().Debugf("{%s} populating data", caller)
	doneCtx, cancel := context.WithCancel(context.Background())
	getResponse, err := get(*args.Prefix, true, nil)
	if err != nil {
		return cancel, fmt.Errorf("get() failed: %s", err)
	}
	func() {
		dataRoot = newDataNode(nil, "", "")
		dataRoot.mutex.Lock()
		defer dataRoot.mutex.Unlock()
		dataRoot.reload(getResponse.DataChan)
		log.main().Debugf("{%s} loaded data: #records=%d #zones=%d revision=%v", caller, dataRoot.recordsCount(), dataRoot.zonesCount(), getResponse.Revision)
	}()
	log.main().Debugf("{%s} starting data watcher", caller)
	go watchData(doneCtx, getResponse.Revision+1)
	return cancel, nil
}

func unix(socket net.Listener) {
	connectMessages, err := setupClient()
	if err != nil {
		log.main().Fatalf("{listen} setupClient() failed: %s", err)
	}
	defer closeClient()
	log.main().WithError(err).Debug("{listen} setupClient: ", strings.Join(connectMessages, "; "))
	cancel, err := populateData("listen")
	if err != nil {
		log.main().Fatalf("{listen} populateData() failed: %s", err)
	}
	defer cancel()
	log.main().Infof("{listen} Waiting for connections")
	var nextClientID uint = 1
	for {
		conn, err := socket.Accept()
		if err != nil {
			log.main().Errorf("Failed to accept new connection: %s", err)
			continue
		}
		log.main().Debugf("{listen} New connection [%d]: %+v", nextClientID, conn)
		go serve(newPdnsClient(nextClientID, conn, conn))
		nextClientID++
	}
}

func pipe() {
	serve(newPdnsClient(0, os.Stdin, os.Stdout))
}

func serve(client *pdnsClient) {
	var logMessages []string
	reqChan := startReadRequests(client)
	// first request must be 'initialize'
	{
		client.log.pdns().Debug("waiting for initial request")
		initRequest := <-reqChan
		if initRequest.Method != "initialize" {
			client.log.pdns().WithField("method", initRequest.Method).Fatal("wrong request method (waited for 'initialize')")
		}
		client.log.main().WithField("parameters", initRequest.Parameters).Info("initializing")
		params := objectType[string]{}
		for k, v := range initRequest.Parameters {
			params[k] = v.(string)
		}
		err := readParameters(params, client)
		if err != nil {
			fatal(client, err)
		}
		client.log.main().Debugf("successfully read parameters")
	}
	if !standalone {
		clientMessages, err := setupClient()
		if err != nil {
			fatal(client, fmt.Errorf("setupClient() failed: %s", err))
		}
		defer closeClient()
		client.log.main().Debugf("connected")
		logMessages = append(logMessages, clientMessages...)
		cancel, err := populateData("serve")
		if err != nil {
			fatal(client, fmt.Errorf("populateData() failed: %s", err))
		}
		defer cancel()
	}
	client.Respond(makeResponse(true, logMessages...))
REQUESTS:
	for {
		request, ok := <-reqChan
		if !ok {
			_ = client.out.Close()
			break
		}
		handleRequest(&request, client)
	}
}

func makeResponse(result any, msgs ...string) objectType[any] {
	response := objectType[any]{"result": result}
	if len(msgs) > 0 {
		response["log"] = msgs
	}
	return response
}

func fatal(client *pdnsClient, msg any) {
	s := fmt.Sprintf("%s", msg)
	client.respond(makeResponse(false, s))
	client.log.main().Fatalf("Fatal error: %s", s)
}
