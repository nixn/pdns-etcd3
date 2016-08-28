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

type PdnsRequest struct {
  Method string
  Parameters map[string]interface{}
}

func (req *PdnsRequest) AsString() string {
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

func main() {
  dec := json.NewDecoder(os.Stdin)
  enc := json.NewEncoder(os.Stdout)
  var request PdnsRequest
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
  respond(enc, true, logMessages...)
  log.Println("initialized.", strings.Join(logMessages, ". "))
  // main loop
  for {
    request := PdnsRequest{}
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

func lookup(params map[string]interface{}) (interface{}, error) {
  qname := params["qname"].(string)
  qtype := params["qtype"].(string)
  zoneId := int32(params["zone-id"].(float64)) // note: documenation says 'zone_id', but it's 'zone-id'! further it is called 'domain_id' in responses (what a mess)
  var zone string
  var isNewZone bool
  if z, ok := id2zone[zoneId]; ok {
    zone = z
    isNewZone = false
  } else {
    zone = qname
    isNewZone = true
  }
  zoneKey := prefix + "/" + zone
  subdomain := extractSubdomain(qname, zone)
  if len(subdomain) == 0 { subdomain = "@" }
  subdomainKey := zoneKey + "/" + subdomain
  recordKey := subdomainKey
  isANY := qtype == "ANY"
  if !isANY { recordKey += "/" + qtype }
  opts := []clientv3.OpOption{}
  isSOA := qtype == "SOA"
  if !isSOA {
    recordKey += "/"
    opts = append(opts, clientv3.WithPrefix())
  }
  var response *clientv3.GetResponse
  var err error
  ctx, cancel := context.WithTimeout(context.Background(), timeout)
  defer cancel()
  // TODO lazy loading of defaults
  defaults := map[string]interface{}{}
  response, err = cli.Get(ctx, zoneKey + "/-defaults")
  if err != nil { return false, err }
  if response.Count > 0 {
    err = json.Unmarshal(response.Kvs[0].Value, &defaults)
    if err != nil { return false, err }
  }
  // TODO load defaults for subdomain
  if !isSOA && !isANY {
    response, err = cli.Get(ctx, recordKey + "-defaults")
    if response.Count > 0 {
      err = json.Unmarshal(response.Kvs[0].Value, &defaults)
      if err != nil { return false, err }
    }
  }
  log.Println("lookup at", recordKey)
  response, err = cli.Get(ctx, recordKey, opts...) // TODO set quorum option. not in API, perhaps default now (in v3)?
  if err != nil { return false, err }
  if isSOA && isNewZone && response.Count > 0 {
    zoneId = nextZoneId
    nextZoneId++
    zone2id[zone] = zoneId
    id2zone[zoneId] = zone
  }
  result := []map[string]interface{}{}
  for _, item := range response.Kvs {
    itemKey := string(item.Key)
    if strings.HasSuffix(itemKey, "-defaults") { continue }
    qtype := qtype // create a copy
    if isANY {
      qtype = strings.TrimPrefix(itemKey, recordKey)
      idx := strings.Index(qtype, "/")
      if idx >= 0 { qtype = qtype[0:idx] }
    }
    var content string
    var ttl int32 = -1
    ttlIsSet := false
    if len(item.Value) == 0 { return false, errors.New("empty value") }
    if item.Value[0] == '{' {
      var obj map[string]interface{}
      err = json.Unmarshal(item.Value, &obj)
      if err != nil { return false, err }
      err = nil
      switch qtype {
        case "SOA": content, ttl, err = soa(obj, defaults, zone, response.Header.Revision)
        case "NS": content, ttl, err = ns(obj, defaults, zone)
        // TODO more qtypes
        default: return false, errors.New("unknown/unimplemented qtype '" + qtype + "', but have (JSON) object data for it (" + recordKey + ")")
      }
      if err != nil { return false, err }
    } else {
      content = string(item.Value)
      ttl, err = getInt32("ttl", defaults)
    }
    if ttl < 0 {
      if ttlIsSet {
        return false, errors.New("TTL must not be negative")
      } else {
        return false, errors.New("TTL not set")
      }
    }
    result = append(result, makeResultItem(zoneId, qname, qtype, content, ttl, zoneId > 0))
  }
  return result, nil
}

func makeResultItem(zoneId int32, qname, qtype, content string, ttl int32, auth bool) map[string]interface{} {
  return map[string]interface{}{
    "domain_id": zoneId,
    "qname": qname,
    "qtype": qtype,
    "content": content,
    "ttl": ttl,
    "auth": auth,
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

func soa(record, defaults map[string]interface{}, zone string, revision int64) (string, int32, error) {
  // primary
  primary, err := getString("primary", record, defaults)
  if err != nil { return "", 0, err }
  primary = strings.TrimSpace(primary)
  primary = fqdn(primary, zone)
  // mail
  mail, err := getString("mail", record, defaults)
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
  mail = fqdn(mail, zone)
  // serial
  serial := revision
  // refresh
  refresh, err := getInt32("refresh", record, defaults)
  if err != nil { return "", 0, err }
  // retry
  retry, err := getInt32("retry", record, defaults)
  if err != nil { return "", 0, err }
  // expire
  expire, err := getInt32("expire", record, defaults)
  if err != nil { return "", 0, err }
  // negative ttl
  negativeTTL, err := getInt32("neg-ttl", record, defaults)
  if err != nil { return "", 0, err }
  // ttl
  ttl, err := getInt32("ttl", record, defaults)
  if err != nil { return "", 0, err }
  // (done)
  var content string = fmt.Sprintf("%s %s %d %d %d %d %d", primary, mail, serial, refresh, retry, expire, negativeTTL)
  return content, ttl, nil
}

func ns(record, defaults map[string]interface{}, zone string) (string, int32, error) {
  hostname, err := getString("hostname", record, defaults)
  if err != nil { return "", 0, err }
  hostname = strings.TrimSpace(hostname)
  hostname = fqdn(hostname, zone)
  ttl, err := getInt32("ttl", record, defaults)
  if err != nil { return "", 0, err }
  content := fmt.Sprintf("%s", hostname)
  return content, ttl, nil
}
