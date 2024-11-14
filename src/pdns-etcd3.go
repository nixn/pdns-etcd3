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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/coreos/etcd/clientv3"
	"github.com/sirupsen/logrus"
)

type pdnsRequest struct {
	Method     string
	Parameters objectType
}

func (req *pdnsRequest) String() string {
	return fmt.Sprintf("%s: %+v", req.Method, req.Parameters)
}

var (
	// update this when changing data structure (only major/minor, patch is always 0)
	dataVersion = VersionType{IsDevelopment: true, Major: 1, Minor: 1}
)

var (
	pdnsVersion = defaultPdnsVersion
	prefix      = defaultPrefix
)
var (
	dataRoot     *dataNode
	dataRevision int64
)

func parseBoolean(s string) (bool, error) {
	s = strings.ToLower(s)
	for _, v := range []string{"y", "yes", "1", "true", "on"} {
		if s == v {
			return true, nil
		}
	}
	for _, v := range []string{"n", "no", "0", "false", "off"} {
		if s == v {
			return false, nil
		}
	}
	return false, fmt.Errorf("not a boolean string (y[es]/n[o], 1/0, true/false, on/off)")
}

type setParameterFunc func(value string) error

func readParameter(name string, params objectType, setParameter setParameterFunc) (bool, error) {
	if v, ok := params[name]; ok {
		if v, ok := v.(string); ok {
			if err := setParameter(v); err != nil {
				return true, fmt.Errorf("failed to set parameter '%s': %s", name, err)
			}
			return true, nil
		}
		return true, fmt.Errorf("parameter '%s' is not a string", name)
	}
	return false, nil
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

func setPdnsVersionParameter(param *int) setParameterFunc {
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

func setDurationParameterFunc(param *time.Duration, allowNegative bool, minValue time.Duration) setParameterFunc {
	return func(value string) error {
		dur, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("failed to parse value as duration: %s", err)
		}
		if !allowNegative && dur < 0 {
			return fmt.Errorf("negative durations not allowed")
		}
		testValue := dur
		if testValue < 0 {
			testValue = -testValue
		}
		if testValue < minValue {
			return fmt.Errorf("duration value %q less than minimum of %q", testValue, minValue)
		}
		*param = dur
		return nil
	}
}

func initialize(request *pdnsRequest) (logMessages []string, err error) {
	logMessages = []string(nil)
	// pdns-version
	if _, err = readParameter("pdns-version", request.Parameters, setPdnsVersionParameter(&pdnsVersion)); err != nil {
		return
	}
	logMessages = append(logMessages, fmt.Sprintf("pdns-version: %d", pdnsVersion))
	// prefix
	if _, err = readParameter("prefix", request.Parameters, setStringParameterFunc(&prefix)); err != nil {
		return
	}
	logMessages = append(logMessages, fmt.Sprintf("prefix: %q", prefix))
	// client
	if logMsgs, err2 := setupClient(request.Parameters); err2 == nil {
		logMessages = append(logMessages, logMsgs...)
	} else {
		err = err2
		return
	}
	// logging levels
	for _, level := range logrus.AllLevels {
		if _, err = readParameter(fmt.Sprintf("log-%s", level), request.Parameters, func(value string) error {
			setLoggingLevel(value, level)
			return nil
		}); err != nil {
			return
		}
	}
	return
}

func startReadRequests() <-chan pdnsRequest {
	dec := json.NewDecoder(os.Stdin)
	ch := make(chan pdnsRequest)
	go func() {
		defer close(ch)
		for {
			request := pdnsRequest{}
			if err := dec.Decode(&request); err != nil {
				if err == io.EOF {
					log.pdns.Debug("EOF on input stream, terminating")
					return
				}
				log.pdns.Fatal("Failed to decode request:", err)
			} else {
				log.pdns.WithField("request", request).Debug("received new request")
				ch <- request
			}
		}
	}()
	return ch
}

func handleRequest(request *pdnsRequest) {
	log.main.Debug("handling request:", request)
	since := time.Now()
	var result interface{}
	var err error
	switch strings.ToLower(request.Method) {
	case "lookup":
		result, err = lookup(request.Parameters)
	case "getalldomainmetadata":
		result, err = false, nil
	default:
		result, err = false, fmt.Errorf("unknown/unimplemented request: %s", request)
	}
	if err == nil {
		respond(result)
	} else {
		respond(result, err.Error())
	}
	dur := time.Since(since)
	log.main.WithFields(logrus.Fields{"dur": dur, "err": err, "val": result}).Trace("result")
}

func handleEvent(event *clientv3.Event) {
	log.etcd.WithField("event", event).Debug("handling event")
	entryKey := string(event.Kv.Key)
	name, entryType, qtype, id, version, err := parseEntryKey(entryKey)
	// check version first, because a new version could change the key syntax (but not prefix and version suffix)
	if version != nil && !dataVersion.isCompatibleTo(version) {
		return
	}
	if err != nil {
		log.data.WithError(err).Errorf("failed to parse entry key %q, ignoring event", entryKey)
		return
	}
	itemData := dataRoot.getChild(name, false)
	zoneData := itemData.findZone()
	if event.Type == clientv3.EventTypeDelete && qtype == "SOA" && id == "" && entryType == normalEntry && zoneData != nil && zoneData.parent != nil {
		// deleting the SOA record deletes the zone, so the parent zone must be reloaded instead. this results in a full data reload for top-level zones.
		zoneData = zoneData.parent.findZone()
	}
	if zoneData == nil {
		zoneData = dataRoot
	}
	getResponse, err := get(prefix+zoneData.prefixKey(), true, &event.Kv.ModRevision)
	if err != nil {
		log.data.WithError(err).Warnf("failed to get data for zone %q, not updating", zoneData.getQname())
		return
	}
	qname := zoneData.getQname()
	log.data.Tracef("reloading zone %q", qname)
	counts := zoneData.reload(getResponse.DataChan)
	dataRevision = event.Kv.ModRevision
	log.data.WithFields(logrus.Fields{"counts": counts, "dataRevision": dataRevision}).Debugf("reloaded zone %q and updated data revision", qname)
}

// Main is the "moved" program entrypoint, but with git version argument (which is set in real main package)
func Main(programVersion VersionType, gitVersion string) {
	// TODO handle arguments, f.e. 'show-defaults' standalone command
	initLogging()
	releaseVersion := programVersion.String() + "+" + dataVersion.String()
	if "v"+releaseVersion != gitVersion {
		releaseVersion += fmt.Sprintf("[%s]", gitVersion)
	}
	log.main.Printf("pdns-etcd3 %s, Copyright © 2016-2024 nix <https://keybase.io/nixn>", releaseVersion)
	var logMessages []string
	reqChan := startReadRequests()
	// first request must be 'initialize'
	{
		initRequest := <-reqChan
		log.pdns.WithField("request", initRequest).Traceln("Received request")
		if initRequest.Method != "initialize" {
			log.pdns.WithField("method", initRequest.Method).Fatalln("Wrong request method (waited for 'initialize')")
		}
		log.main.WithField("parameters", initRequest.Parameters).Info("initializing")
		initMessages, err := initialize(&initRequest)
		if err != nil {
			fatal(err)
		}
		logMessages = append(logMessages, initMessages...)
	}
	defer closeClient()
	doneCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dataRoot = newDataNode(nil, "", "")
	// populate data
	{
		getResponse, err := get(prefix, true, nil)
		if err != nil {
			fatal(err)
		}
		dataRevision = getResponse.Revision
		counts := dataRoot.reload(getResponse.DataChan)
		logMessages = append(logMessages, fmt.Sprintf("counts=%+v", counts), fmt.Sprintf("revision=%v", dataRevision))
	}
	log.main.Debugln("starting data watcher")
	eventsChan := startWatchData(doneCtx, dataRevision+1)
	log.main.Infoln("initialized.", strings.Join(logMessages, ". "))
	respond(true, logMessages...)
	// main loop
	for {
		select {
		case event := <-eventsChan:
			handleEvent(event)
		case request, ok := <-reqChan:
			if ok {
				handleRequest(&request)
			} else {
				break
			}
		}
	}
}

func makeResponse(result interface{}, msgs ...string) objectType {
	response := objectType{"result": result}
	if len(msgs) > 0 {
		response["log"] = msgs
	}
	return response
}

func respond(result interface{}, msgs ...string) {
	response := makeResponse(result, msgs...)
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(&response); err != nil {
		log.pdns.WithError(err).WithField("response", response).Fatal("failed to encode response")
	}
}

func fatal(msg interface{}) {
	s := fmt.Sprintf("%s", msg)
	respond(false, s)
	log.main.Fatal("Fatal error:", s)
}
