package sos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DonaldMurillo/system-one-playground/sosconfig"
)

func runHTTPFormsProgram(t *testing.T, source string, client *http.Client) (string, error) {
	t.Helper()
	if ds := Check(source); len(ds) > 0 {
		t.Fatalf("check: %#v", ds)
	}
	p, ds := Parse(source)
	if len(ds) > 0 {
		t.Fatalf("parse: %#v", ds)
	}
	var out bytes.Buffer
	_, err := Run(context.Background(), p, Options{
		HTTPClient: client,
		Config:     &sosconfig.Effective{ExternalNetwork: true},
		Stdout:     &out,
	})
	return out.String(), err
}

func TestHTTPCanonicalFormsParseAsKnownConstructions(t *testing.T) {
	source := strings.Join([]string{
		`get text from "https://example.com/status" called status`,
		`get JSON from endpoint called users`,
		`get JSON from endpoint expecting status 200 through 299 called users`,
		`post order as JSON to "https://api.example.com/orders" called created`,
		`send an HTTP request to endpoint called response:`,
		`  method from "POST"`,
		`listen for HTTP requests on loopback port 8080 called requests:`,
		`  request deadline 30 seconds`,
		`listen for HTTP requests on all interfaces port 8080 called wide:`,
		`  shutdown deadline 10 seconds`,
		`listen for HTTP requests on port 9000 called plain:`,
		`  allow bodies up to 1 MiB`,
		`read text body from request called text`,
		`read JSON body from request as User called user`,
		`respond to request with status 204`,
		`respond to request with status 200 and text "healthy"`,
		`respond to request with status 201 and JSON created`,
		"",
	}, "\n")
	p, ds := Parse(source)
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %#v", ds)
	}
	byLine := map[int]string{}
	var flatten func([]*Statement)
	flatten = func(sts []*Statement) {
		for _, s := range sts {
			byLine[s.Line] = s.Kind
			flatten(s.Body)
		}
	}
	flatten(p.Statements)
	expected := map[int]string{
		1:  "httpGet",
		2:  "httpGet",
		3:  "httpGet",
		4:  "httpPost",
		5:  "httpRequest",
		6:  "field",
		7:  "httpListen",
		8:  "field",
		9:  "httpListen",
		10: "field",
		11: "httpListen",
		12: "field",
		13: "httpReadBody",
		14: "httpReadBody",
		15: "httpRespond",
		16: "httpRespond",
		17: "httpRespond",
	}
	for line, want := range expected {
		if byLine[line] != want {
			t.Fatalf("line %d classified %q, want %q (kinds=%v)", line, byLine[line], want, byLine)
		}
	}
}

func TestHTTPCanonicalConvenienceFormsRun(t *testing.T) {
	var lastBody []byte
	var lastMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastBody, lastMethod = drainBody(r.Body), r.Method
		switch r.URL.Path {
		case "/status":
			_, _ = w.Write([]byte("healthy"))
		case "/users":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"message":"hello"}`))
		case "/orders":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"order_id":7}`))
		}
	}))
	defer server.Close()

	source := strings.Join([]string{
		`get text from ` + quote(server.URL+"/status") + ` called status`,
		`show status`,
		`get JSON from ` + quote(server.URL+"/users") + ` called users`,
		`show message of users`,
		`post {"item": "book"} as JSON to ` + quote(server.URL+"/orders") + ` called created`,
		`show order_id of created`,
		"",
	}, "\n")
	out, err := runHTTPFormsProgram(t, source, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if out != "healthy\nhello\n7\n" {
		t.Fatalf("out=%q", out)
	}
	if lastMethod != "POST" || string(lastBody) != `{"item":"book"}` {
		t.Fatalf("method=%s body=%q", lastMethod, lastBody)
	}
}

func TestHTTPCanonicalCompleteRequestRuns(t *testing.T) {
	var seen http.Request
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = *r
		seenBody = drainBody(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	source := strings.Join([]string{
		`send an HTTP request to ` + quote(server.URL+"/submit?flag=1") + ` called response:`,
		`  method from "POST"`,
		`  headers from {"x-trace": "canonical"}`,
		`  query from {"page": "2"}`,
		`  JSON body from {"note": "hi"}`,
		`  timeout from 10 seconds`,
		`  following redirects from false`,
		`  accepting at most 64 KiB`,
		`show status of response`,
	}, "\n")
	out, err := runHTTPFormsProgram(t, source, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if seen.Method != "POST" || seen.Header.Get("X-Trace") != "canonical" || seen.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("request headers=%v", seen.Header)
	}
	if seen.URL.Query().Get("flag") != "1" || seen.URL.Query().Get("page") != "2" {
		t.Fatalf("query=%v", seen.URL.RawQuery)
	}
	if string(seenBody) != `{"note":"hi"}` {
		t.Fatalf("body=%q", seenBody)
	}
	if !strings.Contains(out, "200") {
		t.Fatalf("out=%q", out)
	}
}

func TestHTTPCanonicalListenerServesAndResponds(t *testing.T) {
	port := freeLocalPort(t)
	source := strings.Join([]string{
		`define Incoming:`,
		`  message as text`,
		`listen for HTTP requests on loopback port ` + fmt.Sprint(port) + ` called requests:`,
		`  allow headers up to 32 KiB`,
		`  allow bodies up to 1 MiB`,
		`  request deadline 5 seconds`,
		`  shutdown deadline 1 second`,
		`for each request from requests:`,
		`  read JSON body from request as Incoming called incoming`,
		`  respond to request with status 202 and JSON {"received": message of incoming}`,
		`  stop reading`,
		`show "server stopped"`,
		"",
	}, "\n")

	outCh := make(chan struct {
		out string
		err error
	}, 1)
	go func() {
		out, err := runHTTPFormsProgram(t, source, nil)
		outCh <- struct {
			out string
			err error
		}{out, err}
	}()

	address := fmt.Sprintf("127.0.0.1:%d", port)
	var response *http.Response
	deadline := time.Now().Add(5 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		response, err = http.Post("http://"+address+"/events", "application/json", strings.NewReader(`{"message":"hello"}`))
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if response == nil {
		result := <-outCh
		t.Fatalf("request never succeeded; program out=%q err=%v", result.out, result.err)
	}
	payload := drainBody(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted || string(payload) != `{"received":"hello"}` {
		t.Fatalf("status=%d payload=%q", response.StatusCode, payload)
	}
	result := <-outCh
	if result.err != nil {
		t.Fatalf("program error=%v out=%q", result.err, result.out)
	}
	if result.out != "server stopped\n" {
		t.Fatalf("out=%q", result.out)
	}
}

func TestHTTPCanonicalRespondStatusOnlySendsNoContentType(t *testing.T) {
	port := freeLocalPort(t)
	source := strings.Join([]string{
		`listen for HTTP requests on loopback port ` + fmt.Sprint(port) + ` called requests:`,
		`  request deadline 5 seconds`,
		`for each request from requests:`,
		`  respond to request with status 204`,
		`  stop reading`,
		"",
	}, "\n")
	go func() {
		if _, err := runHTTPFormsProgram(t, source, nil); err != nil {
			t.Errorf("program error=%v", err)
		}
	}()
	address := fmt.Sprintf("127.0.0.1:%d", port)
	var response *http.Response
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err = http.Get("http://" + address + "/health")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	payload := drainBody(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent || string(payload) != "" || response.Header.Get("Content-Type") != "" {
		t.Fatalf("status=%d payload=%q content-type=%q", response.StatusCode, payload, response.Header.Get("Content-Type"))
	}
}

func TestHTTPCanonicalReadJSONBodyTypeMismatchIsIsolated(t *testing.T) {
	port := freeLocalPort(t)
	source := strings.Join([]string{
		`define Incoming:`,
		`  message as text`,
		`listen for HTTP requests on loopback port ` + fmt.Sprint(port) + ` called requests:`,
		`  request deadline 5 seconds`,
		`for each request from requests:`,
		`  read JSON body from request as Incoming called incoming`,
		`  respond to request with status 200`,
		`  stop reading`,
		"",
	}, "\n")
	done := make(chan error, 1)
	go func() {
		_, err := runHTTPFormsProgram(t, source, nil)
		done <- err
	}()
	address := fmt.Sprintf("127.0.0.1:%d", port)
	var response *http.Response
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err = http.Post("http://"+address, "application/json", strings.NewReader(`{"count":3}`))
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if response.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("invalid request status=%d body=%q, want 400", response.StatusCode, body)
	}
	_ = response.Body.Close()
	response, err = http.Post("http://"+address, "application/json", strings.NewReader(`{"message":"ok"}`))
	if err != nil {
		t.Fatalf("valid request after mismatch failed: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("valid request status=%d, want 200", response.StatusCode)
	}
	_ = response.Body.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("listener failed after isolating typed mismatch: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("program did not terminate")
	}
}

func TestHTTPCanonicalFailureHandlerSeesTypedFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer server.Close()
	source := strings.Join([]string{
		`get JSON from ` + quote(server.URL) + ` called payload`,
		`  on failure UnexpectedHttpStatus:`,
		`    stop with "unexpected status"`,
		"",
	}, "\n")
	_, err := runHTTPFormsProgram(t, source, server.Client())
	var stopped *StopError
	if err == nil || !errors.As(err, &stopped) || stopped.Message != "unexpected status" {
		t.Fatalf("err=%v", err)
	}
}

func TestHTTPCanonicalFormDiagnostics(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   string
	}{
		{"nondefault range", "get JSON from endpoint expecting status 404 through 499 called payload\n", "200 through 299"},
		{"unknown option", "send an HTTP request to endpoint called response:\n  mode from \"POST\"\n", "unknown construction"},
		{"duplicate option", "send an HTTP request to endpoint called response:\n  method from \"POST\"\n  method from \"GET\"\n", "more than once"},
		{"two bodies", "send an HTTP request to endpoint called response:\n  text body from \"a\"\n  JSON body from order\n", "both"},
		{"bad size", "send an HTTP request to endpoint called response:\n  accepting at most plenty\n", "size"},
		{"bad status", "respond to request with status 42\n", "100 through 599"},
		{"bad port", "listen for HTTP requests on loopback port 70000 called requests:\n  request deadline 1 second\n", "1 through 65535"},
		{"form body unsupported", "read form body from request called fields\n", "form"},
		{"empty block", "send an HTTP request to endpoint called response:\n", "expected an indented body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ds := Check(tc.source)
			found := false
			for _, d := range ds {
				if strings.Contains(d.Message, tc.want) {
					found = true
				}
			}
			if !found {
				t.Fatalf("diagnostics=%#v want %q", ds, tc.want)
			}
		})
	}
}

func TestHTTPCanonicalHeaderLimitAndCompleteRespondParse(t *testing.T) {
	if ds := Check("listen for HTTP requests on port 8080 called requests:\n  allow headers up to 32 KiB\n  request deadline 30 seconds\nfor each request from requests:\n  respond to request with status 204\n"); len(ds) != 0 {
		t.Fatalf("header limits are supported: %#v", ds)
	}
	if ds := Check("make request 1\nmake report {\"ok\": true}\nrespond to request with:\n  status from 200\n  headers from {\"cache-control\": \"no-store\"}\n  JSON body from report\n"); len(ds) != 0 {
	}
	for _, source := range []string{
		"respond to request with:\n  status from 200\n  status from 201\n",
		"respond to request with:\n  text body from \"a\"\n  JSON body from order\n",
		"respond to request with:\n  headers from {}\n",
	} {
		if ds := Check(source); len(ds) == 0 {
			t.Fatalf("expected diagnostics for %q", source)
		}
	}
}
func TestHTTPCanonicalCheckerKnowsNamesAndOwnership(t *testing.T) {
	if ds := Check("respond to request with status 200 and text \"ok\"\n"); len(ds) == 0 {
		t.Fatalf("unknown request name must be diagnosed")
	}
	if ds := Check("read text body from request called body\n"); len(ds) == 0 {
		t.Fatalf("unknown request name must be diagnosed")
	}
	if ds := Check("read JSON body from request as Missing called body\n"); len(ds) == 0 {
		t.Fatalf("unknown type must be diagnosed")
	}
	if ds := Check("get text from \"https://example.com\" called status\n  on failure HttpTimeout:\n    recover\n"); len(ds) == 0 {
		t.Fatalf("recover without a value must be diagnosed for a value-returning form")
	}
	streamTwice := strings.Join([]string{
		"listen for HTTP requests on port 8080 called requests:",
		"  request deadline 1 second",
		"listen for HTTP requests on port 8080 called requests:",
		"  request deadline 1 second",
		"for each request from requests:",
		"  respond to request with status 204",
		"",
	}, "\n")
	if ds := Check(streamTwice); len(ds) == 0 {
		t.Fatalf("second active listener binding must be diagnosed")
	}
	consumed := strings.Join([]string{
		"listen for HTTP requests on port 8080 called requests:",
		"  request deadline 1 second",
		"for each request from requests:",
		"  respond to request with status 204",
		"close stream requests",
		"",
	}, "\n")
	found := false
	for _, d := range Check(consumed) {
		if strings.Contains(d.Message, "already consumed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("closing a consumed listener must be diagnosed")
	}
}

func TestHTTPCanonicalKeywordsSurfaceInEditorCatalog(t *testing.T) {
	keywords := Keywords()
	for _, phrase := range []string{"get text from", "get JSON from", "listen for HTTP requests on", "send an HTTP request to", "read JSON body from", "respond to"} {
		found := false
		for _, k := range keywords {
			if k == phrase {
				found = true
			}
		}
		if !found {
			t.Fatalf("keyword %q missing from Keywords()", phrase)
		}
	}
}

func freeLocalPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func drainBody(body io.Reader) []byte {
	if body == nil {
		return nil
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return nil
	}
	return data
}

func quote(value string) string {
	return `"` + value + `"`
}
