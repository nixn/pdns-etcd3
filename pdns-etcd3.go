package main

import (
  "fmt"
  "log"
  "time"
  "os"
  "encoding/json"
  "strings"
  // "golang.org/x/net/context"
  "github.com/coreos/etcd/clientv3"
)

type PdnsRequest struct {
  Method string
  Parameters map[string]interface{}
}

const DefaultDialTimeout = 2 * time.Second

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
  var cli *clientv3.Client
  if configFile, ok := request.Parameters["configFile"]; ok {
    if configFile, ok := configFile.(string); ok {
      if client, err := clientv3.NewFromConfigFile(configFile); err == nil {
        cli = client
        defer cli.Close()
        respond(enc, true)
      } else {
        fatal(enc, fmt.Sprintf("Failed to create client instance: %v", err))
      }
    } else {
      fatal(enc, "parameters.configFile is not a string")
    }
  } else {
    cfg := clientv3.Config{DialTimeout: DefaultDialTimeout}
    // timeout
    if timeout, ok := request.Parameters["timeout"]; ok {
      if timeout, ok := timeout.(string); ok {
        if timeout, err := time.ParseDuration(timeout); err == nil {
          if timeout > 0 {
            cfg.DialTimeout = timeout
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
          defer cli.Close()
          respond(enc, true)
        } else {
          fatal(enc, fmt.Sprintf("%v", err))
        }
      } else {
        fatal(enc, "parameters.endpoints is not a string")
      }
    } else {
      fatal(enc, "No endpoints defined")
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

/*func main() {
  // TODO use time.parseDuration for timeout config value
  cli, err := clientv3.New(clientv3.Config{
    Endpoints: []string{"http://192.168.12.4:2379"},
    DialTimeout: 2 * time.Second,
  })
  if err != nil {
    log.Fatal("Error creating clientv3", err)
  }
  defer cli.Close()
  ctx, cancel := context.WithTimeout(context.Background(), 2 * time.Second)
  resp, err := cli.Get(ctx)
  cancel()
  fmt.Printf("cli: %+v\n", cli)
}*/
