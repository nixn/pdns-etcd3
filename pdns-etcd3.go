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
  "errors"
  "fmt"
  "log"
  "time"
  "os"
  "io"
  "encoding/json"
  "strings"
  "regexp"
  "golang.org/x/net/context"
  "github.com/coreos/etcd/clientv3"
)

type pdnsRequest struct {
  Method string
  Parameters map[string]interface{}
}

func (req *pdnsRequest) AsString() string {
  return fmt.Sprintf("%s: %+v", req.Method, req.Parameters)
}

var (
  cli *clientv3.Client
  timeout = 2 * time.Second
  prefix = ""
)

var (
  zone2id = map[string]int32{}
  id2zone = map[int32]string{}
  nextZoneId int32 = 1
)

var defaults struct {
  revision int64
  what2values map[string]map[string]interface{} // what = "example.net" or "example.net/subdomain" or "example.net/[subdomain/]RR" => values
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
  logMessages := []string{}
  if pfx, ok := request.Parameters["prefix"]; ok {
    if pfx, ok := pfx.(string); ok {
      if len(pfx) > 0 && !strings.HasPrefix(pfx, "/") {
        fatal(enc, "parameters.prefix does not start with a slash (\"/\")")
      }
      pfx = strings.TrimRight(pfx, "/")
      re := regexp.MustCompile("//+")
      prefix = re.ReplaceAllString(pfx, "/")
    } else {
      fatal(enc, "parameters.prefix is not a string")
    }
  }
  logMessages = append(logMessages, fmt.Sprintf("prefix: %s", prefix))
  if configFile, ok := request.Parameters["configFile"]; ok {
    if configFile, ok := configFile.(string); ok {
      if client, err := clientv3.NewFromConfigFile(configFile); err == nil {
        cli = client
      } else {
        fatal(enc, "Failed to create client instance: " + err.Error())
      }
    } else {
      fatal(enc, "parameters.configFile is not a string")
    }
  } else {
    cfg := clientv3.Config{DialTimeout: timeout}
    // timeout
    if tmo, ok := request.Parameters["timeout"]; ok {
      if tmo, ok := tmo.(string); ok {
        if tmo, err := time.ParseDuration(tmo); err == nil {
          if tmo > 0 {
            cfg.DialTimeout = tmo
            timeout = tmo
          } else {
            fatal(enc, "Non-positive timeout value")
          }
        } else {
          fatal(enc, "Failed to parse timeout value")
        }
      } else {
        fatal(enc, "parameters.timeout is not a string")
      }
    }
    // endpoints
    if endpoints, ok := request.Parameters["endpoints"]; ok {
      if endpoints, ok := endpoints.(string); ok {
        endpoints := strings.Split(endpoints, "|")
        cfg.Endpoints = endpoints
        if client, err := clientv3.New(cfg); err == nil {
          cli = client
        } else {
          fatal(enc, err.Error())
        }
      } else {
        fatal(enc, "parameters.endpoints is not a string")
      }
    } else {
      fatal(enc, "Missing parameters.endpoints")
    }
  }
  defer cli.Close()
  // TODO check storage version
  respond(enc, true, logMessages...)
  log.Println("initialized.", strings.Join(logMessages, ". "))
  // main loop
  for {
    request := pdnsRequest{}
    if err := dec.Decode(&request); err != nil {
      if err == io.EOF {
        log.Println("EOF on input stream, terminating");
        break
      }
      log.Fatalln("Failed to decode request:", err)
    }
    log.Println("request:", request)
    var result interface{}
    var err error
    switch request.Method {
      case "lookup": result, err = lookup(request.Parameters)
      default: result, err = false, errors.New("unknown/unimplemented request: " + request.AsString())
    }
    if err == nil {
      log.Println("result:", result)
      respond(enc, result)
    } else {
      log.Println("error:", err)
      respond(enc, result, err.Error())
    }
  }
}

func makeResponse(result interface{}, msg ...string) map[string]interface{} {
  response := map[string]interface{}{"result":result}
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

func fatal(enc *json.Encoder, msg string) {
  respond(enc, false, msg)
  log.Fatalln("Fatal error:", msg)
}

func extractSubdomain(domain, zone string) string {
  subdomain := strings.TrimSuffix(domain, zone)
  subdomain = strings.TrimSuffix(subdomain, ".")
  return subdomain
}

func ensureDefaults(ctx context.Context, key string) error {
  if _, ok := defaults.what2values[key]; !ok {
    log.Println("loading defaults:", key)
    response, err := cli.Get(ctx, key)
    if err != nil { return err }
    defs := map[string]interface{}{}
    if response.Count > 0 {
      err := json.Unmarshal(response.Kvs[0].Value, &defs)
      if err != nil { return err }
    }
    defaults.what2values[key] = defs
  } else {
    log.Println("reusing defaults:", key)
  }
  return nil
}

type QueryParts struct {
  zoneId int32
  qname, zone, subdomain, qtype string
}

func (qp *QueryParts) isANY() bool { return qp.qtype == "ANY" }
func (qp *QueryParts) isSOA() bool { return qp.qtype == "SOA" }

func (qp *QueryParts) zoneKey() string { return prefix + "/" + qp.zone }
func (qp *QueryParts) subdomainKey() string { return prefix + "/" + qp.zone + "/" + qp.subdomain }
func (qp *QueryParts) recordKey() string {
  key := prefix + "/" + qp.zone + "/" + qp.subdomain
  if !qp.isANY() { key += "/" + qp.qtype }
  if !qp.isSOA() { key += "/" }
  return key
}

func (qp *QueryParts) zoneDefaultsKey() string { return prefix + "/" + qp.zone + "/-defaults" }
func (qp *QueryParts) zoneSubdomainDefaultsKey() string { return prefix + "/" + qp.zone + "/" + qp.subdomain + "/-defaults" }
func (qp *QueryParts) zoneQtypeDefaultsKey() string { return prefix + "/" + qp.zone + "/" + qp.qtype + "-defaults" }
func (qp *QueryParts) zoneSubdomainQtypeDefaultsKey() string { return prefix + "/" + qp.zone + "/" + qp.subdomain + "/" + qp.qtype + "-defaults" }

func lookup(params map[string]interface{}) (interface{}, error) {
  qp := QueryParts{
    qname: params["qname"].(string),
    zoneId: int32(params["zone-id"].(float64)), // note: documenation says 'zone_id', but it's 'zone-id'! further it is called 'domain_id' in responses (what a mess)
    qtype: params["qtype"].(string),
  }
  var isNewZone bool
  if z, ok := id2zone[qp.zoneId]; ok {
    qp.zone = z
    isNewZone = false
  } else {
    qp.zone = qp.qname
    isNewZone = true
  }
  qp.subdomain = extractSubdomain(qp.qname, qp.zone)
  if len(qp.subdomain) == 0 { qp.subdomain = "@" }
  opts := []clientv3.OpOption{}
  if !qp.isSOA() {
    opts = append(opts, clientv3.WithPrefix())
  }
  var response *clientv3.GetResponse
  var err error
  ctx, cancel := context.WithTimeout(context.Background(), timeout)
  defer cancel()
  log.Println("lookup at", qp.recordKey())
  response, err = cli.Get(ctx, qp.recordKey(), opts...) // TODO set quorum option. not in API, perhaps default now (in v3)?
  if err != nil { return false, err }
  // defaults
  if defaults.revision != response.Header.Revision {
    // TODO recheck version
    log.Println("clearing defaults cache. old revision:", defaults.revision, ", new revision:", response.Header.Revision)
    defaults.revision = response.Header.Revision
    defaults.what2values = map[string]map[string]interface{}{}
  }
  if response.Count > 0 {
    // TODO *lazy* loading of defaults
    err = ensureDefaults(ctx, qp.zoneDefaultsKey())
    if err != nil { return false, err }
    err = ensureDefaults(ctx, qp.zoneSubdomainDefaultsKey())
    if err != nil { return false, err }
  }
  if qp.isSOA() && isNewZone && response.Count > 0 {
    qp.zoneId = nextZoneId
    nextZoneId++
    zone2id[qp.zone] = qp.zoneId
    id2zone[qp.zoneId] = qp.zone
  }
  result := []map[string]interface{}{}
  for _, item := range response.Kvs {
    itemKey := string(item.Key)
    if strings.HasSuffix(itemKey, "-defaults") { continue }
    if len(item.Value) == 0 { return false, errors.New("empty value") }
    qp := qp // clone
    if qp.isANY() {
      qp.qtype = strings.TrimPrefix(itemKey, qp.recordKey())
      idx := strings.Index(qp.qtype, "/")
      if idx >= 0 { qp.qtype = qp.qtype[0:idx] }
    }
    var content string
    var ttl time.Duration
    err = ensureDefaults(ctx, qp.zoneQtypeDefaultsKey())
    if err != nil { return false, err }
    err = ensureDefaults(ctx, qp.zoneSubdomainQtypeDefaultsKey())
    if err != nil { return false, err }
    defaultsChain := []map[string]interface{}{
      defaults.what2values[qp.zoneSubdomainQtypeDefaultsKey()],
      defaults.what2values[qp.zoneSubdomainDefaultsKey()],
      defaults.what2values[qp.zoneQtypeDefaultsKey()],
      defaults.what2values[qp.zoneDefaultsKey()],
    }
    if item.Value[0] == '{' {
      var obj map[string]interface{}
      err = json.Unmarshal(item.Value, &obj)
      if err != nil { return false, err }
      err = nil
      valuesChain := []map[string]interface{}{obj}
      valuesChain = append(valuesChain, defaultsChain...)
      switch qp.qtype {
        case "SOA": content, ttl, err = soa(valuesChain, &qp, response.Header.Revision)
        case "NS": content, ttl, err = ns(valuesChain, &qp)
        // TODO more qtypes
        default: return false, errors.New("unknown/unimplemented qtype '" + qp.qtype + "', but have (JSON) object data for it (" + qp.recordKey() + ")")
      }
      if err != nil { return false, err }
    } else {
      content = string(item.Value)
      ttl, err = getDuration("ttl", defaultsChain...)
      if err != nil { return false, err }
    }
    result = append(result, makeResultItem(&qp, content, ttl))
  }
  return result, nil
}

func makeResultItem(qp *QueryParts, content string, ttl time.Duration) map[string]interface{} {
  return map[string]interface{}{
    "domain_id": qp.zoneId,
    "qname": qp.qname,
    "qtype": qp.qtype,
    "content": content,
    "ttl": seconds(ttl),
    "auth": true,
  }
}

func fqdn(domain, qname string) string {
  l := len(domain)
  if l == 0 || domain[l-1] != '.' {
    domain += "." + qname
    l = len(domain)
    if domain[l-1] != '.' {
      domain += "."
    }
  }
  return domain
}

func findValue(name string, maps ...map[string]interface{}) (interface{}, bool) {
  for _, m := range maps {
    if v, ok := m[name]; ok {
      return v, true
    }
  }
  return nil, false
}

func getInt32(name string, maps ...map[string]interface{}) (int32, error) {
  if v, ok := findValue(name, maps...); ok {
    if v, ok := v.(float64); ok {
      if v < 0 {
        return 0, errors.New("'" + name + "' may not be negative")
      }
      return int32(v), nil
    } else {
      return 0, errors.New("'" + name + "' is not a number")
    }
  } else {
    return 0, errors.New("missing '" + name + "'")
  }
}

func getString(name string, maps ...map[string]interface{}) (string, error) {
  if v, ok := findValue(name, maps...); ok {
    if v, ok := v.(string); ok {
      return v, nil
    } else {
      return "", errors.New("'" + name + "' is not a string")
    }
  } else {
    return "", errors.New("missing '" + name + "'")
  }
}

func getDuration(name string, maps ...map[string]interface{}) (time.Duration, error) {
  if v, ok := findValue(name, maps...); ok {
    var dur time.Duration
    switch v.(type) {
      case float64:
        dur = time.Duration(int64(v.(float64))) * time.Second
      case string:
        if v, err := time.ParseDuration(v.(string)); err == nil {
        dur = v
      } else {
        return 0, errors.New("'" + name + "' parse error: " + err.Error())
      }
      default:
        return 0, errors.New("'" + name + "' is neither a number nor a string")
    }
    if dur < time.Second {
      return dur, errors.New("'" + name + "' must be positive")
    }
    return dur, nil
  } else {
    return 0, errors.New("missing '" + name + "'")
  }
}

func seconds(dur time.Duration) int64 {
  return int64(dur.Seconds())
}

func soa(valuesChain []map[string]interface{}, qp *QueryParts, revision int64) (string, time.Duration, error) {
  // primary
  primary, err := getString("primary", valuesChain...)
  if err != nil { return "", 0, err }
  primary = strings.TrimSpace(primary)
  primary = fqdn(primary, qp.zone)
  // mail
  mail, err := getString("mail", valuesChain...)
  if err != nil { return "", 0, err }
  mail = strings.TrimSpace(mail)
  atIndex := strings.Index(mail, "@")
  if atIndex < 0 {
    mail = strings.Replace(mail, ".", "\\.", -1)
  } else {
    localpart := mail[0:atIndex]
    domain := ""
    if atIndex + 1 < len(mail) { domain = mail[atIndex+1:] }
    localpart = strings.Replace(localpart, ".", "\\.", -1)
    mail = localpart + "." + domain
  }
  mail = fqdn(mail, qp.zone)
  // serial
  serial := revision
  // refresh
  refresh, err := getDuration("refresh", valuesChain...)
  if err != nil { return "", 0, err }
  // retry
  retry, err := getDuration("retry", valuesChain...)
  if err != nil { return "", 0, err }
  // expire
  expire, err := getDuration("expire", valuesChain...)
  if err != nil { return "", 0, err }
  // negative ttl
  negativeTTL, err := getDuration("neg-ttl", valuesChain...)
  if err != nil { return "", 0, err }
  // ttl
  ttl, err := getDuration("ttl", valuesChain...)
  if err != nil { return "", 0, err }
  // (done)
  var content string = fmt.Sprintf("%s %s %d %d %d %d %d", primary, mail, serial, seconds(refresh), seconds(retry), seconds(expire), seconds(negativeTTL))
  return content, ttl, nil
}

func ns(valuesChain []map[string]interface{}, qp *QueryParts) (string, time.Duration, error) {
  hostname, err := getString("hostname", valuesChain...)
  if err != nil { return "", 0, err }
  hostname = strings.TrimSpace(hostname)
  hostname = fqdn(hostname, qp.zone)
  ttl, err := getDuration("ttl", valuesChain...)
  if err != nil { return "", 0, err }
  content := fmt.Sprintf("%s", hostname)
  return content, ttl, nil
}
