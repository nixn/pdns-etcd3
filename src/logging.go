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
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

type logFormatter struct {
	msgPrefix string
	component string
}

var logLevelChars = map[logrus.Level]string{
	logrus.PanicLevel: "FTL", // this is a public-facing string and panics are used to exit gracefully and get reasons in upper levels for fatal errors, so just name it "FTL" too
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
		str += " "
		if k != "" {
			str += fmt.Sprintf("%s=", k)
		}
		str += val2str(v)
	}
	str += "\n"
	return []byte(str), nil
}

type logType map[string]*logrus.Logger
type logFatal struct { // TODO remove after having migrated all log.Fatal calls
	code      int
	component string
	clientID  *string
}

func newLog(clientID *string, components ...string) logType {
	msgPrefix := ""
	if clientID != nil {
		msgPrefix = fmt.Sprintf("[%s] ", *clientID)
	}
	newLogger := func(component string) *logrus.Logger {
		logger := logrus.New()
		logger.SetFormatter(&logFormatter{msgPrefix, component})
		logger.ExitFunc = func(code int) {
			panic(logFatal{code, component, clientID})
		}
		return logger
	}
	log := logType{}
	for _, comp := range components {
		log[comp] = newLogger(comp)
	}
	return log
}

func (log *logType) logger(component string) *logrus.Logger {
	return (*log)[component]
}

func (log *logType) main(fields ...any) *logrus.Entry {
	return logFrom(log.logger("main"), fields...)
}

// TODO add "conn" component (for connectors: pipe, unix, http)

func (log *logType) pdns(fields ...any) *logrus.Entry {
	return logFrom(log.logger("pdns"), fields...)
}

func (log *logType) etcd(fields ...any) *logrus.Entry {
	return logFrom(log.logger("etcd"), fields...)
}

func (log *logType) data(fields ...any) *logrus.Entry {
	return logFrom(log.logger("data"), fields...)
}

func (log *logType) setLoggingLevel(components string, level logrus.Level) {
	for _, component := range strings.Split(components, "+") {
		if logger, ok := (*log)[component]; ok {
			log.main().Printf("setting log level of %s to %s", component, level)
			logger.SetLevel(level)
		} else {
			log.main("component", component, "level", level).Warnf("setLoggingLevel(): invalid component")
		}
	}
}

func logFrom(logger *logrus.Logger, fieldsArgs ...any) *logrus.Entry {
	fields := logrus.Fields{}
	var name *string
	if len(fieldsArgs) == 1 {
		s := ""
		name = &s
	}
	n := 1
	for _, v := range fieldsArgs {
		if s, ok := v.(string); ok && name == nil {
			name = &s
		} else if name != nil {
			fields[*name] = v
			name = nil
			n++
		} else {
			fields[fmt.Sprintf("%d", n)] = v
			n++
		}
	}
	if name != nil {
		fields[fmt.Sprintf("%d", n)] = *name
	}
	return logger.WithFields(fields)
}
