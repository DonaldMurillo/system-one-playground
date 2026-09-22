package sos

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultHTTPMaxBytes  = 2 << 20
	absoluteHTTPMaxBytes = 16 << 20
	defaultHTTPTimeout   = 30 * time.Second
	maxHTTPRedirects     = 10
)

var httpClientFailures = []string{
	"InvalidHttpRequest", "HttpConnectionFailed", "HttpTimeout", "HttpTlsFailure",
	"HttpBodyTooLarge", "InvalidHttpResponse", "UnexpectedHttpStatus", "HttpRedirectRejected",
}

type httpRequestOptions struct {
	url             string
	method          string
	headers         http.Header
	query           url.Values
	body            []byte
	contentType     string
	timeout         time.Duration
	maxBytes        int64
	followRedirects bool
}

func init() {
	registerHTTP := func(name string, params []NativeParam, result, description string, fn func(context.Context, Options, []any) (any, error)) {
		if stdRegistry["std/http"] == nil {
			stdRegistry["std/http"] = map[string]NativeOp{}
		}
		stdRegistry["std/http"][name] = NativeOp{
			Name: name, Params: params, Result: result, ContextFn: fn,
			Targets: []string{"native"}, Effects: []string{"network"},
			Description: description, PossibleFailures: append([]string(nil), httpClientFailures...),
		}
		stdDocs["std/http."+name] = stdDoc{result, description, []string{"network"}}
	}
	registerHTTP("request", []NativeParam{{"options", "record"}}, "record", "Sends one bounded HTTP request and returns status, repeated headers, text body, and final URL.", func(ctx context.Context, opts Options, args []any) (any, error) {
		request, err := parseHTTPRequestOptions(args[0].(map[string]any))
		if err != nil {
			return nil, err
		}
		return performHTTPRequest(ctx, opts, request)
	})
	registerHTTP("get_text", []NativeParam{{"url", "text"}}, "text", "Gets a bounded UTF-8 text response and requires status 200 through 299.", func(ctx context.Context, opts Options, args []any) (any, error) {
		request, err := parseHTTPRequestOptions(map[string]any{"url": args[0], "method": "GET", "follow_redirects": true})
		if err != nil {
			return nil, err
		}
		response, err := performHTTPRequest(ctx, opts, request)
		if err != nil {
			return nil, err
		}
		if err := requireHTTPSuccess(response); err != nil {
			return nil, err
		}
		body := response["body"].(string)
		if !utf8.ValidString(body) {
			return nil, httpFailure("InvalidHttpResponse", "HTTP response body is not valid UTF-8", map[string]any{"reason": "response body is not valid UTF-8"})
		}
		return body, nil
	})
	registerHTTP("get_json", []NativeParam{{"url", "text"}}, "any", "Gets and decodes a bounded JSON response and requires status 200 through 299.", func(ctx context.Context, opts Options, args []any) (any, error) {
		return httpJSONConvenience(ctx, opts, "GET", args[0].(string), nil)
	})
	registerHTTP("post_json", []NativeParam{{"url", "text"}, {"value", "any"}}, "any", "Posts JSON, decodes a bounded JSON response, and requires status 200 through 299.", func(ctx context.Context, opts Options, args []any) (any, error) {
		return httpJSONConvenience(ctx, opts, "POST", args[0].(string), args[1])
	})
}

func httpJSONConvenience(ctx context.Context, opts Options, method, endpoint string, value any) (any, error) {
	record := map[string]any{"url": endpoint, "method": method, "follow_redirects": true}
	if method != "GET" {
		record["json"] = value
	}
	request, err := parseHTTPRequestOptions(record)
	if err != nil {
		return nil, err
	}
	response, err := performHTTPRequest(ctx, opts, request)
	if err != nil {
		return nil, err
	}
	if err := requireHTTPSuccess(response); err != nil {
		return nil, err
	}
	contentType := firstHTTPHeader(response["headers"].(map[string]any), "content-type")
	if media := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0])); media != "application/json" && !strings.HasSuffix(media, "+json") {
		return nil, httpFailure("InvalidHttpResponse", "HTTP response is not JSON", map[string]any{"reason": "content type is not JSON"})
	}
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(response["body"].(string)))
	if err := decoder.Decode(&decoded); err != nil {
		return nil, httpFailure("InvalidHttpResponse", "HTTP response contains invalid JSON", map[string]any{"reason": err.Error()})
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, httpFailure("InvalidHttpResponse", "HTTP response contains more than one JSON value", map[string]any{"reason": "multiple JSON values"})
	}
	return decoded, nil
}

func parseHTTPRequestOptions(record map[string]any) (httpRequestOptions, error) {
	allowed := map[string]bool{"url": true, "method": true, "headers": true, "query": true, "text": true, "json": true, "timeout": true, "max_bytes": true, "follow_redirects": true}
	for key := range record {
		if !allowed[key] {
			return httpRequestOptions{}, invalidHTTPRequest("unknown request option " + strconv.Quote(key))
		}
	}
	rawURL, ok := record["url"].(string)
	if !ok || strings.TrimSpace(rawURL) == "" {
		return httpRequestOptions{}, invalidHTTPRequest("url must be nonempty text")
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" || parsedURL.User != nil {
		return httpRequestOptions{}, invalidHTTPRequest("url must be an absolute HTTP or HTTPS URL without credentials")
	}
	request := httpRequestOptions{url: parsedURL.String(), method: "GET", headers: make(http.Header), query: make(url.Values), timeout: defaultHTTPTimeout, maxBytes: defaultHTTPMaxBytes}
	if value, exists := record["method"]; exists {
		method, ok := value.(string)
		if !ok || !validHTTPToken(method) {
			return httpRequestOptions{}, invalidHTTPRequest("method must be HTTP token text")
		}
		request.method = strings.ToUpper(method)
	}
	if value, exists := record["headers"]; exists {
		headers, ok := value.(map[string]any)
		if !ok {
			return httpRequestOptions{}, invalidHTTPRequest("headers must be a record")
		}
		for name, raw := range headers {
			if !validHTTPHeaderName(name) {
				return httpRequestOptions{}, invalidHTTPRequest("invalid header name " + strconv.Quote(name))
			}
			values, err := httpStringValues(raw, "header "+name)
			if err != nil {
				return httpRequestOptions{}, err
			}
			for _, value := range values {
				if !validHTTPHeaderValue(value) {
					return httpRequestOptions{}, invalidHTTPRequest("header values cannot contain control characters")
				}
				request.headers.Add(name, value)
			}
		}
	}
	if value, exists := record["query"]; exists {
		query, ok := value.(map[string]any)
		if !ok {
			return httpRequestOptions{}, invalidHTTPRequest("query must be a record")
		}
		for name, raw := range query {
			values, err := httpStringValues(raw, "query "+name)
			if err != nil {
				return httpRequestOptions{}, err
			}
			for _, value := range values {
				request.query.Add(name, value)
			}
		}
	}
	textValue, hasText := record["text"]
	jsonValue, hasJSON := record["json"]
	if hasText && hasJSON {
		return httpRequestOptions{}, invalidHTTPRequest("request cannot have both text and JSON bodies")
	}
	if hasText {
		text, ok := textValue.(string)
		if !ok {
			return httpRequestOptions{}, invalidHTTPRequest("text body must be text")
		}
		request.body, request.contentType = []byte(text), "text/plain; charset=utf-8"
	}
	if hasJSON {
		encoded, err := json.Marshal(jsonValue)
		if err != nil {
			return httpRequestOptions{}, invalidHTTPRequest("JSON body cannot be encoded: " + err.Error())
		}
		request.body, request.contentType = encoded, "application/json"
	}
	if value, exists := record["timeout"]; exists {
		duration, ok := value.(time.Duration)
		if !ok || duration <= 0 {
			return httpRequestOptions{}, invalidHTTPRequest("timeout must be a positive duration")
		}
		request.timeout = duration
	}
	if value, exists := record["max_bytes"]; exists {
		number, ok := number(value)
		if !ok || math.IsNaN(number) || math.Trunc(number) != number || number < 0 || number > absoluteHTTPMaxBytes {
			return httpRequestOptions{}, invalidHTTPRequest(fmt.Sprintf("max_bytes must be an integer from 0 through %d", absoluteHTTPMaxBytes))
		}
		request.maxBytes = int64(number)
	}
	if value, exists := record["follow_redirects"]; exists {
		follow, ok := value.(bool)
		if !ok {
			return httpRequestOptions{}, invalidHTTPRequest("follow_redirects must be boolean")
		}
		request.followRedirects = follow
	}
	return request, nil
}

func performHTTPRequest(parent context.Context, opts Options, request httpRequestOptions) (map[string]any, error) {
	if opts.Config == nil || !opts.Config.ExternalNetwork {
		return nil, fmt.Errorf("std/http requires network capability enabled by project policy")
	}
	ctx, cancel := context.WithTimeout(parent, request.timeout)
	defer cancel()
	endpoint, _ := url.Parse(request.url)
	query := endpoint.Query()
	for key, values := range request.query {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	endpoint.RawQuery = query.Encode()
	httpRequest, err := http.NewRequestWithContext(ctx, request.method, endpoint.String(), bytes.NewReader(request.body))
	if err != nil {
		return nil, invalidHTTPRequest(err.Error())
	}
	httpRequest.Header = request.headers.Clone()
	if request.contentType != "" && httpRequest.Header.Get("Content-Type") == "" {
		httpRequest.Header.Set("Content-Type", request.contentType)
	}
	base := opts.HTTPClient
	if base == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		base = &http.Client{Transport: transport}
	}
	client := *base
	client.Timeout = 0 // The context owns the complete deadline.
	client.CheckRedirect = func(next *http.Request, previous []*http.Request) error {
		if !request.followRedirects {
			return http.ErrUseLastResponse
		}
		if len(previous) >= maxHTTPRedirects {
			return httpFailure("HttpRedirectRejected", "HTTP redirect limit exceeded", map[string]any{"reason": "more than 10 redirects"})
		}
		prior := previous[len(previous)-1].URL
		if prior.Scheme == "https" && next.URL.Scheme != "https" {
			return httpFailure("HttpRedirectRejected", "HTTPS redirect downgrade rejected", map[string]any{"reason": "HTTPS to HTTP downgrade"})
		}
		if !sameHTTPOrigin(prior, next.URL) {
			// Caller-supplied extension headers may also contain credentials.
			// Do not guess from header names across a trust boundary.
			next.Header = make(http.Header)
		}
		return nil
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return nil, classifyHTTPError(err, endpoint, request.timeout)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, request.maxBytes+1))
	if err != nil {
		return nil, classifyHTTPError(err, endpoint, request.timeout)
	}
	if int64(len(body)) > request.maxBytes {
		return nil, httpFailure("HttpBodyTooLarge", fmt.Sprintf("HTTP response exceeds %d byte limit", request.maxBytes), map[string]any{"limit": float64(request.maxBytes), "observed": float64(len(body))})
	}
	if !utf8.Valid(body) {
		return nil, httpFailure("InvalidHttpResponse", "HTTP response body is not valid UTF-8", map[string]any{"reason": "response body is not valid UTF-8"})
	}
	headers := make(map[string]any, len(response.Header))
	for name, values := range response.Header {
		items := make([]any, len(values))
		for index, value := range values {
			items[index] = value
		}
		headers[strings.ToLower(name)] = items
	}
	return map[string]any{"status": float64(response.StatusCode), "headers": headers, "body": string(body), "final_url": response.Request.URL.String()}, nil
}

func requireHTTPSuccess(response map[string]any) error {
	status := int(response["status"].(float64))
	if status >= 200 && status <= 299 {
		return nil
	}
	preview := response["body"].(string)
	if len(preview) > 1024 {
		preview = preview[:1024]
		for len(preview) > 0 && !utf8.ValidString(preview) {
			preview = preview[:len(preview)-1]
		}
	}
	return httpFailure("UnexpectedHttpStatus", fmt.Sprintf("HTTP request returned status %d", status), map[string]any{"status": float64(status), "body_preview": preview})
}

func classifyHTTPError(err error, endpoint *url.URL, timeout time.Duration) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return httpFailure("HttpTimeout", "HTTP request timed out", map[string]any{"phase": "request", "duration": timeout})
	}
	var typed *typedFailure
	if errors.As(err, &typed) {
		return typed
	}
	var certificate *tls.CertificateVerificationError
	var authority *x509.UnknownAuthorityError
	if errors.As(err, &certificate) || errors.As(err, &authority) {
		return httpFailure("HttpTlsFailure", "HTTP TLS verification failed", map[string]any{"origin": httpOrigin(endpoint), "reason": err.Error()})
	}
	var network net.Error
	retryable := errors.As(err, &network) && (network.Timeout() || network.Temporary())
	return httpFailure("HttpConnectionFailed", "HTTP connection failed", map[string]any{"origin": httpOrigin(endpoint), "reason": err.Error(), "retryable": retryable})
}

func invalidHTTPRequest(reason string) error {
	return httpFailure("InvalidHttpRequest", "invalid HTTP request: "+reason, map[string]any{"reason": reason})
}

func httpFailure(kind, message string, fields map[string]any) error {
	value := map[string]any{"kind": kind, "message": message, "retryable": false}
	for key, field := range fields {
		value[key] = field
	}
	return &typedFailure{kind: kind, value: value}
}

func httpStringValues(value any, label string) ([]string, error) {
	switch typed := value.(type) {
	case string:
		return []string{typed}, nil
	case []any:
		values := make([]string, len(typed))
		for index, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, invalidHTTPRequest(label + " values must be text")
			}
			values[index] = text
		}
		return values, nil
	default:
		return nil, invalidHTTPRequest(label + " must be text or a list of text")
	}
}

func validHTTPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		if !strings.ContainsRune("!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", character) {
			return false
		}
	}
	return true
}

func validHTTPToken(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", character) {
			return false
		}
	}
	return true
}

func validHTTPHeaderValue(value string) bool {
	for _, character := range value {
		if (character < 0x20 && character != '\t') || character == 0x7f {
			return false
		}
	}
	return true
}

func sameHTTPOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Hostname(), right.Hostname()) && effectiveHTTPPort(left) == effectiveHTTPPort(right)
}

func effectiveHTTPPort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if value.Scheme == "https" {
		return "443"
	}
	return "80"
}

func httpOrigin(value *url.URL) string {
	return value.Scheme + "://" + value.Host
}

func firstHTTPHeader(headers map[string]any, name string) string {
	values, _ := headers[strings.ToLower(name)].([]any)
	if len(values) == 0 {
		return ""
	}
	text, _ := values[0].(string)
	return text
}
