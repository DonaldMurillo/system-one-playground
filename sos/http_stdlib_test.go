package sos

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/DonaldMurillo/system-one-playground/sosconfig"
)

func httpOptions(client *http.Client) Options {
	return Options{HTTPClient: client, Config: &sosconfig.Effective{ExternalNetwork: true}}
}

func callHTTP(t *testing.T, name string, opts Options, args ...any) (any, error) {
	t.Helper()
	op := stdRegistry["std/http"][name]
	if err := op.check(args); err != nil {
		return nil, err
	}
	return op.ContextFn(context.Background(), opts, args)
}

type httpRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn httpRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestHTTPCompleteRequestPreservesStatusAndRepeatedHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Query().Get("page") != "2" || r.Header.Get("X-Test") != "yes" {
			t.Errorf("request=%s %s headers=%v", r.Method, r.URL.String(), r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["name"] != "Ada" {
			t.Errorf("body=%v error=%v", body, err)
		}
		w.Header().Add("X-Result", "one")
		w.Header().Add("X-Result", "two")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	result, err := callHTTP(t, "request", httpOptions(server.Client()), map[string]any{
		"url": server.URL, "method": "POST", "query": map[string]any{"page": "2"},
		"headers": map[string]any{"x-test": "yes"}, "json": map[string]any{"name": "Ada"},
		"timeout": 2 * time.Second, "max_bytes": float64(1024),
	})
	if err != nil {
		t.Fatal(err)
	}
	response := result.(map[string]any)
	if response["status"] != float64(201) || response["body"] != `{"ok":true}` || response["final_url"] != server.URL+"?page=2" {
		t.Fatalf("response=%#v", response)
	}
	values := response["headers"].(map[string]any)["x-result"].([]any)
	if len(values) != 2 || values[0] != "one" || values[1] != "two" {
		t.Fatalf("headers=%#v", response["headers"])
	}
}

func TestHTTPRejectsControlCharactersBeforeTransport(t *testing.T) {
	for _, options := range []map[string]any{
		{"url": "https://example.test", "method": "GE\x00T"},
		{"url": "https://example.test", "headers": map[string]any{"x-test": "bad\x01value"}},
	} {
		_, err := parseHTTPRequestOptions(options)
		var failure *typedFailure
		if !errors.As(err, &failure) || failure.kind != "InvalidHttpRequest" {
			t.Fatalf("options=%#v failure=%#v error=%v", options, failure, err)
		}
	}
}

func TestHTTPConvenienceJSONAndUnexpectedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			http.Error(w, "nope", http.StatusTeapot)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"Ada"}`))
	}))
	defer server.Close()

	value, err := callHTTP(t, "get_json", httpOptions(server.Client()), server.URL)
	if err != nil || value.(map[string]any)["name"] != "Ada" {
		t.Fatalf("value=%#v error=%v", value, err)
	}
	_, err = callHTTP(t, "get_text", httpOptions(server.Client()), server.URL+"/bad")
	var failure *typedFailure
	if !errors.As(err, &failure) || failure.kind != "UnexpectedHttpStatus" || failure.value["status"] != float64(418) {
		t.Fatalf("failure=%#v error=%v", failure, err)
	}
}

func TestHTTPUnexpectedStatusPreviewRemainsUTF8(t *testing.T) {
	response := map[string]any{"status": float64(500), "body": strings.Repeat("a", 1023) + "€tail"}
	err := requireHTTPSuccess(response)
	var failure *typedFailure
	if !errors.As(err, &failure) || !utf8.ValidString(failure.value["body_preview"].(string)) {
		t.Fatalf("failure=%#v error=%v", failure, err)
	}
}

func TestHTTPBodyLimitAndNetworkPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 32)))
	}))
	defer server.Close()

	_, err := callHTTP(t, "request", httpOptions(server.Client()), map[string]any{"url": server.URL, "max_bytes": float64(8)})
	var failure *typedFailure
	if !errors.As(err, &failure) || failure.kind != "HttpBodyTooLarge" || failure.value["limit"] != float64(8) {
		t.Fatalf("failure=%#v error=%v", failure, err)
	}
	_, err = callHTTP(t, "get_text", Options{HTTPClient: server.Client(), Config: &sosconfig.Effective{}}, server.URL)
	if err == nil || !strings.Contains(err.Error(), "network capability") {
		t.Fatalf("policy error=%v", err)
	}
}

func TestHTTPTimeoutReportsConfiguredDuration(t *testing.T) {
	timeout := 25 * time.Millisecond
	client := &http.Client{Transport: httpRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	_, err := callHTTP(t, "request", httpOptions(client), map[string]any{
		"url": "https://example.test/slow", "timeout": timeout,
	})
	var failure *typedFailure
	if !errors.As(err, &failure) || failure.kind != "HttpTimeout" || failure.value["duration"] != timeout {
		t.Fatalf("failure=%#v error=%v", failure, err)
	}
}

func TestHTTPRedirectStripsAuthorizationAcrossOrigins(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("authorization leaked: %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, destination.URL, http.StatusFound)
	}))
	defer source.Close()

	result, err := callHTTP(t, "request", httpOptions(source.Client()), map[string]any{
		"url": source.URL, "headers": map[string]any{"authorization": "Bearer secret"}, "follow_redirects": true,
	})
	if err != nil || result.(map[string]any)["body"] != "ok" {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}

func TestHTTPListenerOwnsBodyAndExactlyOneResponse(t *testing.T) {
	registry := newHTTPServerRegistry()
	source, err := openHTTPListener(context.Background(), registry, httpListenerOptions{
		address: "127.0.0.1:0", maxBodyBytes: 1024, requestDeadline: time.Second, shutdown: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.cancel(context.Background()) })

	response := make(chan struct {
		status int
		body   string
		err    error
	}, 1)
	go func() {
		result := struct {
			status int
			body   string
			err    error
		}{}
		httpResponse, requestErr := http.Post("http://"+source.listener.Addr().String()+"/events?kind=create", "application/json", strings.NewReader(`{"path":"a.txt"}`))
		result.err = requestErr
		if requestErr == nil {
			defer httpResponse.Body.Close()
			body, readErr := io.ReadAll(httpResponse.Body)
			result.status, result.body, result.err = httpResponse.StatusCode, string(body), readErr
		}
		response <- result
	}()

	value, more, err := source.next(context.Background())
	if err != nil || !more {
		t.Fatalf("next more=%v error=%v", more, err)
	}
	request := value.(map[string]any)
	if request["method"] != "POST" || request["path"] != "/events" || request["scheme"] != "http" {
		t.Fatalf("request=%#v", request)
	}
	opts := Options{httpServers: registry}
	body, err := readHTTPJSONBody(context.Background(), opts, []any{request})
	if err != nil || body.(map[string]any)["path"] != "a.txt" {
		t.Fatalf("body=%#v error=%v", body, err)
	}
	if _, err = respondHTTPJSON(context.Background(), opts, []any{request, float64(202), map[string]any{"accepted": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err = respondHTTPText(context.Background(), opts, []any{request, float64(200), "again"}); err == nil || !strings.Contains(err.Error(), "already sent") {
		t.Fatalf("second response error=%v", err)
	}
	result := <-response
	if result.err != nil || result.status != 202 || result.body != `{"accepted":true}` {
		t.Fatalf("response=%+v", result)
	}
}

func TestHTTPListenerRejectsOversizedBodiesBeforeAdmission(t *testing.T) {
	registry := newHTTPServerRegistry()
	source, err := openHTTPListener(context.Background(), registry, httpListenerOptions{
		address: "127.0.0.1:0", maxBodyBytes: 4, requestDeadline: time.Second, shutdown: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer source.cancel(context.Background())
	response, err := http.Post("http://"+source.listener.Addr().String(), "text/plain", strings.NewReader("12345"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d", response.StatusCode)
	}
	registry.mu.Lock()
	active := len(registry.active)
	registry.mu.Unlock()
	if active != 0 {
		t.Fatalf("oversized request created %d obligations", active)
	}
}

func TestHTTPListenerOptionsBoundHeadersAndSocketDeadlines(t *testing.T) {
	deadline := 250 * time.Millisecond
	options, err := parseHTTPListenerOptions(map[string]any{
		"address": "127.0.0.1:0", "max_header_bytes": float64(4096), "request_deadline": deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.maxHeaderBytes != 4096 {
		t.Fatalf("maxHeaderBytes=%d", options.maxHeaderBytes)
	}
	source, err := openHTTPListener(context.Background(), newHTTPServerRegistry(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer source.cancel(context.Background())
	if source.server.MaxHeaderBytes != 4096 || source.server.ReadHeaderTimeout != deadline || source.server.ReadTimeout != deadline || source.server.WriteTimeout != deadline {
		t.Fatalf("server limits=%+v", source.server)
	}

	_, err = parseHTTPListenerOptions(map[string]any{"max_header_bytes": float64(absoluteHTTPMaxHeaderBytes + 1)})
	if err == nil || !strings.Contains(err.Error(), "max_header_bytes") {
		t.Fatalf("invalid header limit error=%v", err)
	}
}

func TestHTTPListenerBoundsAdmission(t *testing.T) {
	source, err := openHTTPListener(context.Background(), newHTTPServerRegistry(), httpListenerOptions{
		address: "127.0.0.1:0", maxBodyBytes: 1024, maxInFlight: 1, requestDeadline: time.Second, shutdown: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer source.cancel(context.Background())
	source.admission <- struct{}{}
	response, err := http.Get("http://" + source.listener.Addr().String())
	<-source.admission
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", response.StatusCode)
	}
}

func TestHTTPListenerShutdownLetsInflightRequestFinish(t *testing.T) {
	registry := newHTTPServerRegistry()
	source, err := openHTTPListener(context.Background(), registry, httpListenerOptions{
		address: "127.0.0.1:0", maxBodyBytes: 1024, maxInFlight: 1, requestDeadline: time.Second, shutdown: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	responseDone := make(chan *http.Response, 1)
	go func() {
		response, _ := http.Get("http://" + source.listener.Addr().String())
		responseDone <- response
	}()
	value, more, err := source.next(context.Background())
	if err != nil || !more {
		t.Fatalf("next more=%v error=%v", more, err)
	}
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- source.cancel(context.Background()) }()
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown completed before the in-flight response: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if _, err := respondHTTPText(context.Background(), Options{httpServers: registry}, []any{value, float64(200), "done"}); err != nil {
		t.Fatal(err)
	}
	if response := <-responseDone; response == nil || response.StatusCode != http.StatusOK {
		t.Fatalf("response=%v", response)
	} else {
		response.Body.Close()
	}
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
}

func TestHTTPRejectsForgedRequestAuthorityAndNonJSONBody(t *testing.T) {
	registry := newHTTPServerRegistry()
	id, obligation := registry.register([]byte(`{"ok":true}`))
	opts := Options{httpServers: registry}
	forged := map[string]any{"id": id, "__http_authority": &httpResponseObligation{}, "headers": map[string]any{"content-type": []any{"application/json"}}}
	if _, err := respondHTTPText(context.Background(), opts, []any{forged, float64(200), "bad"}); err == nil {
		t.Fatal("forged request authority was accepted")
	}
	request := map[string]any{"id": id, "__http_authority": obligation, "headers": map[string]any{"content-type": []any{"text/plain"}}}
	if _, err := readHTTPJSONBody(context.Background(), opts, []any{request}); err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("content-type error=%v", err)
	}
}

func TestHTTPReleasedRequestReportsClientDisconnect(t *testing.T) {
	registry := newHTTPServerRegistry()
	id, obligation := registry.register(nil)
	request := map[string]any{"id": id, "__http_authority": obligation}
	registry.release(id, "disconnected")
	_, err := respondHTTPText(context.Background(), Options{httpServers: registry}, []any{request, float64(200), "late"})
	var failure *typedFailure
	if !errors.As(err, &failure) || failure.kind != "HttpClientDisconnected" || failure.value["request_id"] != id {
		t.Fatalf("failure=%#v error=%v", failure, err)
	}
}

func TestHTTPCompleteResponseSupportsHeadersAndStatusOnly(t *testing.T) {
	registry := newHTTPServerRegistry()
	id, obligation := registry.register(nil)
	request := map[string]any{"id": id, "__http_authority": obligation}
	_, err := respondHTTPComplete(context.Background(), Options{httpServers: registry}, []any{request, map[string]any{
		"status": float64(201), "headers": map[string]any{"x-result": []any{"one", "two"}}, "json": map[string]any{"ok": true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if obligation.response.status != 201 || obligation.response.headers.Values("X-Result")[1] != "two" || obligation.response.headers.Get("Content-Type") != "application/json" {
		t.Fatalf("response=%#v", obligation.response)
	}

	id, obligation = registry.register(nil)
	request = map[string]any{"id": id, "__http_authority": obligation}
	if _, err := respondHTTPStatus(context.Background(), Options{httpServers: registry}, []any{request, float64(204)}); err != nil {
		t.Fatal(err)
	}
	if len(obligation.response.body) != 0 || len(obligation.response.headers) != 0 {
		t.Fatalf("status-only response=%#v", obligation.response)
	}
}

func TestHTTPResponseRejectsInvalidBodyAndManagedHeaders(t *testing.T) {
	registry := newHTTPServerRegistry()
	id, obligation := registry.register(nil)
	request := map[string]any{"id": id, "__http_authority": obligation}
	if _, err := respondHTTPText(context.Background(), Options{httpServers: registry}, []any{request, float64(204), "body"}); err == nil {
		t.Fatal("204 response body was accepted")
	}
	if _, err := respondHTTPComplete(context.Background(), Options{httpServers: registry}, []any{request, map[string]any{
		"status": float64(200), "headers": map[string]any{"content-length": "999"}, "text": "short",
	}}); err == nil {
		t.Fatal("managed content-length header was accepted")
	}
}

func TestHTTPListenerClassifiesNonAddressInUseBindFailure(t *testing.T) {
	_, err := openHTTPListener(context.Background(), newHTTPServerRegistry(), httpListenerOptions{address: "127.0.0.1:-1"})
	var failure *typedFailure
	if !errors.As(err, &failure) || failure.kind != "HttpListenerFailed" {
		t.Fatalf("failure=%#v error=%v", failure, err)
	}
}

func TestHTTPUnansweredRequestGetsSafeResponseAndTypedFailure(t *testing.T) {
	registry := newHTTPServerRegistry()
	id, obligation := registry.register(nil)
	request := map[string]any{"id": id, "path": "/forgotten", "__http_authority": obligation}
	err := registry.ensureResponded(request)
	var failure *typedFailure
	if !errors.As(err, &failure) || failure.kind != "UnansweredHttpRequest" {
		t.Fatalf("failure=%#v error=%v", failure, err)
	}
	select {
	case <-obligation.done:
		if obligation.response.status != http.StatusInternalServerError || string(obligation.response.body) != "Internal Server Error\n" {
			t.Fatalf("response=%#v", obligation.response)
		}
	default:
		t.Fatal("unanswered request was not completed")
	}
}
