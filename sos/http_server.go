package sos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	defaultHTTPMaxHeaderBytes  = 32 << 10
	absoluteHTTPMaxHeaderBytes = 1 << 20
	defaultHTTPIdleTimeout     = 60 * time.Second
)

type httpServerRegistry struct {
	mu        sync.Mutex
	next      atomic.Uint64
	active    map[string]*httpResponseObligation
	completed map[string]string
	order     []string
}

type httpResponse struct {
	status  int
	headers http.Header
	body    []byte
}

type httpResponseObligation struct {
	mu       sync.Mutex
	id       string
	done     chan struct{}
	response httpResponse
	finished bool
	body     []byte
	bodyRead bool
}

func newHTTPServerRegistry() *httpServerRegistry {
	return &httpServerRegistry{active: map[string]*httpResponseObligation{}, completed: map[string]string{}}
}

func (r *httpServerRegistry) register(body []byte) (string, *httpResponseObligation) {
	id := fmt.Sprintf("http-request-%d", r.next.Add(1))
	obligation := &httpResponseObligation{id: id, done: make(chan struct{}), body: body}
	r.mu.Lock()
	r.active[id] = obligation
	r.mu.Unlock()
	return id, obligation
}

func (r *httpServerRegistry) obligation(id string, authority *httpResponseObligation) (*httpResponseObligation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if obligation := r.active[id]; obligation != nil && obligation == authority {
		return obligation, nil
	}
	if reason, exists := r.completed[id]; exists {
		if reason == "responded" {
			return nil, httpFailure("HttpResponseAlreadySent", "HTTP response was already sent", map[string]any{"request_id": id})
		}
		return nil, httpFailure("HttpClientDisconnected", "HTTP client disconnected before a response was sent", map[string]any{"request_id": id})
	}
	return nil, httpFailure("InvalidHttpRequest", "HTTP request is no longer active", map[string]any{"reason": "unknown or expired request identity"})
}

func (r *httpServerRegistry) release(id, reason string) {
	r.mu.Lock()
	delete(r.active, id)
	r.completed[id] = reason
	r.order = append(r.order, id)
	if len(r.order) > 1024 {
		delete(r.completed, r.order[0])
		r.order = r.order[1:]
	}
	r.mu.Unlock()
}

func (r *httpServerRegistry) ensureResponded(value any) error {
	request, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	id, _ := request["id"].(string)
	authority, _ := request["__http_authority"].(*httpResponseObligation)
	if id == "" || authority == nil {
		return nil
	}
	r.mu.Lock()
	obligation := r.active[id]
	r.mu.Unlock()
	if obligation == nil || obligation != authority {
		return nil
	}
	obligation.mu.Lock()
	finished := obligation.finished
	obligation.mu.Unlock()
	if finished {
		return nil
	}
	response := httpResponse{status: http.StatusInternalServerError, headers: make(http.Header), body: []byte("Internal Server Error\n")}
	response.headers.Set("Content-Type", "text/plain; charset=utf-8")
	_ = obligation.complete(response)
	path, _ := request["path"].(string)
	return httpFailure("UnansweredHttpRequest", "HTTP request handler finished without sending a response", map[string]any{"request_id": id, "path": path})
}

// completeUnhandledFailure turns an unhandled request-scoped runtime failure
// into a bounded HTTP response. The listener remains alive for later requests;
// explicit SysOneScript failure handlers still take precedence because this is
// called only after the request body has returned an error to the stream loop.
func (r *httpServerRegistry) completeUnhandledFailure(value any, failure error) bool {
	request, ok := value.(map[string]any)
	if !ok {
		return false
	}
	id, _ := request["id"].(string)
	authority, _ := request["__http_authority"].(*httpResponseObligation)
	if id == "" || authority == nil {
		return false
	}
	r.mu.Lock()
	obligation := r.active[id]
	r.mu.Unlock()
	if obligation == nil || obligation != authority {
		return true
	}

	status := http.StatusInternalServerError
	message := "request handler failed"
	var typed *typedFailure
	if errors.As(failure, &typed) {
		switch typed.kind {
		case "InvalidHttpBody", "InvalidHttpRequest":
			status, message = http.StatusBadRequest, "invalid request"
		case "HttpRequestTooLarge":
			status, message = http.StatusRequestEntityTooLarge, "request body is too large"
		case "HttpServerShuttingDown":
			status, message = http.StatusServiceUnavailable, "server is shutting down"
		}
	}
	body, _ := json.Marshal(map[string]any{"error": message})
	response := httpResponse{status: status, headers: make(http.Header), body: body}
	response.headers.Set("Content-Type", "application/json")
	response.headers.Set("Cache-Control", "no-store")
	_ = obligation.complete(response)
	return true
}

func (o *httpResponseObligation) complete(response httpResponse) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		return httpFailure("HttpResponseAlreadySent", "HTTP response was already sent", map[string]any{"request_id": o.id})
	}
	o.response, o.finished = response, true
	close(o.done)
	return nil
}

func (o *httpResponseObligation) readBody() ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.bodyRead {
		return nil, httpFailure("InvalidHttpBody", "HTTP request body was already consumed", map[string]any{"reason": "body was already consumed"})
	}
	o.bodyRead = true
	return append([]byte(nil), o.body...), nil
}

type httpListenerOptions struct {
	address         string
	maxHeaderBytes  int
	maxBodyBytes    int64
	requestDeadline time.Duration
	shutdown        time.Duration
	maxInFlight     int
	maxConnections  int
}

type httpListenerSource struct {
	ctx       context.Context
	cancelRun context.CancelFunc
	listener  net.Listener
	server    *http.Server
	registry  *httpServerRegistry
	requests  chan map[string]any
	done      chan struct{}
	errMu     sync.Mutex
	err       error
	shutdown  time.Duration
	admission chan struct{}
}

type boundedHTTPListener struct {
	net.Listener
	permits chan struct{}
}

func (l *boundedHTTPListener) Accept() (net.Conn, error) {
	l.permits <- struct{}{}
	connection, err := l.Listener.Accept()
	if err != nil {
		<-l.permits
		return nil, err
	}
	return &boundedHTTPConnection{Conn: connection, release: func() { <-l.permits }}, nil
}

type boundedHTTPConnection struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *boundedHTTPConnection) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

func init() {
	if stdRegistry["std/http"] == nil {
		stdRegistry["std/http"] = map[string]NativeOp{}
	}
	serverFailures := []string{"HttpAddressInUse", "HttpListenerFailed", "InvalidHttpBody", "HttpRequestTooLarge", "HttpResponseAlreadySent", "HttpClientDisconnected", "HttpServerShuttingDown", "UnansweredHttpRequest"}
	stdRegistry["std/http"]["listen"] = NativeOp{
		Name: "listen", Params: []NativeParam{{Name: "options", Type: "record"}}, Result: "stream of any",
		Targets: []string{"native"}, Effects: []string{"network-listen"}, PossibleFailures: serverFailures,
		Description: "Listens for bounded HTTP requests and emits independently owned response obligations.",
		StreamFn: func(ctx context.Context, opts Options, args []any) (streamSource, error) {
			if opts.Config == nil || !opts.Config.ExternalNetwork {
				return nil, fmt.Errorf("std/http requires network capability enabled by project policy")
			}
			configuration, err := parseHTTPListenerOptions(args[0].(map[string]any))
			if err != nil {
				return nil, err
			}
			return openHTTPListener(ctx, opts.httpServers, configuration)
		},
	}
	stdDocs["std/http.listen"] = stdDoc{"stream of any", "Listens for bounded HTTP requests and emits response obligations.", []string{"network-listen"}}
	registerHTTPServerOp("read_text_body", []NativeParam{{"request", "record"}}, "text", "Consumes one request body as bounded UTF-8 text.", serverFailures, readHTTPTextBody)
	registerHTTPServerOp("read_json_body", []NativeParam{{"request", "record"}}, "any", "Consumes and decodes one bounded request body as JSON.", serverFailures, readHTTPJSONBody)
	registerHTTPServerOp("respond_status", []NativeParam{{"request", "record"}, {"status", "integer"}}, "text", "Completes one HTTP request with a status and no body.", serverFailures, respondHTTPStatus)
	registerHTTPServerOp("respond_text", []NativeParam{{"request", "record"}, {"status", "integer"}, {"text", "text"}}, "text", "Completes one HTTP request with a text response.", serverFailures, respondHTTPText)
	registerHTTPServerOp("respond_json", []NativeParam{{"request", "record"}, {"status", "integer"}, {"value", "any"}}, "text", "Completes one HTTP request with a JSON response.", serverFailures, respondHTTPJSON)
	registerHTTPServerOp("respond", []NativeParam{{"request", "record"}, {"options", "record"}}, "text", "Completes one HTTP request with bounded status, headers, and an optional text or JSON body.", serverFailures, respondHTTPComplete)
}

func registerHTTPServerOp(name string, params []NativeParam, result, description string, failures []string, fn func(context.Context, Options, []any) (any, error)) {
	stdRegistry["std/http"][name] = NativeOp{Name: name, Params: params, Result: result, Targets: []string{"native"}, Effects: []string{"network-listen"}, Description: description, PossibleFailures: append([]string(nil), failures...), ContextFn: fn}
	stdDocs["std/http."+name] = stdDoc{result, description, []string{"network-listen"}}
}

func parseHTTPListenerOptions(record map[string]any) (httpListenerOptions, error) {
	allowed := map[string]bool{"address": true, "max_header_bytes": true, "max_body_bytes": true, "max_in_flight": true, "max_connections": true, "request_deadline": true, "shutdown_deadline": true}
	for name := range record {
		if !allowed[name] {
			return httpListenerOptions{}, invalidHTTPRequest("unknown listener option " + name)
		}
	}
	result := httpListenerOptions{address: "127.0.0.1:8080", maxHeaderBytes: defaultHTTPMaxHeaderBytes, maxBodyBytes: 1 << 20, requestDeadline: 30 * time.Second, shutdown: 10 * time.Second, maxInFlight: 128, maxConnections: 256}
	if value, exists := record["address"]; exists {
		address, ok := value.(string)
		if !ok || address == "" {
			return httpListenerOptions{}, invalidHTTPRequest("listener address must be nonempty text")
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil || (host != "127.0.0.1" && host != "::1" && host != "localhost" && host != "0.0.0.0" && host != "::") {
			return httpListenerOptions{}, invalidHTTPRequest("listener address must contain an explicit loopback or all-interfaces host and port")
		}
		result.address = address
	}
	if value, exists := record["max_body_bytes"]; exists {
		n, ok := boundedHTTPInteger(value, 0, absoluteHTTPMaxBytes)
		if !ok {
			return httpListenerOptions{}, invalidHTTPRequest(fmt.Sprintf("max_body_bytes must be an integer from 0 through %d", absoluteHTTPMaxBytes))
		}
		result.maxBodyBytes = n
	}
	if value, exists := record["max_header_bytes"]; exists {
		n, ok := boundedHTTPInteger(value, 1, absoluteHTTPMaxHeaderBytes)
		if !ok {
			return httpListenerOptions{}, invalidHTTPRequest(fmt.Sprintf("max_header_bytes must be an integer from 1 through %d", absoluteHTTPMaxHeaderBytes))
		}
		result.maxHeaderBytes = int(n)
	}
	if value, exists := record["max_in_flight"]; exists {
		n, ok := boundedHTTPInteger(value, 1, 4096)
		if !ok {
			return httpListenerOptions{}, invalidHTTPRequest("max_in_flight must be an integer from 1 through 4096")
		}
		result.maxInFlight = int(n)
	}
	if value, exists := record["max_connections"]; exists {
		n, ok := boundedHTTPInteger(value, 1, 8192)
		if !ok {
			return httpListenerOptions{}, invalidHTTPRequest("max_connections must be an integer from 1 through 8192")
		}
		result.maxConnections = int(n)
	}
	for name, target := range map[string]*time.Duration{"request_deadline": &result.requestDeadline, "shutdown_deadline": &result.shutdown} {
		if value, exists := record[name]; exists {
			duration, ok := value.(time.Duration)
			if !ok || duration <= 0 {
				return httpListenerOptions{}, invalidHTTPRequest(name + " must be a positive duration")
			}
			*target = duration
		}
	}
	return result, nil
}

func boundedHTTPInteger(value any, minimum, maximum int64) (int64, bool) {
	n, ok := number(value)
	if !ok || n != float64(int64(n)) || n < float64(minimum) || n > float64(maximum) {
		return 0, false
	}
	return int64(n), true
}

func openHTTPListener(parent context.Context, registry *httpServerRegistry, options httpListenerOptions) (*httpListenerSource, error) {
	if options.maxHeaderBytes == 0 {
		options.maxHeaderBytes = defaultHTTPMaxHeaderBytes
	}
	if options.maxInFlight == 0 {
		options.maxInFlight = 128
	}
	if options.maxConnections == 0 {
		options.maxConnections = 256
	}
	baseListener, err := net.Listen("tcp", options.address)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return nil, httpFailure("HttpAddressInUse", "HTTP listener could not bind", map[string]any{"address": options.address})
		}
		return nil, httpFailure("HttpListenerFailed", "HTTP listener could not start", map[string]any{"address": options.address, "reason": err.Error()})
	}
	listener := &boundedHTTPListener{Listener: baseListener, permits: make(chan struct{}, options.maxConnections)}
	ctx, cancel := context.WithCancel(parent)
	source := &httpListenerSource{ctx: ctx, cancelRun: cancel, listener: listener, registry: registry, requests: make(chan map[string]any), done: make(chan struct{}), shutdown: options.shutdown, admission: make(chan struct{}, options.maxInFlight)}
	source.server = &http.Server{
		ReadHeaderTimeout: options.requestDeadline,
		ReadTimeout:       options.requestDeadline,
		WriteTimeout:      options.requestDeadline,
		IdleTimeout:       defaultHTTPIdleTimeout,
		MaxHeaderBytes:    options.maxHeaderBytes,
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			source.handleRequest(writer, request, options)
		}),
	}
	go func() {
		err := source.server.Serve(listener)
		if err != nil && err != http.ErrServerClosed {
			source.errMu.Lock()
			source.err = httpFailure("HttpListenerFailed", "HTTP listener failed", map[string]any{"address": listener.Addr().String(), "reason": err.Error()})
			source.errMu.Unlock()
		}
		close(source.done)
	}()
	return source, nil
}

func (s *httpListenerSource) handleRequest(writer http.ResponseWriter, request *http.Request, options httpListenerOptions) {
	select {
	case s.admission <- struct{}{}:
		defer func() { <-s.admission }()
	default:
		http.Error(writer, "Server overloaded", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, options.maxBodyBytes+1))
	_ = request.Body.Close()
	if err != nil {
		http.Error(writer, "Could not read request body", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > options.maxBodyBytes {
		http.Error(writer, "Request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	id, obligation := s.registry.register(body)
	releaseReason := "responded"
	defer func() { s.registry.release(id, releaseReason) }()
	requestContext, cancel := context.WithTimeout(request.Context(), options.requestDeadline)
	defer cancel()
	value := serverHTTPRequestValue(id, obligation, request)
	select {
	case s.requests <- value:
	case <-requestContext.Done():
		releaseReason = "disconnected"
		http.Error(writer, "Request canceled", http.StatusRequestTimeout)
		return
	case <-s.ctx.Done():
		releaseReason = "shutdown"
		http.Error(writer, "Server shutting down", http.StatusServiceUnavailable)
		return
	}
	select {
	case <-obligation.done:
		obligation.mu.Lock()
		response := obligation.response
		obligation.mu.Unlock()
		for name, values := range response.headers {
			for _, value := range values {
				writer.Header().Add(name, value)
			}
		}
		writer.WriteHeader(response.status)
		_, _ = writer.Write(response.body)
	case <-requestContext.Done():
		releaseReason = "disconnected"
		http.Error(writer, "Request deadline exceeded", http.StatusGatewayTimeout)
	case <-s.ctx.Done():
		releaseReason = "shutdown"
		http.Error(writer, "Server shutting down", http.StatusServiceUnavailable)
	}
}

func serverHTTPRequestValue(id string, authority *httpResponseObligation, request *http.Request) map[string]any {
	headers := map[string]any{}
	for name, values := range request.Header {
		items := make([]any, len(values))
		for index, value := range values {
			items[index] = value
		}
		headers[strings.ToLower(name)] = items
	}
	query := map[string]any{}
	for name, values := range request.URL.Query() {
		items := make([]any, len(values))
		for index, value := range values {
			items[index] = value
		}
		query[name] = items
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	return map[string]any{"id": id, "method": request.Method, "scheme": scheme, "host": request.Host, "path": request.URL.Path, "query": query, "headers": headers, "remote_address": request.RemoteAddr, "received_at": time.Now().UTC(), "__http_authority": authority}
}

func (s *httpListenerSource) next(ctx context.Context) (any, bool, error) {
	select {
	case request := <-s.requests:
		return request, true, nil
	case <-s.done:
		s.errMu.Lock()
		err := s.err
		s.errMu.Unlock()
		return nil, false, err
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

func (s *httpListenerSource) cancel(ctx context.Context) error {
	shutdownContext := ctx
	if _, hasDeadline := shutdownContext.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		shutdownContext, cancel = context.WithTimeout(context.Background(), s.shutdown)
		defer cancel()
	}
	err := s.server.Shutdown(shutdownContext)
	if err != nil {
		_ = s.server.Close()
	}
	s.cancelRun()
	select {
	case <-s.done:
	case <-shutdownContext.Done():
		if err == nil {
			err = shutdownContext.Err()
		}
	}
	return err
}

func requestIdentity(value any) (string, *httpResponseObligation, error) {
	request, ok := value.(map[string]any)
	if !ok {
		return "", nil, invalidHTTPRequest("request must be an HTTP request record")
	}
	id, ok := request["id"].(string)
	if !ok || id == "" {
		return "", nil, invalidHTTPRequest("request record has no identity")
	}
	authority, ok := request["__http_authority"].(*httpResponseObligation)
	if !ok || authority == nil {
		return "", nil, invalidHTTPRequest("request record has no response authority")
	}
	return id, authority, nil
}

func readHTTPTextBody(_ context.Context, opts Options, args []any) (any, error) {
	id, authority, err := requestIdentity(args[0])
	if err != nil {
		return nil, err
	}
	obligation, err := opts.httpServers.obligation(id, authority)
	if err != nil {
		return nil, err
	}
	body, err := obligation.readBody()
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(body) {
		return nil, httpFailure("InvalidHttpBody", "HTTP request body is not valid UTF-8", map[string]any{"reason": "body is not valid UTF-8"})
	}
	return string(body), nil
}

func readHTTPJSONBody(ctx context.Context, opts Options, args []any) (any, error) {
	request, ok := args[0].(map[string]any)
	if !ok {
		return nil, invalidHTTPRequest("request must be an HTTP request record")
	}
	headers, ok := request["headers"].(map[string]any)
	if !ok {
		return nil, invalidHTTPRequest("request record has no headers")
	}
	contentType := firstHTTPHeader(headers, "content-type")
	media := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if media != "application/json" && !strings.HasSuffix(media, "+json") {
		return nil, httpFailure("InvalidHttpBody", "HTTP request body is not JSON", map[string]any{"reason": "content type is not JSON"})
	}
	text, err := readHTTPTextBody(ctx, opts, args)
	if err != nil {
		return nil, err
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(text.(string)))
	if err := decoder.Decode(&value); err != nil {
		return nil, httpFailure("InvalidHttpBody", "HTTP request body is not valid JSON", map[string]any{"reason": err.Error()})
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, httpFailure("InvalidHttpBody", "HTTP request body contains more than one JSON value", map[string]any{"reason": "multiple JSON values"})
	}
	return value, nil
}

func respondHTTPText(_ context.Context, opts Options, args []any) (any, error) {
	return completeHTTPResponse(opts, args[0], args[1], []byte(args[2].(string)), "text/plain; charset=utf-8")
}

func respondHTTPStatus(_ context.Context, opts Options, args []any) (any, error) {
	return completeHTTPResponseWithHeaders(opts, args[0], args[1], nil, nil)
}

func respondHTTPJSON(_ context.Context, opts Options, args []any) (any, error) {
	body, err := json.Marshal(args[2])
	if err != nil {
		return nil, httpFailure("InvalidHttpBody", "HTTP response value cannot be encoded as JSON", map[string]any{"reason": err.Error()})
	}
	return completeHTTPResponse(opts, args[0], args[1], body, "application/json")
}

func respondHTTPComplete(_ context.Context, opts Options, args []any) (any, error) {
	record := args[1].(map[string]any)
	allowed := map[string]bool{"status": true, "headers": true, "text": true, "json": true}
	for name := range record {
		if !allowed[name] {
			return nil, invalidHTTPRequest("unknown response option " + name)
		}
	}
	status, exists := record["status"]
	if !exists {
		return nil, invalidHTTPRequest("response status is required")
	}
	headers := make(http.Header)
	if value, exists := record["headers"]; exists {
		recordHeaders, ok := value.(map[string]any)
		if !ok {
			return nil, invalidHTTPRequest("response headers must be a record")
		}
		for name, raw := range recordHeaders {
			if !validHTTPHeaderName(name) || strings.EqualFold(name, "content-length") {
				return nil, invalidHTTPRequest("invalid or managed response header " + name)
			}
			values, err := httpStringValues(raw, "response header "+name)
			if err != nil {
				return nil, err
			}
			for _, value := range values {
				if !validHTTPHeaderValue(value) {
					return nil, invalidHTTPRequest("response header values cannot contain control characters")
				}
				headers.Add(name, value)
			}
		}
	}
	textValue, hasText := record["text"]
	jsonValue, hasJSON := record["json"]
	if hasText && hasJSON {
		return nil, invalidHTTPRequest("response cannot have both text and JSON bodies")
	}
	var body []byte
	if hasText {
		text, ok := textValue.(string)
		if !ok {
			return nil, invalidHTTPRequest("response text body must be text")
		}
		body = []byte(text)
		if headers.Get("Content-Type") == "" {
			headers.Set("Content-Type", "text/plain; charset=utf-8")
		}
	}
	if hasJSON {
		encoded, err := json.Marshal(jsonValue)
		if err != nil {
			return nil, httpFailure("InvalidHttpBody", "HTTP response value cannot be encoded as JSON", map[string]any{"reason": err.Error()})
		}
		body = encoded
		if headers.Get("Content-Type") == "" {
			headers.Set("Content-Type", "application/json")
		}
	}
	return completeHTTPResponseWithHeaders(opts, args[0], status, body, headers)
}

func completeHTTPResponse(opts Options, requestValue, statusValue any, body []byte, contentType string) (any, error) {
	headers := make(http.Header)
	if contentType != "" {
		headers.Set("Content-Type", contentType)
	}
	return completeHTTPResponseWithHeaders(opts, requestValue, statusValue, body, headers)
}

func completeHTTPResponseWithHeaders(opts Options, requestValue, statusValue any, body []byte, headers http.Header) (any, error) {
	id, authority, err := requestIdentity(requestValue)
	if err != nil {
		return nil, err
	}
	status, ok := boundedHTTPInteger(statusValue, 100, 599)
	if !ok {
		return nil, invalidHTTPRequest("response status must be an integer from 100 through 599")
	}
	if len(body) > 0 && (status < 200 || status == http.StatusNoContent || status == http.StatusNotModified) {
		return nil, invalidHTTPRequest(fmt.Sprintf("response status %d cannot include a body", status))
	}
	obligation, err := opts.httpServers.obligation(id, authority)
	if err != nil {
		return nil, err
	}
	if headers == nil {
		headers = make(http.Header)
	}
	response := httpResponse{status: int(status), headers: headers.Clone(), body: body}
	if err := obligation.complete(response); err != nil {
		return nil, err
	}
	return id, nil
}

func addHTTPServerFailureDefinitions(failures map[string]*FailureDef) {
	definitions := map[string][]RecordField{
		"HttpAddressInUse":        {{Name: "address", Type: TypeRef{Name: "text"}}},
		"HttpListenerFailed":      {{Name: "address", Type: TypeRef{Name: "text"}}, {Name: "reason", Type: TypeRef{Name: "text"}}},
		"InvalidHttpBody":         {{Name: "reason", Type: TypeRef{Name: "text"}}},
		"HttpRequestTooLarge":     {{Name: "limit", Type: TypeRef{Name: "integer"}}, {Name: "observed", Type: TypeRef{Name: "integer", Optional: true}}},
		"HttpResponseAlreadySent": {{Name: "request_id", Type: TypeRef{Name: "text"}}},
		"HttpClientDisconnected":  {{Name: "request_id", Type: TypeRef{Name: "text"}}},
		"HttpServerShuttingDown":  {{Name: "address", Type: TypeRef{Name: "text"}}},
		"UnansweredHttpRequest":   {{Name: "request_id", Type: TypeRef{Name: "text"}}, {Name: "path", Type: TypeRef{Name: "text"}}},
	}
	for name, fields := range definitions {
		failures[name] = &FailureDef{Name: name, Fields: fields}
	}
}
