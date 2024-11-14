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
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
)

type logFormatter struct {
	Component string
}

var logLevelChars = map[logrus.Level]string{
	logrus.PanicLevel: "PNC",
	logrus.FatalLevel: "FTL",
	logrus.ErrorLevel: "ERR",
	logrus.WarnLevel:  "WRN",
	logrus.InfoLevel:  "INF",
	logrus.DebugLevel: "DBG",
	logrus.TraceLevel: "TRC",
}

func (f *logFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	str := fmt.Sprintf("pdns-etcd3[%d] %-4s %s: %s", pid, f.Component, logLevelChars[entry.Level], entry.Message)
	for k, v := range entry.Data {
		str += fmt.Sprintf(" %s=%+v", k, v)
	}
	str += "\n"
	return []byte(str), nil
}

var log struct {
	main *logrus.Logger
	pdns *logrus.Logger
	etcd *logrus.Logger
	data *logrus.Logger
}
var loggers = map[string]*logrus.Logger{}

func initLogging() {
	log.main = logrus.New()
	log.pdns = logrus.New()
	log.etcd = logrus.New()
	log.data = logrus.New()
	loggers["main"] = log.main
	loggers["pdns"] = log.pdns
	loggers["etcd"] = log.etcd
	loggers["data"] = log.data
	for component, logger := range loggers {
		logger.SetFormatter(&logFormatter{Component: component})
	}
}

func setLoggingLevel(components string, level logrus.Level) {
	for _, component := range strings.Split(components, "+") {
		if logger, ok := loggers[component]; ok {
			log.main.Printf("Setting log level of %s to %s", component, level)
			logger.SetLevel(level)
		} else {
			log.main.WithFields(logrus.Fields{"component": component, "level": level}).Warn("setLoggingLevel(): invalid component")
		}
	}
}
