/* Copyright 2016-2020 nix <https://keybase.io/nixn>

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
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"
)

type pdnsRequest struct {
	Method     string
	Parameters objectType
}

func (req *pdnsRequest) String() string {
	return fmt.Sprintf("%s: %+v", req.Method, req.Parameters)
}

var (
	dataVersion    = versionType{true, 1, 0} // update this when changing data structure
	programVersion = versionType{true, 1, 0} // update this before a new release
)

var (
	pdnsVersion   = defaultPdnsVersion
	prefix        = defaultPrefix
	reversedNames = defaultReversedNames
	minCacheTime  = defaultMinCacheTime
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

// Main is the "moved" program entrypoint, but with git version argument (which is set in real main package)
func Main(gitVersion string) {
	// TODO handle arguments, f.e. 'show-defaults' standalone command
	log.SetPrefix(fmt.Sprintf("pdns-etcd3[%d]: ", os.Getpid()))
	log.SetFlags(0)
	releaseVersion := fmt.Sprintf("%s/%s", &dataVersion, &programVersion)
	if "v"+releaseVersion != gitVersion {
		releaseVersion += fmt.Sprintf("+%s", gitVersion)
	}
	log.Printf("pdns-etcd3 %s, Copyright © 2016-2020 nix <https://keybase.io/nixn>", releaseVersion)
	var err error
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	var request pdnsRequest
	if err = dec.Decode(&request); err != nil {
		log.Fatalln("Failed to decode JSON:", err)
	}
	if request.Method != "initialize" {
		log.Fatalln("Waited for 'initialize', got:", request.Method)
	}
	logMessages := []string(nil)
	// pdns-version
	if _, err := readParameter("pdns-version", request.Parameters, setPdnsVersionParameter(&pdnsVersion)); err != nil {
		fatal(enc, err)
	}
	logMessages = append(logMessages, fmt.Sprintf("pdns-version: %d", pdnsVersion))
	// prefix
	if _, err := readParameter("prefix", request.Parameters, setStringParameterFunc(&prefix)); err != nil {
		fatal(enc, err)
	}
	logMessages = append(logMessages, fmt.Sprintf("prefix: %q", prefix))
	// reversed-names
	if _, err := readParameter("reversed-names", request.Parameters, setBooleanParameterFunc(&reversedNames)); err != nil {
		fatal(enc, err)
	}
	logMessages = append(logMessages, fmt.Sprintf("reversed-names: %v", reversedNames))
	// min-cache-time
	if _, err := readParameter("min-cache-time", request.Parameters, setDurationParameterFunc(&minCacheTime, false, 0)); err != nil {
		fatal(enc, err)
	}
	logMessages = append(logMessages, fmt.Sprintf("min-cache-time: %s", minCacheTime))
	// client
	if logMsgs, err := setupClient(request.Parameters); err != nil {
		fatal(enc, err.Error())
	} else {
		logMessages = append(logMessages, logMsgs...)
	}
	defer closeClient()
	dataCache = newDataCache(0, time.Time{})
	respond(enc, true, logMessages...)
	log.Println("initialized.", strings.Join(logMessages, ". "))
	// main loop
	for {
		request := pdnsRequest{}
		if err := dec.Decode(&request); err != nil {
			if err == io.EOF {
				log.Println("EOF on input stream, terminating")
				break
			}
			log.Fatalln("Failed to decode request:", err)
		}
		log.Println("request:", request)
		since := time.Now()
		var result interface{}
		var err error
		switch strings.ToLower(request.Method) {
		case "lookup":
			result, err = lookup(request.Parameters)
		default:
			result, err = false, fmt.Errorf("unknown/unimplemented request: %s", request)
		}
		if err == nil {
			respond(enc, result)
		} else {
			respond(enc, result, err.Error())
		}
		dur := time.Since(since)
		log.Printf("result: %v [err %v] [dur %s]", result, err, dur)
	}
}

func makeResponse(result interface{}, msg ...string) objectType {
	response := objectType{"result": result}
	if len(msg) > 0 {
		response["log"] = msg
	}
	return response
}

func respond(enc *json.Encoder, result interface{}, msg ...string) {
	response := makeResponse(result, msg...)
	if err := enc.Encode(&response); err != nil {
		log.Fatalln("Failed to encode response", response, ":", err)
	}
}

func fatal(enc *json.Encoder, msg interface{}) {
	s := fmt.Sprintf("%s", msg)
	respond(enc, false, s)
	log.Fatalln("Fatal error:", s)
}
