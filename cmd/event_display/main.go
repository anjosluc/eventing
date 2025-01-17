/*
Copyright 2021 The Knative Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"github.com/cloudevents/sdk-go/observability/opencensus/v2/client"
	cloudevents "github.com/cloudevents/sdk-go/v2"
	cehttp "github.com/cloudevents/sdk-go/v2/protocol/http"

	"go.uber.org/zap"
	"knative.dev/pkg/tracing"
	"knative.dev/pkg/tracing/config"
)

/*
Example Output:

☁️  cloudevents.Event
Validation: valid
Context Attributes,
  specversion: 1.0
  type: dev.knative.eventing.samples.heartbeat
  source: https://knative.dev/eventing-contrib/cmd/heartbeats/#event-test/mypod
  id: 2b72d7bf-c38f-4a98-a433-608fbcdd2596
  time: 2019-10-18T15:23:20.809775386Z
  contenttype: application/json
Extensions,
  beats: true
  heart: yes
  the: 42
Data,
  {
    "id": 2,
    "label": ""
  }
*/

// display prints the given Event in a human-readable format.
func display(event cloudevents.Event) {
	jsonstr, _ := json.Marshal(event.Context.GetExtensions())
	log.Printf("{\"data\": %s, \"type\": %s, \"extensions\": %s}",
		event.DataEncoded,
		event.Context.GetType(),
		string(jsonstr),
	)
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func main() {
	logFile, err := os.OpenFile(getEnv("LOG_FILE_PATH", "/var/log/app.log"), os.O_CREATE|os.O_APPEND|os.O_RDWR, 0666)
	if err != nil {
		panic(err)
	}
	mw := io.MultiWriter(os.Stdout, logFile)

	// Disabling timestamp
	log.SetFlags(0)

	log.SetOutput(mw)
	defer logFile.Close()

	run(context.Background())
}

func run(ctx context.Context) {

	requestLoggingEnabled, _ := strconv.ParseBool(os.Getenv("REQUEST_LOGGING_ENABLED"))
	if requestLoggingEnabled {
		log.Println("Request logging enabled, request logging is not recommended for production since it might log sensitive information")
	}

	c, err := client.NewClientHTTP(
		[]cehttp.Option{
			cehttp.WithMiddleware(healthzMiddleware),
			cehttp.WithMiddleware(requestLoggingMiddleware(requestLoggingEnabled)),
		}, nil,
	)
	if err != nil {
		log.Fatal("Failed to create client: ", err)
	}
	conf, err := config.JSONToTracingConfig(os.Getenv("K_CONFIG_TRACING"))
	if err != nil {
		log.Printf("Failed to read tracing config, using the no-op default: %v", err)
	}
	tracer, err := tracing.SetupPublishingWithStaticConfig(zap.L().Sugar(), "", conf)
	if err != nil {
		log.Fatalf("Failed to initialize tracing: %v", err)
	}
	defer tracer.Shutdown(context.Background())

	if err := c.StartReceiver(ctx, display); err != nil {
		log.Fatal("Error during receiver's runtime: ", err)
	}
}

// HTTP path of the health endpoint used for probing the service.
const healthzPath = "/healthz"

// healthzMiddleware is a cehttp.Middleware which exposes a health endpoint.
func healthzMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.RequestURI == healthzPath {
			w.WriteHeader(http.StatusNoContent)
		} else {
			next.ServeHTTP(w, req)
		}
	})
}

// requestLoggingMiddleware is a cehttp.Middleware which logs incoming requests.
func requestLoggingMiddleware(enabled bool) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if enabled {
				logRequest(req)
			}
			next.ServeHTTP(w, req)
		})
	}
}

type LoggableRequest struct {
	Method           string      `json:"method,omitempty"`
	URL              *url.URL    `json:"URL,omitempty"`
	Proto            string      `json:"proto,omitempty"`
	ProtoMajor       int         `json:"protoMajor,omitempty"`
	ProtoMinor       int         `json:"protoMinor,omitempty"`
	Header           http.Header `json:"headers,omitempty"`
	Body             string      `json:"body,omitempty"`
	ContentLength    int64       `json:"contentLength,omitempty"`
	TransferEncoding []string    `json:"transferEncoding,omitempty"`
	Host             string      `json:"host,omitempty"`
	Trailer          http.Header `json:"trailer,omitempty"`
	RemoteAddr       string      `json:"remoteAddr"`
	RequestURI       string      `json:"requestURI"`
}

func logRequest(req *http.Request) {
	b, err := json.MarshalIndent(toReq(req), "", "  ")
	if err != nil {
		log.Println("failed to marshal request", err)
	}

	log.Println(string(b))
}

func toReq(req *http.Request) LoggableRequest {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		log.Println("failed to read request body")
	}
	_ = req.Body.Close()
	// Replace the body with a new reader after reading from the original
	req.Body = io.NopCloser(bytes.NewBuffer(body))
	return LoggableRequest{
		Method:           req.Method,
		URL:              req.URL,
		Proto:            req.Proto,
		ProtoMajor:       req.ProtoMajor,
		ProtoMinor:       req.ProtoMinor,
		Header:           req.Header,
		Body:             string(body),
		ContentLength:    req.ContentLength,
		TransferEncoding: req.TransferEncoding,
		Host:             req.Host,
		Trailer:          req.Trailer,
		RemoteAddr:       req.RemoteAddr,
		RequestURI:       req.RequestURI,
	}
}
