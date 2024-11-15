/* Copyright 2016-2024 nix <https://keybase.io/nixn>

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

	"github.com/coreos/etcd/clientv3"
	"github.com/sirupsen/logrus"
)

type pdnsRequest struct {
	Method     string
	Parameters objectType[any]
}

func (req *pdnsRequest) String() string {
	return fmt.Sprintf("%s: %+v", req.Method, req.Parameters)
}

var (
	// update this when changing data structure (only major/minor, patch is always 0)
	dataVersion = VersionType{IsDevelopment: true, Major: 1, Minor: 1}
)

type programArgs struct {
	ConfigFile  *string
	Endpoints   *string
	DialTimeout *time.Duration
	PdnsVersion *uint
	Prefix      *string
	Logging     map[logrus.Level]*string
}

var (
	args       programArgs
	standalone bool
	dataRoot   *dataNode
	events     <-chan *clientv3.Event
)

func parseBoolean(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "y", "yes", "1", "true", "on":
		return true, nil
	case "n", "no", "0", "false", "off":
		return false, nil
	default:
		return false, fmt.Errorf("not a boolean string (y[es]/n[o], 1/0, true/false, on/off)")
	}
}

type setParameterFunc func(value string) error

func readParameter(name string, params objectType[string], setParameter setParameterFunc) error {
	if v, ok := params[name]; ok {
		if err := setParameter(v); err != nil {
			return fmt.Errorf("failed to set parameter '%s': %s", name, err)
		}
		return nil
	}
	return nil
}

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

func setStringParameterFunc(param *string) setParameterFunc {
	return func(value string) error {
		*param = value
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

func readParameters(params objectType[string]) error {
	for k := range params {
		var err error
	SWITCH:
		switch {
		case k == configFileParam:
			err = readParameter(k, params, setStringParameterFunc(args.ConfigFile))
		case k == endpointsParam:
			err = readParameter(k, params, setStringParameterFunc(args.Endpoints))
		case k == dialTimeoutParam:
			err = readParameter(k, params, setDurationParameterFunc(args.DialTimeout, &minimumDialTimeout))
		case k == pdnsVersionParam:
			err = readParameter(k, params, setPdnsVersionParameter(args.PdnsVersion))
		case k == prefixParam:
			err = readParameter(k, params, setStringParameterFunc(args.Prefix))
		case startsWith(k, logParamPrefix):
			for _, level := range logrus.AllLevels {
				if k == logParamPrefix+level.String() {
					err = readParameter(k, params, setStringParameterFunc(args.Logging[level]))
					break SWITCH
				}
			}
			err = fmt.Errorf("invalid log level parameter: %s", k)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func configureLogging() {
	for level, components := range args.Logging {
		if len(*components) > 0 {
			setLoggingLevel(*components, level)
		}
	}
}

func startReadRequests(comm *commType[pdnsRequest]) <-chan pdnsRequest {
	ch := make(chan pdnsRequest)
	go func() {
		defer close(ch)
		for {
			if request, err := comm.read(); err != nil {
				if err == io.EOF {
					log.pdns.Debug("EOF on input stream, terminating")
					return
				}
				log.pdns.Fatal("Failed to decode request:", err)
			} else {
				log.pdns.WithField("request", request).Debug("received new request")
				ch <- *request
			}
		}
	}()
	return ch
}

func handleRequest[T any](request *pdnsRequest, comm *commType[T]) {
	log.main.Debug("handling request:", request)
	since := time.Now()
	var result interface{}
	var err error
	switch strings.ToLower(request.Method) {
	case "lookup":
		result, err = lookup(request.Parameters)
	case "getalldomainmetadata":
		result, err = map[string]any{}, nil
	default:
		result, err = false, fmt.Errorf("unknown/unimplemented request: %s", request)
	}
	if err == nil {
		respond(comm, result)
	} else {
		respond(comm, result, err.Error())
	}
	dur := time.Since(since)
	log.main.WithFields(logrus.Fields{"dur": dur, "err": err, "val": result}).Trace("result")
}

func handleEvent(event *clientv3.Event) {
	log.etcd.WithField("event", event).Debug("handling event")
	since := time.Now()
	entryKey := string(event.Kv.Key)
	name, entryType, qtype, id, version, err := parseEntryKey(entryKey)
	// check version first, because a new version could change the key syntax (but not prefix and version suffix)
	if version != nil && !dataVersion.isCompatibleTo(version) {
		log.data.Tracef("ignoring event on version incompatible entry: %s", entryKey)
		return
	}
	if err != nil {
		log.data.WithError(err).Errorf("failed to parse entry key %q, ignoring event", entryKey)
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
		log.data.WithError(err).Warnf("failed to get data for zone %q, not updating", zoneData.getQname())
		return
	}
	qname := zoneData.getQname()
	log.data.Tracef("reloading zone %q", qname)
	zoneData.mutex.RUnlock()
	if zoneData.parent != nil {
		defer zoneData.parent.rUnlockUpwards(nil)
	}
	zoneData.mutex.Lock()
	defer zoneData.mutex.Unlock()
	zoneData.reload(getResponse.DataChan)
	dur := time.Since(since)
	logFrom(log.data, "#records", zoneData.recordsCount(), "#zones", zoneData.zonesCount(), "dataRevision", maxOf(event.Kv.ModRevision, event.Kv.CreateRevision), "event-duration", dur).Debugf("reloaded zone %q and updated data revision", qname)
}

// Main is the "moved" program entrypoint, but with git version argument (which is set in real main package)
func Main(programVersion VersionType, gitVersion string) {
	initLogging()
	releaseVersion := programVersion.String() + "+" + dataVersion.String()
	if "v"+releaseVersion != gitVersion {
		releaseVersion += fmt.Sprintf("[%s]", gitVersion)
	}
	log.main.Printf("pdns-etcd3 %s, Copyright © 2016-2024 nix <https://keybase.io/nixn>", releaseVersion)
	// handle arguments // TODO handle more arguments, f.e. 'show-defaults' standalone command
	unixSocketPath := flag.String("unix", "", `Create a unix socket at given path and run in Unix Connector mode ("standalone")`)
	args = programArgs{
		ConfigFile:  flag.String(configFileParam, "", "Use the given configuration file for the ETCD connection (overrides -endpoints)"),
		Endpoints:   flag.String(endpointsParam, defaultEndpointIPv6+"|"+defaultEndpointIPv4, "Use the endpoints configuration for ETCD connection"),
		DialTimeout: flag.Duration(dialTimeoutParam, defaultDialTimeout, "ETCD dial timeout"),
		PdnsVersion: flag.Uint(pdnsVersionParam, defaultPdnsVersion, "PowerDNS version (required for right protocol)"),
		Prefix:      flag.String(prefixParam, "", "Global key prefix"),
		Logging:     map[logrus.Level]*string{},
	}
	for _, level := range logrus.AllLevels {
		args.Logging[level] = flag.String(logParamPrefix+level.String(), "", fmt.Sprintf("Set logging level %s to the given components (separated by +)", level))
	}
	flag.Parse()
	standalone = unixSocketPath != nil && *unixSocketPath != ""
	if standalone {
		configureLogging()
		socket, err := net.Listen("unix", *unixSocketPath)
		if err != nil {
			log.main.Fatalf("Failed to create a unix socket at %s: %s", *unixSocketPath, err)
		}
		defer socket.Close()
		err = os.Chmod(*unixSocketPath, 0777)
		if err != nil {
			log.main.Warnf("Failed to chmod unix socket to 0777")
		}
		go unix(socket)
	} else {
		go pipe()
	}
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, os.Kill, syscall.SIGTERM)
	log.main.Debug("main: waiting for signal...")
	sig := <-c
	log.main.Debugf("main: caught signal %s, shutting down", sig)
}

func populateData() (context.CancelFunc, error) {
	log.main.Debugf("populating data")
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
		log.main.Debugf("loaded data: #records=%d #zones=%d revision=%v", dataRoot.recordsCount(), dataRoot.zonesCount(), getResponse.Revision)
	}()
	log.main.Debug("starting data watcher")
	events = startWatchData(doneCtx, getResponse.Revision+1)
	return cancel, nil
}

func unix(socket net.Listener) {
	connectMessages, err := setupClient()
	if err != nil {
		log.main.Fatalf("setupClient() failed: %s", err)
	}
	defer closeClient()
	log.main.WithError(err).Debug("setupClient: ", strings.Join(connectMessages, "; "))
	cancel, err := populateData()
	if err != nil {
		log.main.Fatalf("populateData() failed: %s", err)
	}
	defer cancel()
	log.main.Info("Waiting for connections")
	for {
		conn, err := socket.Accept()
		if err != nil {
			log.main.Errorf("Failed to accept new connection: %s", err)
			continue
		}
		log.main.Debugf("New connection from %s", conn)
		go serve(conn, conn)
	}
}

func pipe() {
	serve(os.Stdin, os.Stdout)
}

func serve(in io.Reader, out io.Writer) {
	comm := newComm[pdnsRequest](in, out)
	var logMessages []string
	reqChan := startReadRequests(comm)
	// first request must be 'initialize'
	{
		log.pdns.Debug("Waiting for initial request")
		initRequest := <-reqChan
		log.pdns.WithField("request", initRequest).Traceln("Received request")
		if initRequest.Method != "initialize" {
			log.pdns.WithField("method", initRequest.Method).Fatal("Wrong request method (waited for 'initialize')")
		}
		log.main.WithField("parameters", initRequest.Parameters).Info("initializing")
		params := objectType[string]{}
		for k, v := range initRequest.Parameters {
			params[k] = v.(string)
		}
		err := readParameters(params)
		if err != nil {
			fatal(comm, err)
		}
		log.main.Debug("successfully read parameters")
	}
	if !standalone {
		configureLogging()
		clientMessages, err := setupClient()
		if err != nil {
			fatal(comm, fmt.Errorf("setupClient() failed: %s", err))
		}
		defer closeClient()
		log.main.Debug("connected")
		logMessages = append(logMessages, clientMessages...)
		cancel, err := populateData()
		if err != nil {
			fatal(comm, fmt.Errorf("populateData() failed: %s", err))
		}
		defer cancel()
	}
	respond(comm, true, logMessages...)
	for {
		select {
		case event := <-events:
			handleEvent(event)
		case request, ok := <-reqChan:
			if ok {
				handleRequest(&request, comm)
			} else {
				return
			}
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

func respond[T any](comm *commType[T], result any, msgs ...string) {
	response := makeResponse(result, msgs...)
	log.pdns.WithField("response", response).Tracef("response")
	if err := comm.write(response); err != nil {
		log.pdns.WithError(err).WithField("response", response).Fatal("failed to encode response")
	}
}

func fatal[T any](comm *commType[T], msg any) {
	s := fmt.Sprintf("%s", msg)
	respond(comm, false, s)
	log.main.Fatal("Fatal error:", s)
}
