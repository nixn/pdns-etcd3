/* Copyright 2016 nix <https://github.com/nixn>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. */

package main

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
	Parameters map[string]interface{}
}

func (req *pdnsRequest) String() string {
	return fmt.Sprintf("%s: %+v", req.Method, req.Parameters)
}

var version = "?"

var (
	pdnsVersion = 3
	prefix      = ""
)

type setParameterFunc func(value string) error

func readParameter(name string, params map[string]interface{}, setParameter setParameterFunc) (bool, error) {
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

func main() {
	log.SetPrefix(fmt.Sprintf("pdns-etcd3[%d]: ", os.Getpid()))
	log.SetFlags(0)
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	var request pdnsRequest
	if err := dec.Decode(&request); err != nil {
		log.Fatalln("Failed to decode JSON:", err)
	}
	if request.Method != "initialize" {
		log.Fatalln("Waited for 'initialize', got:", request.Method)
	}
	logMessages := []string{fmt.Sprintf("v:%s", version)}
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
	// client
	if logMsgs, err := setupClient(request.Parameters); err != nil {
		fatal(enc, err.Error())
	} else {
		logMessages = append(logMessages, logMsgs...)
	}
	defer closeClient()
	// TODO check storage version
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
			log.Println("result:", result)
			respond(enc, result)
		} else {
			log.Println("error:", err)
			respond(enc, result, err.Error())
		}
		dur := time.Since(since)
		log.Println("request dur:", dur)
	}
}

func makeResponse(result interface{}, msg ...string) map[string]interface{} {
	response := map[string]interface{}{"result": result}
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
