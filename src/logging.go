/* Copyright 2016-2025 nix <https://keybase.io/nixn>

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
	"reflect"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

type logFormatter struct {
	msgPrefix string
	component string
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
	var arg1 string
	if standalone {
		arg1 = fmt.Sprintf("[%s]", time.Now().Format(time.StampMilli))
	} else {
		arg1 = fmt.Sprintf("pdns-etcd3[%d]", pid)
	}
	str := fmt.Sprintf("%s %-4s %s: %s%s", arg1, f.component, logLevelChars[entry.Level], f.msgPrefix, entry.Message)
	if len(entry.Data) > 0 {
		str += " |"
	}
	for k, v := range entry.Data {
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Pointer {
			if rv.IsNil() {
				str += fmt.Sprintf(" *%s=<nil>", k)
			} else {
				str += fmt.Sprintf(" *%s=%+v", k, rv.Elem())
			}
		} else if rv.Kind() == reflect.String {
			str += fmt.Sprintf(" %s=%q", k, v)
		} else {
			str += fmt.Sprintf(" %s=%+v", k, v)
		}
	}
	str += "\n"
	return []byte(str), nil
}

type logType map[string]*logrus.Logger

func newLog(msgPrefix string, components ...string) logType {
	newLogger := func(component string) *logrus.Logger {
		logger := logrus.New()
		logger.SetFormatter(&logFormatter{msgPrefix, component})
		return logger
	}
	log := logType{}
	for _, comp := range components {
		log[comp] = newLogger(comp)
	}
	return log
}

func (log *logType) main() *logrus.Logger {
	return (*log)["main"]
}

func (log *logType) pdns() *logrus.Logger {
	return (*log)["pdns"]
}

func (log *logType) etcd() *logrus.Logger {
	return (*log)["etcd"]
}

func (log *logType) data() *logrus.Logger {
	return (*log)["data"]
}

func (log *logType) setLoggingLevel(components string, level logrus.Level) {
	for _, component := range strings.Split(components, "+") {
		if logger, ok := (*log)[component]; ok {
			log.main().Printf("Setting log level of %s to %s", component, level)
			logger.SetLevel(level)
		} else {
			logFrom(log.main(), "component", component, "level", level).Warnf("setLoggingLevel(): invalid component")
		}
	}
}

func logFrom(logger *logrus.Logger, fieldsArgs ...any) *logrus.Entry {
	fields := logrus.Fields{}
	var name string
	for i, v := range fieldsArgs {
		if i%2 == 0 {
			if v, ok := v.(string); ok {
				name = v
			} else {
				name = fmt.Sprintf("%d", (i/2)+1)
			}
		} else {
			fields[name] = v
		}
	}
	return logger.WithFields(fields)
}
