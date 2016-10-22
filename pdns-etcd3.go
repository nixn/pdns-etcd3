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
  "strconv"
  "regexp"
  "net"
  "golang.org/x/net/context"
  "github.com/coreos/etcd/clientv3"
)

type pdnsRequest struct {
  Method string
  Parameters map[string]interface{}
}

func (req *pdnsRequest) String() string {
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
  values map[string]map[string]interface{} // key = "example.net" or "example.net/subdomain" or "example.net/[subdomain/]RR"
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
      prefix = pfx
    } else {
      fatal(enc, "parameters.prefix is not a string")
    }
  }
  logMessages = append(logMessages, fmt.Sprintf("prefix: '%s'", prefix))
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
        if tmo, err := strconv.ParseUint(tmo, 10, 32); err == nil {
          if tmo > 0 {
            timeout = time.Duration(tmo) * time.Millisecond
            cfg.DialTimeout = timeout
          } else {
            fatal(enc, "Timeout may not be zero")
          }
        } else {
          fatal(enc, fmt.Sprintf("Failed to parse timeout value: %s", err))
        }
      } else {
        fatal(enc, "parameters.timeout is not a string")
      }
    }
    logMessages = append(logMessages, fmt.Sprintf("timeout: %s", timeout))
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
      cfg.Endpoints = []string{"[::1]:2379", "127.0.0.1:2379"}
      if client, err := clientv3.New(cfg); err == nil {
        cli = client
      } else {
        fatal(enc, err.Error())
      }
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
    since := time.Now()
    var result interface{}
    var err error
    switch request.Method {
      case "lookup": result, err = lookup(request.Parameters)
      default: result, err = false, fmt.Errorf("unknown/unimplemented request: %s", request)
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

type defaultsMessage struct {
  key string
  value map[string]interface{}
  err error
}

func loadDefaults(key string, c chan defaultsMessage) {
  since := time.Now()
  ctx, cancel := context.WithTimeout(context.Background(), timeout)
  response, err := cli.Get(ctx, key)
  cancel()
  dur := time.Since(since)
  log.Println("loaded", key, "in", dur)
  if err != nil {
    c <- defaultsMessage{ key, nil, err }
    return
  }
  defs := map[string]interface{}{}
  if response.Count > 0 {
    err := json.Unmarshal(response.Kvs[0].Value, &defs)
    if err != nil {
      c <- defaultsMessage{ key, nil, err }
      return
    }
  }
  c <- defaultsMessage{ key, defs, nil }
}

func ensureDefaults(qp *queryParts) error {
  keys := []string{
    qp.zoneDefaultsKey(),
    qp.zoneQtypeDefaultsKey(),
    qp.zoneSubdomainDefaultsKey(),
    qp.zoneSubdomainQtypeDefaultsKey() }
  c := make(chan defaultsMessage, len(keys))
  n := 0
  since := time.Now()
  for _, key := range keys {
    if _, ok := defaults.values[key]; !ok {
      log.Println("loading defaults: ", key)
      go loadDefaults(key, c)
      n++
    } else {
      log.Println("reusing defaults:", key)
    }
  }
  var err error
  for i := 0; i < n; i++ {
    msg := <- c
    if msg.err == nil {
      defaults.values[msg.key] = msg.value
    } else {
      if err == nil { err = msg.err }
    }
  }
  dur := time.Since(since)
  log.Println("ensureDefaults dur:", dur)
  return err
}

type queryParts struct {
  zoneId int32
  qname, zone, subdomain, qtype string
}

func (qp *queryParts) isANY() bool { return qp.qtype == "ANY" }
func (qp *queryParts) isSOA() bool { return qp.qtype == "SOA" }

func (qp *queryParts) zoneKey() string { return prefix + "/" + qp.zone }
func (qp *queryParts) subdomainKey() string { return prefix + "/" + qp.zone + "/" + qp.subdomain }
func (qp *queryParts) recordKey() string {
  key := prefix + "/" + qp.zone + "/" + qp.subdomain
  if !qp.isANY() { key += "/" + qp.qtype }
  if !qp.isSOA() { key += "/" }
  return key
}

func (qp *queryParts) zoneDefaultsKey() string { return prefix + "/" + qp.zone + "/-defaults" }
func (qp *queryParts) zoneSubdomainDefaultsKey() string { return prefix + "/" + qp.zone + "/" + qp.subdomain + "/-defaults" }
func (qp *queryParts) zoneQtypeDefaultsKey() string { return prefix + "/" + qp.zone + "/" + qp.qtype + "-defaults" }
func (qp *queryParts) zoneSubdomainQtypeDefaultsKey() string { return prefix + "/" + qp.zone + "/" + qp.subdomain + "/" + qp.qtype + "-defaults" }
func (qp *queryParts) isDefaultsKey(key string) bool {
  if key == qp.zoneDefaultsKey() { return true; }
  if key == qp.zoneSubdomainDefaultsKey() { return true; }
  if key == qp.zoneQtypeDefaultsKey() { return true; }
  if key == qp.zoneSubdomainQtypeDefaultsKey() { return true; }
  return false;
}

func lookup(params map[string]interface{}) (interface{}, error) {
  qp := queryParts{
    qname: params["qname"].(string),
    zoneId: int32(params["zone-id"].(float64)), // note: documentation says 'zone_id', but it's 'zone-id'! further it is called 'domain_id' in responses (what a mess)
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
  log.Println("lookup:", qp.recordKey())
  {
    since := time.Now()
    ctx, cancel := context.WithTimeout(context.Background(), timeout)
    response, err = cli.Get(ctx, qp.recordKey(), opts...) // TODO set quorum option. not in API, perhaps default now (in v3)?
    cancel()
    dur := time.Since(since)
    log.Println("lookup dur:", dur)
  }
  if err != nil { return false, err }
  // defaults
  if defaults.revision != response.Header.Revision {
    // TODO recheck version
    log.Println("clearing defaults cache. old revision:", defaults.revision, ", new revision:", response.Header.Revision)
    defaults.revision = response.Header.Revision
    defaults.values = map[string]map[string]interface{}{}
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
    if qp.isDefaultsKey(itemKey) { continue } // this is needed for 'ANY' requests
    if len(item.Value) == 0 { return false, errors.New("empty value") }
    qp := qp // clone
    if qp.isANY() {
      qp.qtype = strings.TrimPrefix(itemKey, qp.recordKey())
      idx := strings.Index(qp.qtype, "/")
      if idx >= 0 { qp.qtype = qp.qtype[0:idx] }
    }
    var content string
    var ttl time.Duration
    if item.Value[0] == '{' {
      var obj map[string]interface{}
      err = json.Unmarshal(item.Value, &obj)
      if err != nil { return false, err }
      err = nil
      switch qp.qtype {
        case "SOA": content, ttl, err = soa(obj, &qp, response.Header.Revision)
        case "NS": content, ttl, err = ns(obj, &qp)
        case "A": content, ttl, err = a(obj, &qp)
        case "AAAA": content, ttl, err = aaaa(obj, &qp)
        case "PTR": content, ttl, err = ptr(obj, &qp)
        // TODO more qtypes
        default: return false, errors.New("unknown/unimplemented qtype '" + qp.qtype + "', but have (JSON) object data for it (" + qp.recordKey() + ")")
      }
      if err != nil { return false, err }
    } else {
      content = string(item.Value)
      ttl, err = getDuration("ttl", nil, &qp)
      if err != nil { return false, err }
    }
    result = append(result, makeResultItem(&qp, content, ttl))
  }
  return result, nil
}

func makeResultItem(qp *queryParts, content string, ttl time.Duration) map[string]interface{} {
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

func findValue(name string, obj map[string]interface{}, qp *queryParts) (interface{}, error) {
  if v, ok := obj[name]; ok { return v, nil }
  if err := ensureDefaults(qp); err != nil { return nil, err }
  if v, ok := defaults.values[qp.zoneSubdomainQtypeDefaultsKey()][name]; ok { return v, nil }
  if v, ok := defaults.values[qp.zoneSubdomainDefaultsKey()][name]; ok { return v, nil }
  if v, ok := defaults.values[qp.zoneQtypeDefaultsKey()][name]; ok { return v, nil }
  if v, ok := defaults.values[qp.zoneDefaultsKey()][name]; ok { return v, nil }
  return nil, errors.New("missing '" + name + "'")
}

func getInt32(name string, obj map[string]interface{}, qp *queryParts) (int32, error) {
  if v, err := findValue(name, obj, qp); err == nil {
    if v, ok := v.(float64); ok {
      if v < 0 {
        return 0, errors.New("'" + name + "' may not be negative")
      }
      return int32(v), nil
    }
    return 0, errors.New("'" + name + "' is not a number")
  } else {
    return 0, err
  }
}

func getString(name string, obj map[string]interface{}, qp *queryParts) (string, error) {
  if v, err := findValue(name, obj, qp); err == nil {
    if v, ok := v.(string); ok {
      return v, nil
    } else {
      return "", errors.New("'" + name + "' is not a string")
    }
  } else {
    return "", err
  }
}

func getDuration(name string, obj map[string]interface{}, qp *queryParts) (time.Duration, error) {
  if v, err := findValue(name, obj, qp); err == nil {
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
    return 0, err
  }
}

func seconds(dur time.Duration) int64 {
  return int64(dur.Seconds())
}

func soa(obj map[string]interface{}, qp *queryParts, revision int64) (string, time.Duration, error) {
  // primary
  primary, err := getString("primary", obj, qp)
  if err != nil { return "", 0, err }
  primary = strings.TrimSpace(primary)
  primary = fqdn(primary, qp.zone)
  // mail
  mail, err := getString("mail", obj, qp)
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
  refresh, err := getDuration("refresh", obj, qp)
  if err != nil { return "", 0, err }
  // retry
  retry, err := getDuration("retry", obj, qp)
  if err != nil { return "", 0, err }
  // expire
  expire, err := getDuration("expire", obj, qp)
  if err != nil { return "", 0, err }
  // negative ttl
  negativeTTL, err := getDuration("neg-ttl", obj, qp)
  if err != nil { return "", 0, err }
  // ttl
  ttl, err := getDuration("ttl", obj, qp)
  if err != nil { return "", 0, err }
  // (done)
  var content = fmt.Sprintf("%s %s %d %d %d %d %d", primary, mail, serial, seconds(refresh), seconds(retry), seconds(expire), seconds(negativeTTL))
  return content, ttl, nil
}

func ns(obj map[string]interface{}, qp *queryParts) (string, time.Duration, error) {
  hostname, err := getString("hostname", obj, qp)
  if err != nil { return "", 0, err }
  hostname = strings.TrimSpace(hostname)
  hostname = fqdn(hostname, qp.zone)
  ttl, err := getDuration("ttl", obj, qp)
  if err != nil { return "", 0, err }
  content := fmt.Sprintf("%s", hostname)
  return content, ttl, nil
}

func a(obj map[string]interface{}, qp *queryParts) (string, time.Duration, error) {
  var ip net.IP
  v, err := findValue("ip", obj, qp)
  if err != nil { return "", 0, err }
  switch v.(type) {
    case string:
      v := v.(string)
      ipv4HexRE := regexp.MustCompile("^([0-9a-fA-F]{2}){4}$")
      if ipv4HexRE.MatchString(v) {
        ip = net.IP{0, 0, 0, 0}
        for i := 0; i < 4; i++ {
          v, err := strconv.ParseUint(v[i * 2:i * 2 + 2], 16, 8)
          if err != nil { return "", 0, err }
          ip[i] = byte(v)
        }
      } else {
        ip = net.ParseIP(v)
        if ip == nil { return "", 0, errors.New("invalid IPv4: failed to parse") }
        ip = ip.To4()
        if ip == nil { return "", 0, errors.New("invalid IPv4: parsed, but not as IPv4") }
      }
    case []interface{}:
      v := v.([]interface{})
      if len(v) != 4 { return "", 0, errors.New("invalid IPv4: array length not 4") }
      ip = net.IP{0, 0, 0, 0}
      for i, v := range v {
        switch v.(type) {
          case float64:
            v := int64(v.(float64))
            if v < 0 || v > 255 { return "", 0, fmt.Errorf("invalid IPv4: part %d out of range", i + 1) }
            ip[i] = byte(v)
          case string:
            v, err := strconv.ParseUint(v.(string), 0, 8)
            if err != nil { return "", 0, err }
            if v > 255 { return "", 0, fmt.Errorf("invalid IPv4: part %d out of range", i + 1) }
            ip[i] = byte(v)
          default:
            return "", 0, errors.New("invalid IPv4: part neither number nor string")
        }
      }
    default:
      return "", 0, errors.New("invalid IPv4: not string or array")
  }
  ttl, err := getDuration("ttl", obj, qp)
  if err != nil { return "", 0, err }
  content := ip.String()
  return content, ttl, nil
}

func aaaa(obj map[string]interface{}, qp *queryParts) (string, time.Duration, error) {
  var ip net.IP
  v, err := findValue("ip", obj, qp)
  if err != nil { return "", 0, err }
  switch v.(type) {
    case string:
      v := v.(string)
      ipv6HexRE := regexp.MustCompile("^([0-9a-fA-F]{2}){16}$")
      if ipv6HexRE.MatchString(v) {
        ip = net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
        for i := 0; i < 16; i++ {
          v, err := strconv.ParseUint(v[i * 2:i * 2 + 2], 16, 8)
          if err != nil { return "", 0, err }
          ip[i] = byte(v)
        }
      } else {
        ip = net.ParseIP(v)
        if ip == nil { return "", 0, errors.New("invalid IPv6: failed to parse") }
        ip = ip.To16()
        if ip == nil { return "", 0, errors.New("invalid IPv6: parsed, but no IPv6") }
      }
    case []interface{}:
      v := v.([]interface{})
      var bytesPerPart int
      switch len(v) {
        case 8:
          bytesPerPart = 2
        case 16:
          bytesPerPart = 1
        default:
          return "", 0, errors.New("invalid IPv6: array length neither 8 nor 16")
      }
      bitSize := bytesPerPart * 8
      maxVal := uint64(1 << uint(bitSize) - 1)
      ip = net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
      setPart := func (i int, v uint64) {
        for j := 0; j < bytesPerPart; j++ {
          v := (v >> uint((bytesPerPart - 1 - j) * 8)) & 0xFF
          ip[i * bytesPerPart + j] = byte(v)
        }
      }
      for i, v := range v {
        switch v.(type) {
          case float64:
            if v.(float64) < 0 { return "", 0, errors.New("invalid IPv6: part out of range") }
            v := uint64(v.(float64))
            if v > maxVal { return "", 0, errors.New("invalid IPv6: part out of range") }
            setPart(i, v)
          case string:
            v, err := strconv.ParseUint(v.(string), 0, bitSize)
            if err != nil { return "", 0, errors.New("invalid IPv6: " + err.Error()) }
            if v > maxVal { return "", 0, errors.New("invalid IPv6: part out of range") }
            setPart(i, v)
          default:
            return "", 0, errors.New("invalid IPv6: not string or number")
        }
      }
    default:
      return "", 0, errors.New("invalid IPv6: not string or array")
  }
  ttl, err := getDuration("ttl", obj, qp)
  if err != nil { return "", 0, err }
  content := ip.String()
  return content, ttl, nil
}

func ptr(obj map[string]interface{}, qp *queryParts) (string, time.Duration, error) {
  hostname, err := getString("hostname", obj, qp)
  if err != nil { return "", 0, err }
  hostname = strings.TrimSpace(hostname)
  hostname = fqdn(hostname, qp.zone)
  ttl, err := getDuration("ttl", obj, qp)
  if err != nil { return "", 0, err }
  content := fmt.Sprintf("%s", hostname)
  return content, ttl, nil
}
