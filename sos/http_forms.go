package sos

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Canonical English HTTP forms and their deterministic lowering to the
// std/http operations. Every canonical statement resolves to exactly one
// registered operation; the technical call syntax stays available for
// tooling, adapters, and advanced use.

var (
	httpDurationRE = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*(milliseconds?|ms|seconds?|s|minutes?|m|hours?|h)$`)
	httpSizeRE     = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*(KiB|MiB|GiB|B)?$`)
)

var httpRespondOptionKeyRE = regexp.MustCompile(`^(status|headers|text body|JSON body) from (.+)$`)

func httpRespondOptionParts(text string) (key, value string, ok bool) {
	if m := httpRespondOptionKeyRE.FindStringSubmatch(text); m != nil {
		return m[1], strings.TrimSpace(m[2]), true
	}
	return "", "", false
}

var httpRequestOptionKeyRE = regexp.MustCompile(`^(method|headers|query|text body|JSON body|timeout|following redirects) from (.+)$`)
var httpRequestAcceptRE = regexp.MustCompile(`^accepting at most (.+)$`)
var httpListenOptionKeyRE = regexp.MustCompile(`^(request deadline|shutdown deadline) (.+)$`)
var httpListenBodyRE = regexp.MustCompile(`^allow bodies up to (.+)$`)
var httpListenHeadersRE = regexp.MustCompile(`^allow headers up to (.+)$`)

// httpRequestOptionParts recognizes one request option line, returning its
// canonical option key and raw value text.
func httpRequestOptionParts(text string) (key, value string, ok bool) {
	if m := httpRequestOptionKeyRE.FindStringSubmatch(text); m != nil {
		return m[1], strings.TrimSpace(m[2]), true
	}
	if m := httpRequestAcceptRE.FindStringSubmatch(text); m != nil {
		return "accepting at most", strings.TrimSpace(m[1]), true
	}
	return "", "", false
}

// httpListenOptionParts recognizes one listener option line.
func httpListenOptionParts(text string) (key, value string, ok bool) {
	if m := httpListenOptionKeyRE.FindStringSubmatch(text); m != nil {
		return m[1], strings.TrimSpace(m[2]), true
	}
	if m := httpListenBodyRE.FindStringSubmatch(text); m != nil {
		return "allow bodies up to", strings.TrimSpace(m[1]), true
	}
	if m := httpListenHeadersRE.FindStringSubmatch(text); m != nil {
		return "allow headers up to", strings.TrimSpace(m[1]), true
	}
	return "", "", false
}

// httpDurationLiteral normalizes English duration words ("30 seconds") to the
// core duration literal form ("30s"). The boolean reports whether the text was
// a recognized literal rather than an arbitrary expression.
func httpDurationLiteral(text string) (string, bool) {
	m := httpDurationRE.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return text, false
	}
	switch strings.ToLower(m[2]) {
	case "millisecond", "milliseconds", "ms":
		return m[1] + "ms", true
	case "second", "seconds", "s":
		return m[1] + "s", true
	case "minute", "minutes", "m":
		return m[1] + "m", true
	case "hour", "hours", "h":
		return m[1] + "h", true
	}
	return text, false
}

// httpSizeLiteral translates a byte-size literal ("2 MiB") to its integer
// byte count. Bare integers count bytes.
func httpSizeLiteral(text string) (int64, bool) {
	m := httpSizeRE.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return 0, false
	}
	amount, err := strconv.ParseFloat(m[1], 64)
	if err != nil || amount < 0 {
		return 0, false
	}
	multiplier := float64(1)
	switch m[2] {
	case "KiB":
		multiplier = 1024
	case "MiB":
		multiplier = 1024 * 1024
	case "GiB":
		multiplier = 1024 * 1024 * 1024
	}
	bytes := int64(amount * multiplier)
	if float64(bytes) != amount*multiplier {
		return 0, false
	}
	return bytes, true
}

// validateHTTPStatements checks the statically decidable canonical HTTP
// surface: option vocabulary, duplicates, mutually exclusive bodies,
// literal bounds, and forms std/http cannot honor yet.
func validateHTTPStatements(statements []*Statement) []Diagnostic {
	var ds []Diagnostic
	var walk func([]*Statement)
	walk = func(sts []*Statement) {
		for _, s := range sts {
			m := match(s.Kind, s.Text)
			switch s.Kind {
			case "httpGet":
				if m[3] != "" && (m[3] != "200" || m[4] != "299") {
					ds = append(ds, Diagnostic{s.Line, 1, "convenience forms require status 200 through 299; use send an HTTP request for any other range"})
				}
			case "httpRequest":
				ds = append(ds, validateHTTPRequestOptions(s)...)
			case "httpListen":
				port, _ := strconv.Atoi(m[2])
				if port < 1 || port > 65535 {
					ds = append(ds, Diagnostic{s.Line, 1, "listener port must be an integer from 1 through 65535"})
				}
				ds = append(ds, validateHTTPListenOptions(s)...)
			case "httpRespondComplete":
				ds = append(ds, validateHTTPRespondOptions(s)...)
			case "httpRespond":
				status, _ := strconv.Atoi(m[2])
				if status < 100 || status > 599 {
					ds = append(ds, Diagnostic{s.Line, 1, "response status must be an integer from 100 through 599"})
				}
			case "httpReadBody":
				if m[1] == "form" {
					ds = append(ds, Diagnostic{s.Line, 1, "form bodies are not supported by std/http yet; read a text or JSON body instead"})
				}
			}
			walk(s.Body)
		}
	}
	walk(statements)
	return ds
}

func validateHTTPRequestOptions(s *Statement) []Diagnostic {
	var ds []Diagnostic
	seen := map[string]bool{}
	hasText, hasJSON := false, false
	for _, field := range s.Body {
		if field.Kind != "field" {
			continue
		}
		key, value, ok := httpRequestOptionParts(field.Text)
		if !ok {
			continue
		}
		if seen[key] {
			ds = append(ds, Diagnostic{field.Line, 1, "request option " + key + " appears more than once"})
			continue
		}
		seen[key] = true
		switch key {
		case "text body":
			hasText = true
		case "JSON body":
			hasJSON = true
		case "timeout":
			if literal, isLiteral := httpDurationLiteral(value); isLiteral {
				if duration, err := time.ParseDuration(literal); err != nil || duration <= 0 {
					ds = append(ds, Diagnostic{field.Line, 1, "timeout must be a positive duration"})
				}
			}
		case "accepting at most":
			bytes, valid := httpSizeLiteral(value)
			if !valid {
				ds = append(ds, Diagnostic{field.Line, 1, "accepting at most requires a byte size like 64 KiB or 2 MiB"})
			} else if bytes > absoluteHTTPMaxBytes {
				ds = append(ds, Diagnostic{field.Line, 1, fmt.Sprintf("accepting at most exceeds the %d byte limit", absoluteHTTPMaxBytes)})
			}
		}
	}
	if hasText && hasJSON {
		ds = append(ds, Diagnostic{s.Line, 1, "a request cannot carry both a text and a JSON body"})
	}
	return ds
}

func validateHTTPListenOptions(s *Statement) []Diagnostic {
	var ds []Diagnostic
	seen := map[string]bool{}
	for _, field := range s.Body {
		if field.Kind != "field" {
			continue
		}
		key, value, ok := httpListenOptionParts(field.Text)
		if !ok {
			continue
		}
		if seen[key] {
			ds = append(ds, Diagnostic{field.Line, 1, "listener option " + key + " appears more than once"})
			continue
		}
		seen[key] = true
		switch key {
		case "allow headers up to":
			bytes, valid := httpSizeLiteral(value)
			if !valid {
				ds = append(ds, Diagnostic{field.Line, 1, "allow headers up to requires a byte size like 32 KiB"})
			} else if bytes < 1 || bytes > absoluteHTTPMaxHeaderBytes {
				ds = append(ds, Diagnostic{field.Line, 1, fmt.Sprintf("allow headers up to must be from 1 through %d bytes", absoluteHTTPMaxHeaderBytes)})
			}
		case "allow bodies up to":
			bytes, valid := httpSizeLiteral(value)
			if !valid {
				ds = append(ds, Diagnostic{field.Line, 1, "allow bodies up to requires a byte size like 1 MiB"})
			} else if bytes > absoluteHTTPMaxBytes {
				ds = append(ds, Diagnostic{field.Line, 1, fmt.Sprintf("allow bodies up to exceeds the %d byte limit", absoluteHTTPMaxBytes)})
			}
		case "request deadline", "shutdown deadline":
			literal, isLiteral := httpDurationLiteral(value)
			if !isLiteral {
				ds = append(ds, Diagnostic{field.Line, 1, key + " requires a duration literal like 30 seconds"})
			} else if duration, err := time.ParseDuration(literal); err != nil || duration <= 0 {
				ds = append(ds, Diagnostic{field.Line, 1, key + " must be a positive duration"})
			}
		}
	}
	return ds
}

// httpCanonicalBinding returns the name a canonical statement binds.
func httpCanonicalBinding(s *Statement, m []string) string {
	switch s.Kind {
	case "httpGet":
		return m[5]
	case "httpPost", "httpListen":
		return m[3]
	case "httpRequest":
		return m[2]
	case "httpReadBody":
		return m[4]
	}
	return ""
}

// httpCanonicalExprs lists the argument expression texts a canonical call
// form contributes, with duration and size words already normalized to core
// literals so expression checking sees exactly what the runtime evaluates.
func httpCanonicalExprs(s *Statement, m []string) []string {
	switch s.Kind {
	case "httpGet":
		return []string{m[2]}
	case "httpPost":
		return []string{m[2], m[1]}
	case "httpRequest":
		exprs := []string{m[1]}
		for _, field := range s.Body {
			if field.Kind != "field" {
				continue
			}
			key, value, ok := httpRequestOptionParts(field.Text)
			if !ok {
				continue
			}
			if key == "accepting at most" {
				if bytes, valid := httpSizeLiteral(value); valid {
					exprs = append(exprs, strconv.FormatInt(bytes, 10))
				}
				continue
			}
			if key == "timeout" {
				if literal, isLiteral := httpDurationLiteral(value); isLiteral {
					exprs = append(exprs, literal)
					continue
				}
			}
			exprs = append(exprs, value)
		}
		return exprs
	case "httpReadBody":
		return []string{m[2]}
	case "httpRespond":
		if m[4] != "" {
			return []string{m[4]}
		}
	case "httpRespondComplete":
		exprs := []string{}
		for _, field := range s.Body {
			if field.Kind != "field" {
				continue
			}
			if _, value, ok := httpRespondOptionParts(field.Text); ok {
				exprs = append(exprs, value)
			}
		}
		return exprs
	}
	return nil
}

// httpFormAction maps a canonical statement to its std/http operation name.
func httpFormAction(s *Statement) (string, bool) {
	m := match(s.Kind, s.Text)
	switch s.Kind {
	case "httpGet":
		if m[1] == "text" {
			return "get_text", true
		}
		return "get_json", true
	case "httpPost":
		return "post_json", true
	case "httpRequest":
		return "request", true
	case "httpListen":
		return "listen", true
	case "httpReadBody":
		if m[1] == "text" {
			return "read_text_body", true
		}
		return "read_json_body", true
	case "httpRespond":
		return "respond_status", true
	case "httpRespondComplete":
		return "respond", true
	}
	return "", false
}

// httpFormPossibleFailures reports the std/http failure set a canonical
// statement can raise. The second result is false for non-HTTP statements.
func httpFormPossibleFailures(s *Statement) ([]string, bool) {
	action, ok := httpFormAction(s)
	if !ok {
		return nil, false
	}
	operations := stdRegistry["std/http"]
	if operations == nil {
		return nil, false
	}
	operation, exists := operations[action]
	if !exists {
		return nil, false
	}
	return append([]string(nil), operation.PossibleFailures...), true
}

// httpRequestRecordText synthesizes the std/http.request options record as
// one record-literal expression text.
func httpRequestRecordText(s *Statement, m []string) (string, error) {
	parts := []string{`"url": ` + strings.TrimSpace(m[1])}
	seen := map[string]bool{}
	for _, field := range s.Body {
		if field.Kind != "field" {
			continue
		}
		key, value, ok := httpRequestOptionParts(field.Text)
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		option, expression := httpRequestOptionEntry(key, value)
		parts = append(parts, `"`+option+`": `+expression)
	}
	return "{" + strings.Join(parts, ", ") + "}", nil
}

func httpRequestOptionEntry(key, value string) (option, expression string) {
	switch key {
	case "method":
		return "method", value
	case "headers":
		return "headers", value
	case "query":
		return "query", value
	case "text body":
		return "text", value
	case "JSON body":
		return "json", value
	case "timeout":
		if literal, isLiteral := httpDurationLiteral(value); isLiteral {
			return "timeout", literal
		}
		return "timeout", value
	case "following redirects":
		return "follow_redirects", value
	case "accepting at most":
		if bytes, valid := httpSizeLiteral(value); valid {
			return "max_bytes", strconv.FormatInt(bytes, 10)
		}
	}
	return "", ""
}

// httpListenRecordText synthesizes the std/http.listen options record.
func httpListenRecordText(s *Statement, m []string) (string, error) {
	host := "127.0.0.1"
	if m[1] == "all interfaces" {
		host = "0.0.0.0"
	}
	parts := []string{fmt.Sprintf(`"address": %q`, host+":"+m[2])}
	seen := map[string]bool{}
	for _, field := range s.Body {
		if field.Kind != "field" {
			continue
		}
		key, value, ok := httpListenOptionParts(field.Text)
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		var option, expression string
		switch key {
		case "allow headers up to":
			if bytes, valid := httpSizeLiteral(value); valid {
				option, expression = "max_header_bytes", strconv.FormatInt(bytes, 10)
			}
		case "allow bodies up to":
			if bytes, valid := httpSizeLiteral(value); valid {
				option, expression = "max_body_bytes", strconv.FormatInt(bytes, 10)
			}
		case "request deadline":
			if literal, isLiteral := httpDurationLiteral(value); isLiteral {
				option, expression = "request_deadline", literal
			}
		case "shutdown deadline":
			if literal, isLiteral := httpDurationLiteral(value); isLiteral {
				option, expression = "shutdown_deadline", literal
			}
		}
		if option != "" {
			parts = append(parts, `"`+option+`": `+expression)
		}
	}
	return "{" + strings.Join(parts, ", ") + "}", nil
}

// httpRespondRecordText synthesizes the std/http.respond options record.
func httpRespondRecordText(s *Statement) (string, error) {
	parts := []string{}
	seen := map[string]bool{}
	for _, field := range s.Body {
		if field.Kind != "field" {
			continue
		}
		key, value, ok := httpRespondOptionParts(field.Text)
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		var option string
		switch key {
		case "status":
			option = "status"
		case "headers":
			option = "headers"
		case "text body":
			option = "text"
		case "JSON body":
			option = "json"
		}
		if option != "" {
			parts = append(parts, `"`+option+`": `+value)
		}
	}
	return "{" + strings.Join(parts, ", ") + "}", nil
}

// validateHTTPRespondOptions checks the complete respond form: exactly one
// status and at most one body kind.
func validateHTTPRespondOptions(s *Statement) []Diagnostic {
	var ds []Diagnostic
	seen := map[string]bool{}
	hasText, hasJSON := false, false
	for _, field := range s.Body {
		if field.Kind != "field" {
			continue
		}
		key, _, ok := httpRespondOptionParts(field.Text)
		if !ok {
			continue
		}
		if seen[key] {
			ds = append(ds, Diagnostic{field.Line, 1, "response option " + key + " appears more than once"})
			continue
		}
		seen[key] = true
		switch key {
		case "text body":
			hasText = true
		case "JSON body":
			hasJSON = true
		}
	}
	if !seen["status"] {
		ds = append(ds, Diagnostic{s.Line, 1, "respond to request with requires status from"})
	}
	if hasText && hasJSON {
		ds = append(ds, Diagnostic{s.Line, 1, "a response cannot carry both a text and a JSON body"})
	}
	return ds
}

// runHTTPCall executes the canonical client and body-reading forms through
// the same module-operation path a technical call uses.
func (r *runtime) runHTTPCall(s *Statement, m []string) error {
	action, ok := httpFormAction(s)
	if !ok {
		return fmt.Errorf("unknown canonical HTTP operation")
	}
	called := httpCanonicalBinding(s, m)
	args := []string{}
	switch s.Kind {
	case "httpGet":
		args = []string{m[2]}
	case "httpPost":
		args = []string{m[2], m[1]}
	case "httpRequest":
		record, err := httpRequestRecordText(s, m)
		if err != nil {
			return err
		}
		args = []string{record}
	case "httpReadBody":
		args = []string{m[2]}
	}
	mod, _ := stdModule("std/http")
	if e := r.callModule(s, mod, action, "http."+action, args, called); e != nil {
		return e
	}
	if s.Kind == "httpGet" && m[1] == "text" && called != "" {
		r.types[called] = TypeRef{Name: "text"}
	}
	if s.Kind == "httpReadBody" && m[1] == "JSON" && m[3] != "" && called != "" {
		r.types[called] = TypeRef{Name: m[3]}
		if !typeMatchesRef(r.env[called], TypeRef{Name: m[3]}, r.definitions) {
			delete(r.types, called)
			return httpFailure("InvalidHttpBody", "HTTP request body does not match type "+m[3], map[string]any{"reason": "decoded JSON does not match type " + m[3]})
		}
	}
	return nil
}

// runHTTPListen opens the canonical listener stream through std/http.listen
// with the same handle, registration, and event surface as a technical open.
func (r *runtime) runHTTPListen(s *Statement, m []string) error {
	if existing, ok := r.env[m[3]].(*streamHandle); ok {
		existing.mu.Lock()
		active := existing.state == streamActive
		existing.mu.Unlock()
		if active {
			return fmt.Errorf("stream %s is already active; consume or close it before reopening", m[3])
		}
	}
	record, err := httpListenRecordText(s, m)
	if err != nil {
		return err
	}
	value, e := r.eval(record, nil)
	if e != nil {
		return e
	}
	mod, _ := stdModule("std/http")
	operation := mod.Native["listen"]
	if e := operation.check([]any{value}); e != nil {
		return e
	}
	source, e := operation.StreamFn(r.ctx, r.opts, []any{value})
	if e != nil {
		return fmt.Errorf("http.listen: %w", e)
	}
	itemType, parseErr := parseType("any", true)
	if parseErr != nil {
		_ = source.cancel(context.Background())
		return parseErr
	}
	stream := newStreamHandleWithContext(r.ctx, itemType, "http.listen", source)
	stream.id = fmt.Sprintf("stream-%d", r.shared.streams.Add(1))
	stream.binding, stream.line, stream.emit = m[3], s.Line, r.opts.OnStreamEvent
	if r.opts.Streams != nil {
		r.opts.Streams.register(stream)
	}
	stream.publish("opened")
	r.env[m[3]] = stream
	return nil
}

// runHTTPRespond completes the request's response obligation. Every variant
// delegates to its std/http operation: respond_status, respond_text,
// respond_json, or the complete respond with an options record.
func (r *runtime) runHTTPRespond(s *Statement, m []string) error {
	if e := r.tick(); e != nil {
		return e
	}
	if s.Kind == "httpRespondComplete" {
		record, err := httpRespondRecordText(s)
		if err != nil {
			return err
		}
		return r.callModule(s, stdHTTPModule(), "respond", "http.respond", []string{m[1], record}, "")
	}
	if m[3] == "" {
		return r.callModule(s, stdHTTPModule(), "respond_status", "http.respond_status", []string{m[1], m[2]}, "")
	}
	action := "respond_text"
	if m[3] == "JSON" {
		action = "respond_json"
	}
	return r.callModule(s, stdHTTPModule(), action, "http."+action, []string{m[1], m[2], m[4]}, "")
}

// stdHTTPModule materializes the standard HTTP module once per call site; it
// is cheap after the registry is built.
func stdHTTPModule() *Module {
	mod, _ := stdModule("std/http")
	return mod
}
