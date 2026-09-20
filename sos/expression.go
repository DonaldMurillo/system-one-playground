package sos

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type token struct {
	text   string
	quoted bool
}

func lex(s string) ([]token, error) {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		if c == ' ' || c == '\n' || c == '\r' {
			i++
			continue
		}
		if c == '"' {
			j := i + 1
			for j < len(s) {
				if s[j] == '\\' {
					j += 2
					continue
				}
				if s[j] == '"' {
					break
				}
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("unterminated string")
			}
			var v string
			if err := json.Unmarshal([]byte(s[i:j+1]), &v); err != nil {
				return nil, err
			}
			out = append(out, token{v, true})
			i = j + 1
			continue
		}
		if strings.ContainsRune("()[]{},:+-*/%<>=!", rune(c)) {
			j := i + 1
			if j < len(s) && s[j] == '=' {
				j++
			}
			out = append(out, token{s[i:j], false})
			i = j
			continue
		}
		j := i
		for j < len(s) && !strings.ContainsRune(" \n\r()[]{},:+-*/%<>=!", rune(s[j])) {
			j++
		}
		if j == i {
			return nil, fmt.Errorf("unexpected character %q", c)
		}
		out = append(out, token{s[i:j], false})
		i = j
	}
	return out, nil
}

type exprParser struct {
	t    []token
	i    int
	env  map[string]any
	item any
}

func evaluate(s string, env map[string]any, item any) (any, error) {
	t, e := lex(strings.TrimSpace(s))
	if e != nil {
		return nil, e
	}
	p := &exprParser{t: t, env: env, item: item}
	v, e := p.expr(0)
	if e == nil && p.i < len(t) {
		e = fmt.Errorf("unexpected %q in expression", t[p.i].text)
	}
	if e == nil {
		e = validateValue(v)
	}
	return v, e
}
func (p *exprParser) eat(s string) bool {
	if p.i < len(p.t) && !p.t[p.i].quoted && p.t[p.i].text == s {
		p.i++
		return true
	}
	return false
}
func (p *exprParser) want(s string) error {
	if !p.eat(s) {
		return fmt.Errorf("expected %q", s)
	}
	return nil
}
func (p *exprParser) expr(min int) (any, error) {
	left, e := p.atom()
	if e != nil {
		return nil, e
	}
	for p.i < len(p.t) {
		op := p.t[p.i].text
		if p.t[p.i].quoted {
			break
		}
		prec := precedence(op)
		if prec < min || prec == 0 {
			break
		}
		p.i++
		if op == "is" {
			op = "=="
			if p.eat("not") {
				op = "!="
			}
		}
		if b, ok := left.(bool); ok && ((op == "and" && !b) || (op == "or" && b)) {
			if e := p.skipRHS(prec + 1); e != nil {
				return nil, e
			}
			continue
		}
		right, e := p.expr(prec + 1)
		if e != nil {
			return nil, e
		}
		left, e = binary(op, left, right)
		if e != nil {
			return nil, e
		}
	}
	return left, nil
}
func precedence(s string) int {
	switch s {
	case "or":
		return 1
	case "and":
		return 2
	case "is", "==", "!=", "<", ">", "<=", ">=", "contains":
		return 3
	case "+", "-", "plus", "minus":
		return 4
	case "*", "/", "%", "times":
		return 5
	}
	return 0
}
func (p *exprParser) atom() (any, error) {
	if p.i >= len(p.t) {
		return nil, fmt.Errorf("expected a value")
	}
	t := p.t[p.i]
	p.i++
	if t.quoted {
		return interpolate(t.text, p.env, p.item)
	}
	switch t.text {
	case "(":
		v, e := p.expr(0)
		if e != nil {
			return nil, e
		}
		return v, p.want(")")
	case "[":
		a := []any{}
		if p.eat("]") {
			return a, nil
		}
		for {
			v, e := p.expr(0)
			if e != nil {
				return nil, e
			}
			a = append(a, v)
			if p.eat("]") {
				break
			}
			if e = p.want(","); e != nil {
				return nil, e
			}
		}
		return a, nil
	case "{":
		m := map[string]any{}
		if p.eat("}") {
			return m, nil
		}
		for {
			if p.i >= len(p.t) {
				return nil, fmt.Errorf("expected field")
			}
			key := p.t[p.i].text
			p.i++
			if e := p.want(":"); e != nil {
				return nil, e
			}
			v, e := p.expr(0)
			if e != nil {
				return nil, e
			}
			m[key] = v
			if p.eat("}") {
				break
			}
			if e = p.want(","); e != nil {
				return nil, e
			}
		}
		return m, nil
	case "true", "on":
		return true, nil
	case "false", "off":
		return false, nil
	case "null":
		return nil, nil
	case "now":
		return time.Now().UTC(), nil
	case "empty":
		if p.eat("list") {
			return []any{}, nil
		}
		if p.eat("record") {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("expected list or record after empty")
	case "not":
		v, e := p.atom()
		if e != nil {
			return nil, e
		}
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("not requires boolean")
		}
		return !b, nil
	case "-":
		v, e := p.atom()
		if e != nil {
			return nil, e
		}
		n, ok := number(v)
		if !ok {
			return nil, fmt.Errorf("negation requires number")
		}
		return -n, nil
	case "count", "length":
		p.eat("of")
		v, e := p.atom()
		if e != nil {
			return nil, e
		}
		switch a := v.(type) {
		case []any:
			return float64(len(a)), nil
		case string:
			return float64(len([]rune(a))), nil
		case map[string]any:
			return float64(len(a)), nil
		}
		return nil, fmt.Errorf("count requires text, list or record")
	case "words":
		p.eat("of")
		v, e := p.atom()
		if e != nil {
			return nil, e
		}
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("words requires text")
		}
		a := []any{}
		for _, w := range strings.Fields(s) {
			a = append(a, w)
		}
		return a, nil
	case "first", "last":
		n, e := p.atom()
		if e != nil {
			return nil, e
		}
		p.eat("items")
		p.eat("of")
		v, e := p.atom()
		if e != nil {
			return nil, e
		}
		return sliceItems(v, n, t.text == "last")
	}
	if n, e := strconv.ParseFloat(t.text, 64); e == nil {
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, fmt.Errorf("number must be finite")
		}
		return n, nil
	}
	if d, e := time.ParseDuration(t.text); e == nil {
		return d, nil
	}
	// Field-of-object reads naturally alongside dotted access.
	if p.eat("of") {
		v, e := p.atom()
		if e != nil {
			return nil, e
		}
		return property(v, t.text)
	}
	parts := strings.Split(t.text, ".")
	var v any
	var ok bool
	if v, ok = p.env[parts[0]]; !ok {
		if m, yes := p.item.(map[string]any); yes {
			v, ok = m[parts[0]]
		}
		if parts[0] == "item" {
			v, ok = p.item, true
		}
	}
	if !ok {
		return nil, fmt.Errorf("unknown name %q", parts[0])
	}
	for _, part := range parts[1:] {
		var e error
		v, e = property(v, part)
		if e != nil {
			return nil, e
		}
	}
	return v, nil
}
func property(v any, key string) (any, error) {
	if m, ok := v.(map[string]any); ok {
		if x, ok := m[key]; ok {
			return x, nil
		}
	}
	return nil, fmt.Errorf("field %q is missing", key)
}
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		x, e := n.Float64()
		return x, e == nil
	}
	return 0, false
}
func binary(op string, a, b any) (any, error) {
	if op == "plus" {
		op = "+"
	}
	if op == "minus" {
		op = "-"
	}
	if op == "times" {
		op = "*"
	}
	if op == "and" || op == "or" {
		x, xok := a.(bool)
		y, yok := b.(bool)
		if !xok || !yok {
			return nil, fmt.Errorf("%s requires booleans", op)
		}
		if op == "and" {
			return x && y, nil
		}
		return x || y, nil
	}
	if op == "==" || op == "!=" {
		eq := reflect.DeepEqual(a, b)
		if x, ok := number(a); ok {
			if y, yes := number(b); yes {
				eq = x == y
			}
		}
		if op == "!=" {
			eq = !eq
		}
		return eq, nil
	}
	if op == "contains" {
		switch x := a.(type) {
		case string:
			y, ok := b.(string)
			if ok {
				return strings.Contains(x, y), nil
			}
		case []any:
			for _, v := range x {
				if reflect.DeepEqual(v, b) {
					return true, nil
				}
			}
			return false, nil
		}
		return nil, fmt.Errorf("contains requires matching text or a list")
	}
	if t, ok := a.(time.Time); ok {
		if d, ok := b.(time.Duration); ok {
			if op == "-" {
				return t.Add(-d), nil
			}
			if op == "+" {
				return t.Add(d), nil
			}
		}
	}
	// Timestamp strings from JSON compare against captured timestamps.
	if t, ok := b.(time.Time); ok {
		if s, ok := a.(string); ok {
			x, e := time.Parse(time.RFC3339Nano, s)
			if e != nil {
				return nil, fmt.Errorf("invalid timestamp %q", s)
			}
			a = x
		}
		if x, ok := a.(time.Time); ok {
			return order(op, float64(x.UnixMilli()), float64(t.UnixMilli()))
		}
	}
	if x, ok := a.(string); ok {
		if y, yes := b.(string); yes {
			if op == "+" {
				if len(x)+len(y) > 16<<20 {
					return nil, fmt.Errorf("text exceeds 16 MiB limit")
				}
				return x + y, nil
			}
			cmp := strings.Compare(x, y)
			return order(op, float64(cmp), 0)
		}
	}
	x, xok := number(a)
	y, yok := number(b)
	if !xok || !yok {
		return nil, fmt.Errorf("%s requires compatible values", op)
	}
	var n float64
	switch op {
	case "+":
		n = x + y
	case "-":
		n = x - y
	case "*":
		n = x * y
	case "/":
		if y == 0 {
			return nil, fmt.Errorf("division by zero")
		}
		n = x / y
	case "%":
		if y == 0 {
			return nil, fmt.Errorf("division by zero")
		}
		n = math.Mod(x, y)
	default:
		return order(op, x, y)
	}
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return nil, fmt.Errorf("numeric overflow")
	}
	return n, nil
}
func order(op string, x, y float64) (any, error) {
	switch op {
	case "<":
		return x < y, nil
	case ">":
		return x > y, nil
	case "<=":
		return x <= y, nil
	case ">=":
		return x >= y, nil
	}
	return nil, fmt.Errorf("unsupported operator %q", op)
}
func sliceItems(v, n any, last bool) (any, error) {
	a, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("expected list")
	}
	f, ok := number(n)
	if !ok || f < 0 || f != math.Trunc(f) {
		return nil, fmt.Errorf("item count must be a nonnegative integer")
	}
	if f > float64(len(a)) {
		f = float64(len(a))
	}
	i := int(f)
	if last {
		return append([]any{}, a[len(a)-i:]...), nil
	}
	return append([]any{}, a[:i]...), nil
}
func interpolate(s string, env map[string]any, item any) (string, error) {
	var b strings.Builder
	for {
		i := strings.Index(s, "{")
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		j := strings.Index(s[i:], "}")
		if j < 0 {
			return "", fmt.Errorf("unclosed interpolation")
		}
		v, e := evaluate(s[i+1:i+j], env, item)
		if e != nil {
			return "", e
		}
		b.WriteString(display(v))
		s = s[i+j+1:]
	}
	return b.String(), nil
}
func display(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case time.Time:
		return x.Format(time.RFC3339Nano)
	case time.Duration:
		return x.String()
	case nil:
		return "null"
	}
	b, e := json.Marshal(v)
	if e != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
func sortedKeys(m map[string]any) []string {
	a := make([]string, 0, len(m))
	for k := range m {
		a = append(a, k)
	}
	sort.Strings(a)
	return a
}
func validName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if !unicode.IsLetter(c) && c != '_' && (i == 0 || !unicode.IsDigit(c)) {
			return false
		}
	}
	return true
}

// skipRHS consumes the unused side of a short-circuit boolean expression.
func (p *exprParser) skipRHS(min int) error {
	start := p.i
	depth := 0
	for p.i < len(p.t) {
		t := p.t[p.i]
		if !t.quoted {
			switch t.text {
			case "(", "[", "{":
				depth++
			case ")", "]", "}":
				if depth == 0 {
					return nil
				}
				depth--
			case ",":
				if depth == 0 {
					return nil
				}
			}
			if depth == 0 && precedence(t.text) > 0 && precedence(t.text) < min {
				break
			}
		}
		p.i++
	}
	if p.i == start {
		return fmt.Errorf("expected boolean expression")
	}
	return nil
}

func validateValue(value any) error {
	units, bytes := 0, 0
	var walk func(any, int) error
	walk = func(v any, depth int) error {
		units++
		if depth > 64 || units > 100000 {
			return fmt.Errorf("value exceeds nesting or item limit")
		}
		switch x := v.(type) {
		case string:
			bytes += len(x)
		case []any:
			for _, v := range x {
				if e := walk(v, depth+1); e != nil {
					return e
				}
			}
		case map[string]any:
			for k, v := range x {
				bytes += len(k)
				if e := walk(v, depth+1); e != nil {
					return e
				}
			}
		}
		if bytes > 16<<20 {
			return fmt.Errorf("value exceeds 16 MiB limit")
		}
		return nil
	}
	return walk(value, 0)
}
