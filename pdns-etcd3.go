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
  "strconv"
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

func lookup(params map[string]interface{}) (interface{}, error) {
  qname := params["qname"].(string)
  qtype := params["qtype"].(string)
  key := prefix + "/" + qname + "/"
  defaultsKey := key + "-defaults"
  ttlKey := key + "-default-ttl"
  key += qtype
  opts := []clientv3.OpOption{}
  if qtype != "SOA" {
    key += "/"
    opts = append(opts, clientv3.WithPrefix())
  }
  var response *clientv3.GetResponse
  var err error
  ctx, cancel := context.WithTimeout(context.Background(), timeout)
  defer cancel()
  response, err = cli.Get(ctx, ttlKey)
  if err != nil { return false, err }
  var defaultTTL int32 = -1
  if response.Count > 0 {
    ttl, err := strconv.ParseInt(string(response.Kvs[0].Value), 0, 32)
    if err != nil { return false, err }
    defaultTTL = int32(ttl)
  }
  defaults := map[string]interface{}{}
  response, err = cli.Get(ctx, defaultsKey)
  if err != nil { return false, err }
  if response.Count > 0 {
    err = json.Unmarshal(response.Kvs[0].Value, &defaults)
    if err != nil { return false, err }
  }
  response, err = cli.Get(ctx, defaultsKey + "/" + qtype)
  if response.Count > 0 {
    err = json.Unmarshal(response.Kvs[0].Value, &defaults)
    if err != nil { return false, err }
  }
  response, err = cli.Get(ctx, key, opts...) // TODO set quorum option. not in API, perhaps default now (in v3)?
  if err != nil { return false, err }
  result := []map[string]interface{}{}
  for _, item := range response.Kvs {
    var content string
    var ttl int32 = defaultTTL
    ttlIsSet := false
    if len(item.Value) == 0 { return false, errors.New("empty value") }
    if item.Value[0] == '{' {
      var obj map[string]interface{}
      err = json.Unmarshal(item.Value, &obj)
      if err != nil { return false, err }
      err = nil
      switch qtype {
        case "SOA": content, ttl, err = soa(obj, defaults, qname, response.Header.Revision)
        default: return false, errors.New("unknown/unimplemented qtype '" + qtype + "', but have (JSON) object data for it")
      }
      if err != nil { return false, err }
    } else {
      content = string(item.Value)
    }
    if ttl < 0 {
      if ttlIsSet {
        return false, errors.New("TTL must not be negative")
      } else {
        return false, errors.New("TTL not set")
      }
    }
    result = append(result, makeResultItem(qname, qtype, content, ttl))
  }
  return result, nil
}

func makeResultItem(qname, qtype, content string, ttl int32) map[string]interface{} {
  return map[string]interface{}{
    "qname": qname,
    "qtype": qtype,
    "content": content,
    "ttl": ttl,
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

func soa(record, defaults map[string]interface{}, qname string, revision int64) (string, int32, error) {
  // primary
  var primary string
  if v, ok := findValue("primary", record, defaults); ok {
    if v, ok := v.(string); ok {
      primary = v
    } else {
      return "", 0, errors.New("'primary' is not a string")
    }
  } else {
    return "", 0, errors.New("missing 'primary'")
  }
  primary = fqdn(strings.TrimSpace(primary), qname)
  // mail
  var mail string
  if v, ok := findValue("mail", record, defaults); ok {
    if v, ok := v.(string); ok {
      mail = v
    } else {
      return "", 0, errors.New("'mail' is not a string")
    }
  } else if v, ok := defaults["mail"].(string); ok {
    mail = v
  } else {
    return "", 0, errors.New("missing 'mail'")
  }
  mail = strings.TrimSpace(mail)
  atIndex := strings.Index(mail, "@")
  if atIndex < 0 { atIndex = len(mail) }
  if atIndex > 0 {
    localpart := mail[0:atIndex]
    localpart = strings.Replace(localpart, ".", "\\.", -1)
    mail = localpart + mail[atIndex:]
  }
  mail = fqdn(mail, qname)
  // serial
  serial := revision
  // (helper for following)
  getInt32 := func(name string) (int32, error) {
    if v, ok := findValue(name, record, defaults); ok {
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
  // refresh
  refresh, err := getInt32("refresh")
  if err != nil { return "", 0, err }
  // retry
  retry, err := getInt32("retry")
  if err != nil { return "", 0, err }
  // expire
  expire, err := getInt32("expire")
  if err != nil { return "", 0, err }
  // negative ttl
  negativeTTL, err := getInt32("neg-ttl")
  if err != nil { return "", 0, err }
  // ttl
  ttl, err := getInt32("ttl")
  if err != nil { return "", 0, err }
  // (done)
  var content string = fmt.Sprintf("%s %s %d %d %d %d %d", primary, mail, serial, refresh, retry, expire, negativeTTL)
  return content, ttl, nil
}
