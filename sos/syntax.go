package sos

import (
	"errors"
	"fmt"
	"github.com/DonaldMurillo/system-one-playground/sosconfig"
	"regexp"
	"strings"
)

var forms = []struct{ kind, pattern string }{
	{"command", `^command ([A-Za-z_]\w*):$`},
	{"parameter", `^(argument|option|switch) ([A-Za-z_]\w*)(?: as (text|number|integer|folder|file|duration|boolean))?(?: choices (.+?))?(?: default (.+))?$`},
	{"package", `^package ([A-Za-z_]\w*)$`},
	{"import", `^import "([^"\n]+)"(?: as ([A-Za-z_]\w*))?$`},
	{"export", `^export ([A-Za-z_]\w*)$`},
	{"define", `^define ([A-Z][A-Za-z0-9_]*)\s*:$`},
	{"failure", `^define failure ([A-Z][A-Za-z0-9_]*)\s*:$`},
	{"word", `^word ([A-Za-z_]\w*) of ([A-Za-z_]\w*)$`},
	{"describe", `^describe (".*")$`},
	{"schema", `^expect ([\w-]+) with:$`},
	{"remember", `^remember (.+) called ([A-Za-z_]\w*)$`},
	{"find", `^find files under (.+) matching (.+) called ([A-Za-z_]\w*)$`},
	{"readEach", `^read each (\w+) in (.+) as lines of json into (\w+)$`},
	{"readFile", `^read file (.+) as text called ([A-Za-z_]\w*)$`},
	{"read", `^read (.+) as (json|text|lines of json) called (\w+)$`},
	{"httpGet", `^get (text|JSON) from (.+?)(?: expecting status (\d+) through (\d+))? called ([A-Za-z_]\w*)$`},
	{"httpPost", `^post (.+?) as JSON to (.+?) called ([A-Za-z_]\w*)$`},
	{"httpRequest", `^send an HTTP request to (.+?) called ([A-Za-z_]\w*):$`},
	{"httpListen", `^listen for HTTP requests on (?:(all interfaces|loopback) )?port (\d+) called ([A-Za-z_]\w*):$`},
	{"httpReadBody", `^read (text|JSON|form) body from ([A-Za-z_]\w*)(?: as ([A-Z][A-Za-z0-9_]*))? called ([A-Za-z_]\w*)$`},
	{"httpRespondComplete", `^respond to ([A-Za-z_]\w*) with:$`},
	{"httpRespond", `^respond to ([A-Za-z_]\w*) with status (\d+)(?: and (text|JSON) (.+))?$`},
	{"require", `^require each (\w+) in (.+) matches ([\w-]+)$`},
	{"keep", `^keep (\w+) where (.+)$`},
	{"sort", `^sort (\w+) by (.+?)(?: (ascending|descending))?$`},
	{"group", `^group (\w+) by (.+) called (\w+)$`},
	{"folder", `^create folder (.+) if missing$`},
	{"writeFile", `^write (.+?)( atomically)? to file (.+)$`},
	{"appendFile", `^append (.+) to file (.+)$`},
	{"checkExists", `^check whether (entry|file|folder) (.+) exists called ([A-Za-z_]\w*)$`},
	{"inspectEntry", `^inspect (entry|file|folder) (.+) called ([A-Za-z_]\w*)$`},
	{"listEntries", `^list (entries|files|folders) in folder (.+) called ([A-Za-z_]\w*)$`},
	{"walkThrough", `^walk through folder (.+) at most (\d+) entries called ([A-Za-z_]\w*)$`},
	{"createFoldersThrough", `^create folders through (.+)$`},
	{"copyEntry", `^copy (file|folder) (.+?) to (.+)$`},
	{"moveEntry", `^move (file|folder) (.+?) to (.+)$`},
	{"removeFile", `^remove file (.+)$`},
	{"removeEmptyFolder", `^remove empty folder (.+)$`},
	{"removeFolder", `^remove folder (.+) including its contents$`},
	{"streamFiles", `^stream (files|entries|folders) under folder (.+) called ([A-Za-z_]\w*)$`},
	{"watchFolder", `^watch folder (.+?)( recursively)? called ([A-Za-z_]\w*)$`},
	{"make", `^(?:make|assign|set) (\w+) (.+)$`},
	{"openStream", `^stream ([A-Za-z_]\w*(?:\.[A-Za-z_]\w*)?)(?: with (.+?))? called ([A-Za-z_]\w*)$`},
	{"closeStream", `^close stream ([A-Za-z_]\w*)$`},
	{"collectStream", `^collect at most (.+) items from (.+) called ([A-Za-z_]\w*)$`},
	{"stopReading", `^stop reading$`},
	{"streamFor", `^for each ([A-Za-z_]\w*) from (.+):$`},
	{"quietStream", `^wait for (?:each (\w+) in )?([A-Za-z_]\w*) to be quiet for (.+?)(?: with at most (\d+) pending \w+)?(?: called ([A-Za-z_]\w*))?$`},
	{"limitStream", `^limit ([A-Za-z_]\w*)(?: for each (\w+))? to (.+?) each (millisecond|second|minute|hour|day)s?(?: keeping the (first|latest))?(?: with at most (\d+) \w+)?(?: called ([A-Za-z_]\w*))?$`},
	{"throttlePolicy", `^keeping the (first|latest)$`},
	{"rejectExcess", `^rejecting excess \w+ with status (\d+)$`},
	{"handleEachStream", `^handle each (\w+) from ([A-Za-z_]\w*)(?: with at most (\d+)(?: \w+)? at once)?:?$`},
	{"handleOneStream", `^handle each (\w+) from ([A-Za-z_]\w*)(?: one at a time)?:?$`},
	{"newestStream", `^handle only the newest (\w+)(?: for each (\w+))? from ([A-Za-z_]\w*):?$`},
	{"newestCancel", `^when a newer (\w+) arrives cancel (?:the previous work|remaining work)$`},
	{"effectAck", `^acknowledging completed effects are not reversed$`},
	{"cancelPolicy", `^canceling an older (\w+) with status (\d+):$`},
	{"streamPendingBound", `^with at most (\d+) pending \w+$`},
	{"streamBound", `^with at most (\d+)(?: \w+)? at once:?$`},
	{"streamKnownBound", `^and at most (\d+) known \w+:?$`},
	{"streamKeyBound", `^with at most (\d+) \w+$`},
	{"oneAtATime", `^one at a time:$`},
	{"conflateStream", `^handle ([A-Za-z_]\w*) one at a time:?$`},
	{"conflatePolicy", `^keeping only the latest waiting (\w+):$`},
	{"exhaustStream", `^handle one ([A-Za-z_][\w ]*?) at a time:?$`},
	{"exhaustPolicy", `^(ignoring|rejecting) new \w+(?: with status (\d+))? while busy:$`},
	{"batchStream", `^group ([A-Za-z_]\w*)(?: for each (\w+))? into batches of at most (\d+)(?: with at most (\d+) \w+)?(?: called ([A-Za-z_]\w*))?$`},
	{"batchWindow", `^or after (.+)$`},
	{"distinctStream", `^ignore consecutive duplicate ([A-Za-z_]\w*) called ([A-Za-z_]\w*)$`},
	{"distinctKeyStream", `^keep only changes in (\w+) from ([A-Za-z_]\w*) called ([A-Za-z_]\w*)$`},
	{"idleStream", `^require an? (?:item from ([A-Za-z_]\w*)|([A-Za-z_]\w*)) at least every (.+?)(?: from ([A-Za-z_]\w*))?(?: called ([A-Za-z_]\w*))?$`},
	{"takeForStream", `^listen to ([A-Za-z_]\w*) for at most (.+?)(?: then stop normally)?(?: called ([A-Za-z_]\w*))?$`},
	{"deadlineStream", `^require ([A-Za-z_]\w*) to finish within (.+?)(?: called ([A-Za-z_]\w*))?$`},
	{"filterStream", `^keep each (\w+) from ([A-Za-z_]\w*) called ([A-Za-z_]\w*):$`},
	{"keepItem", `^keep ([A-Za-z_]\w*)$`},
	{"projectStream", `^take a value from each (\w+) in ([A-Za-z_]\w*) called ([A-Za-z_]\w*):$`},
	{"useValue", `^use (.+)$`},
	{"calledName", `^called ([A-Za-z_]\w*)$`},
	{"thenStopNormally", `^then stop normally$`},
	{"map", `^map each (\w+) in (.+) with at most (.+) running called (\w+)( collecting failures)?:$`},
	{"for", `^for each (\w+) in (.+?)(?: numbered from (\d+))?:$`},
	{"while", `^while (.+):$`},
	{"repeat", `^repeat (.+) times:$`},
	{"when", `^when (.+):$`},
	{"otherwise", `^otherwise:$`},
	{"take", `^take (first|last) (.+) items from (.+) called (\w+)$`},
	{"classify", `^classify (.+) by (.+) called ([A-Za-z_]\w*):$`},
	{"evaluate", `^evaluate (.+) by jev using (.+) called ([A-Za-z_]\w*)$`},
	{"judge", `^judge (.+) by (.+) called ([A-Za-z_]\w*)$`},
	{"score", `^score (.+) by (.+) called ([A-Za-z_]\w*):$`},
	{"append", `^append (.+) to (\w+)$`},
	{"save", `^save (.+) as (json|text) (?:in (.+)|under (.+) named (.+))$`},
	{"send", `^send (.+)$`},
	{"show", `^(?:show|print|emit) (.+)$`},
	{"rethrow", `^rethrow$`},
	{"passFailure", `^pass failure on$`},
	{"recover", `^recover(?: with (.+))?$`},
	{"finish", `^finish(?: with (.+))?$`},
	{"fail", `^fail ([A-Z][A-Za-z0-9_]*) with (.+?)(?::)?$`},
	{"capture", `^capture ([A-Za-z_]\w*(?:\.[A-Za-z_]\w*)?)(?: with (.+?))? called ([A-Za-z_]\w*)$`},
	{"stop", `^stop(?: with (.+))?$`},
	{"to", `^to ([A-Za-z_]\w*)(?: with (.*?))?(?:(?: returning ((?:optional )?(?:list of )?(?:any|text|file|folder|timestamp|number|integer|boolean|duration|[A-Z][A-Za-z0-9_]*)))|(?: streaming (text|file|folder|timestamp|number|integer|boolean|duration|[A-Z][A-Za-z0-9_]*)))?(?: may fail with (.+?))?:$`},
	{"call", `^call ([A-Za-z_]\w*(?:\.[A-Za-z_]\w*)?)(?: with (.+?))?(?: called (\w+))?$`},
	{"return", `^return (.+)$`},
	{"handler", `^on (failure|success|uncertain|existing)(?::| (.+))$`},
	{"ask", `^ask (.+)$`},
	{"using", `^using (.+)$`},
	{"model", `^model (.+)$`},
	{"accept", `^accept probability at least (.+)$`},
}
var patterns = map[string]*regexp.Regexp{}

func init() {
	for _, f := range forms {
		patterns[f.kind] = regexp.MustCompile(f.pattern)
	}
}
func match(kind, text string) []string {
	re := patterns[kind]
	if re == nil {
		return nil
	}
	masked := maskQuoted(text)
	indices := re.FindStringSubmatchIndex(string(masked))
	if indices == nil {
		return nil
	}
	out := make([]string, len(indices)/2)
	for i := range out {
		if indices[2*i] >= 0 {
			out[i] = text[indices[2*i]:indices[2*i+1]]
		}
	}
	return out
}

func Keywords() []string {
	return append([]string{"wait for", "to be quiet for", "limit", "handle each", "handle only the newest", "handle one", "keeping the first", "keeping the latest", "keeping only the latest waiting", "ignoring new", "rejecting new", "rejecting excess", "canceling an older", "acknowledging completed effects are not reversed", "or after", "into batches of at most", "ignore consecutive duplicate", "keep only changes in", "keep each", "listen to", "then stop normally", "at least every", "to finish within", "at once", "one at a time", "while busy", "use", "read file", "write to file", "check whether", "inspect entry", "inspect file", "inspect folder", "list entries in folder", "list files in folder", "list folders in folder", "walk through folder", "stream files under folder", "watch folder", "copy file", "copy folder", "move file", "move folder", "remove file", "remove empty folder", "remove folder", "create folders through", "atomically", "recursively", "folders deep", "following symbolic links", "without following symbolic links", "including files and folders", "including its contents", "only if it does not exist", "only if the destination does not exist", "replacing an existing file", "matching", "excluding"}, originalKeywords()...)
}

func originalKeywords() []string {
	return []string{"stream", "streaming", "from", "send", "close stream", "stop reading", "collect at most", "finish", "fail", "recover", "pass failure on", "capture", "rethrow", "map", "evaluate", "describe", "choices", "read", "keep", "sort", "group", "save", "show", "make", "assign", "remember", "find", "for each", "when", "otherwise", "classify", "jev", "called", "where", "by", "as", "optional", "into", "on failure", "may fail with", "returning", "to", "call", "return", "while", "repeat", "judge", "score", "create folder", "take", "append", "require", "expect", "define", "failure", "command", "option", "argument", "switch", "using", "ask", "accept", "model", "on uncertain", "on existing", "package", "import", "export", "get text from", "get JSON from", "post as JSON to", "send an HTTP request to", "listen for HTTP requests on", "read text body from", "read JSON body from", "respond to"}
}

func classifyLine(text string) string {
	for _, f := range forms {
		if match(f.kind, text) != nil {
			return f.kind
		}
	}
	return ""
}

// joinWrappedActionHeaders normalizes formatter-produced action signatures
// while preserving physical line indexes by blanking consumed continuations.
func joinWrappedActionHeaders(lines []string) {
	for i := 1; i < len(lines); i++ {
		continuation := strings.TrimSpace(lines[i])
		previousIndex := i - 1
		for previousIndex >= 0 && strings.TrimSpace(lines[previousIndex]) == "" {
			previousIndex--
		}
		previous := ""
		if previousIndex >= 0 {
			previous = strings.TrimSpace(lines[previousIndex])
		}
		if (strings.HasPrefix(continuation, "returning ") || strings.HasPrefix(continuation, "streaming ") || strings.HasPrefix(continuation, "may fail with ")) && strings.HasPrefix(previous, "to ") && !strings.HasSuffix(previous, ":") {
			lines[previousIndex] = previous + " " + continuation
			lines[i] = ""
		}
	}
}

func stripComment(s string) string {
	quoted, esc := false, false
	for i, c := range s {
		if esc {
			esc = false
			continue
		}
		if c == '\\' && quoted {
			esc = true
			continue
		}
		if c == '"' {
			quoted = !quoted
		}
		if c == '#' && !quoted {
			return strings.TrimRight(s[:i], " ")
		}
	}
	return s
}

// Parse builds an indented statement tree, collecting syntax errors without executing code.
func Parse(source string) (*Program, []Diagnostic) {
	p := &Program{Source: source}
	if len(source) > 1<<20 {
		return p, []Diagnostic{{1, 1, "source exceeds 1 MiB limit"}}
	}
	body, _, err := sosconfig.Extract(source)
	if err != nil {
		var positioned *sosconfig.Error
		if errors.As(err, &positioned) {
			return p, []Diagnostic{{positioned.Line, positioned.Column, positioned.Message}}
		}
		return p, []Diagnostic{{1, 1, err.Error()}}
	}
	var ds []Diagnostic
	type frame struct {
		indent int
		list   *[]*Statement
		parent *Statement
	}
	stack := []frame{{-1, &p.Statements, nil}}
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	joinWrappedActionHeaders(lines)
	for i, raw := range lines {
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(strings.TrimSpace(raw), "#") {
			continue
		}
		if strings.Contains(raw, "\t") {
			ds = append(ds, Diagnostic{i + 1, 1, "use spaces, not tabs, for indentation"})
			continue
		}
		text := strings.TrimSpace(stripComment(raw))
		if text == "" {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		if indent%2 != 0 {
			ds = append(ds, Diagnostic{i + 1, 1, "indentation must use multiples of two spaces"})
		}
		for len(stack) > 1 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}
		fr := stack[len(stack)-1]
		expected := fr.indent + 2
		if fr.parent == nil {
			expected = 0
		}
		if indent != expected {
			ds = append(ds, Diagnostic{i + 1, 1, fmt.Sprintf("expected indentation of %d spaces", expected)})
		}
		kind := classifyLine(text)
		if kind == "" && fr.parent != nil {
			// Filesystem modifier continuations classify only under their
			// owning statement, like schema fields under expect headers.
			if isFileForm(fr.parent.Kind) {
				if continuation := classifyFileContinuation(text); continuation != "" && fileContinuationAllowed(fr.parent.Kind, continuation) {
					kind = continuation
				}
			}
		}
		if kind == "" && fr.parent != nil {
			switch fr.parent.Kind {
			case "define", "failure":
				if _, err := parseFieldDecl(text); err == nil {
					kind = "field"
				}
			case "fail":
				if regexp.MustCompile(`^[A-Za-z_]\w* from .+$`).MatchString(text) {
					kind = "field"
				}
			case "schema":
				if regexp.MustCompile(`^\w+ as (?:optional )?(?:text|file|folder|timestamp|number|integer|boolean|duration|[A-Z][A-Za-z0-9_]*|list of (?:text|file|folder|timestamp|number|integer|boolean|duration|[A-Z][A-Za-z0-9_]*))$`).MatchString(text) {
					kind = "field"
				}
			case "make":
				if strings.HasSuffix(fr.parent.Text, " with:") && regexp.MustCompile(`^\w+ from .+$`).MatchString(text) {
					kind = "field"
				}
			case "httpRequest":
				if _, _, ok := httpRequestOptionParts(text); ok {
					kind = "field"
				}
			case "httpListen":
				if _, _, ok := httpListenOptionParts(text); ok {
					kind = "field"
				}
			case "httpRespondComplete":
				if _, _, ok := httpRespondOptionParts(text); ok {
					kind = "field"
				}
			case "classify", "score":
				if regexp.MustCompile(`^(?:"[^"\n]+"|[0-9]+): ".*"$`).MatchString(text) {
					kind = "choice"
				}
			}
		}
		if kind == "" {
			ds = append(ds, Diagnostic{i + 1, indent + 1, "unknown construction: " + text})
			kind = "invalid"
		}
		st := &Statement{Kind: kind, Text: text, Line: i + 1}
		*fr.list = append(*fr.list, st)
		if kind == "otherwise" {
			siblings := *fr.list
			if len(siblings) < 2 || siblings[len(siblings)-2].Kind != "when" {
				ds = append(ds, Diagnostic{i + 1, indent + 1, "otherwise must follow when at the same indentation"})
			}
		}
		if kind == "handler" && (fr.parent == nil || fr.parent.Kind == "command" || fr.parent.Kind == "for") {
			ds = append(ds, Diagnostic{i + 1, indent + 1, "on handler must be indented under its operation"})
		}
		if kind == "command" && fr.parent != nil && fr.parent.Kind != "command" {
			ds = append(ds, Diagnostic{i + 1, 1, "command belongs at top level or directly inside a command"})
		}
		if (kind == "define" || kind == "failure") && fr.parent != nil {
			ds = append(ds, Diagnostic{i + 1, 1, "define declarations belong at top level or in a package"})
		}
		if kind == "package" || kind == "import" || kind == "export" || kind == "word" {
			if fr.parent != nil {
				ds = append(ds, Diagnostic{i + 1, 1, kind + " declarations belong at top level"})
			}
			if kind == "package" {
				for _, sibling := range *fr.list {
					if sibling != st && sibling.Kind == "package" {
						ds = append(ds, Diagnostic{i + 1, 1, "duplicate package declaration"})
					}
				}
			}
		}
		// Any operation may own on-failure handlers, even without a colon.
		stack = append(stack, frame{indent, &st.Body, st})
	}
	var validate func([]*Statement, *Statement)
	validate = func(sts []*Statement, parent *Statement) {
		for _, s := range sts {
			parentKind := ""
			if parent != nil {
				parentKind = parent.Kind
			}
			// A bare concurrent header whose continuation line is
			// "one at a time:" is the split sequential form.
			if s.Kind == "handleEachStream" && streamChild(s, "oneAtATime") != nil {
				s.Kind = "handleOneStream"
			}
			if streamChildOnlyLine(s.Kind) {
				if parent == nil {
					ds = append(ds, Diagnostic{s.Line, 1, s.Kind + " must appear under its stream construction: " + s.Text})
				} else if !streamChildAllowedUnder(s.Kind, parentKind) {
					ds = append(ds, Diagnostic{s.Line, 1, "unexpected line under this stream construction: " + s.Text})
				}
			}
			block := semanticCriterionDeclRe.MatchString(s.Text) || s.Kind == "map" || s.Kind == "for" || s.Kind == "streamFor" || s.Kind == "while" || s.Kind == "repeat" || s.Kind == "when" || s.Kind == "otherwise" || s.Kind == "to" || s.Kind == "command" || s.Kind == "schema" || s.Kind == "define" || s.Kind == "failure" || (s.Kind == "fail" && strings.HasSuffix(s.Text, ":")) || s.Kind == "classify" || s.Kind == "score" || s.Kind == "httpRequest" || s.Kind == "httpListen" || strings.HasSuffix(s.Text, "with:") || strings.HasSuffix(s.Text, "jev:") || s.Kind == "handler" && strings.HasSuffix(s.Text, ":") || streamHandlingBlock(s.Kind)
			if block && len(s.Body) == 0 && s.Kind != "failure" && !streamHandlingOptionalBody(s.Kind) {
				ds = append(ds, Diagnostic{s.Line, 1, "expected an indented body"})
			}
			if children, restricted := streamConstructionChildren[s.Kind]; restricted {
				allowed := map[string]bool{"handler": true}
				for _, k := range children {
					allowed[k] = true
				}
				for _, c := range s.Body {
					if !allowed[c.Kind] {
						ds = append(ds, Diagnostic{c.Line, 1, "unexpected line under this stream construction: " + c.Text})
					}
				}
			} else {
				for _, c := range s.Body {
					if !block && c.Kind != "handler" && !fileContinuationAllowed(s.Kind, c.Kind) {
						ds = append(ds, Diagnostic{c.Line, 1, "only on handlers may follow this operation"})
					}
				}
			}
			validate(s.Body, s)
		}
	}
	validate(p.Statements, nil)
	p.Definitions, _ = collectRecordDefinitions(p.Statements)
	var failureDiagnostics []Diagnostic
	p.Failures, failureDiagnostics = collectFailureDefinitions(p.Statements)
	ds = append(ds, failureDiagnostics...)
	ds = append(ds, validateHTTPStatements(p.Statements)...)
	ds = append(ds, buildCommands(p)...)
	return p, ds
}

func Check(source string) []Diagnostic {
	return checkSource(source, nil)
}
func Format(source string) (string, []Diagnostic) {
	p, ds := Parse(source)
	// Formatting is whitespace-only: unknown constructions (including
	// sentence calls whose vocabulary is resolved elsewhere) must not block
	// it. Every other diagnostic is real and refuses to format.
	for _, d := range ds {
		if !strings.HasPrefix(d.Message, "unknown construction:") {
			return "", ds
		}
	}
	_ = p
	lines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	_, header, _ := sosconfig.Extract(source)
	inHeader := header != nil
	for i := range lines {
		if inHeader {
			if i > 0 && strings.TrimSpace(lines[i]) == "+++" {
				inHeader = false
			}
			continue
		}
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n", nil
}

func maskQuoted(text string) []byte {
	masked := []byte(text)
	quoted, escaped := false, false
	for i, c := range []byte(text) {
		if escaped {
			if quoted {
				masked[i] = '_'
			}
			escaped = false
			continue
		}
		if c == '\\' && quoted {
			masked[i] = '_'
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
			continue
		}
		if quoted {
			masked[i] = '_'
		}
	}
	return masked
}
func splitOutside(text, separator string) (string, string, bool) {
	i := strings.Index(string(maskQuoted(text)), separator)
	if i < 0 {
		return text, "", false
	}
	return text[:i], text[i+len(separator):], true
}

// UsesJev reports whether a statement invokes the provider, matching the same
// token boundaries used by checking and execution (never quoted data).
func UsesJev(s *Statement) bool {
	if s == nil {
		return false
	}
	if s.Kind == "evaluate" {
		return true
	}
	if s.Kind == "judge" || s.Kind == "classify" || s.Kind == "score" {
		return true
	}
	if s.Kind == "keep" {
		m := match("keep", s.Text)
		return m != nil && jevPredicate(m[2])
	}
	return false
}
func jevPredicate(text string) bool { return text == "jev:" || strings.HasPrefix(text, "jev ") }

// streamHandlingBlock reports stream-handling constructions whose header
// opens a handler body. Continuation-only headers (quiet waiting, rate
// limiting, batching) are not blocks: their children are optional policy,
// bound, and naming lines.
func streamHandlingBlock(kind string) bool {
	switch kind {
	case "handleEachStream", "handleOneStream", "newestStream", "conflateStream", "exhaustStream", "filterStream", "projectStream", "newestCancel", "cancelPolicy", "conflatePolicy", "exhaustPolicy":
		return true
	}
	return false
}

// streamHandlingOptionalBody covers block headers whose required policy lines
// are enforced by the checker (with dedicated diagnostics) rather than by the
// generic empty-body rule.
func streamHandlingOptionalBody(kind string) bool {
	switch kind {
	case "limitStream", "newestStream", "conflateStream", "exhaustStream", "handleEachStream", "handleOneStream", "newestCancel", "conflatePolicy", "exhaustPolicy", "cancelPolicy":
		return true
	}
	return false
}

// streamConstructionChildren lists the only statement kinds allowed directly
// under each stream-handling header whose children are policy, bound, and
// naming lines rather than a handler body. Headers with handler bodies are
// unrestricted; their policy lines are governed by streamChildOnlyParents.
var streamConstructionChildren = map[string][]string{
	"quietStream":    {"streamPendingBound", "calledName"},
	"limitStream":    {"throttlePolicy", "rejectExcess", "streamKeyBound", "calledName"},
	"batchStream":    {"batchWindow", "streamKeyBound", "calledName"},
	"idleStream":     {"calledName"},
	"takeForStream":  {"thenStopNormally", "calledName"},
	"deadlineStream": {"calledName"},
	"filterStream":   {"when"},
	"projectStream":  {"useValue"},
}

// streamChildOnlyParents maps every continuation line kind to the statement
// kinds it may appear directly under. Such lines are invalid at top level and
// under any other parent.
var streamChildOnlyParents = map[string][]string{
	"calledName":         {"quietStream", "limitStream", "batchStream", "idleStream", "takeForStream", "deadlineStream"},
	"streamPendingBound": {"quietStream"},
	"streamKeyBound":     {"limitStream", "batchStream"},
	"batchWindow":        {"batchStream"},
	"throttlePolicy":     {"limitStream"},
	"rejectExcess":       {"limitStream"},
	"streamBound":        {"newestStream", "handleEachStream", "handleOneStream"},
	"streamKnownBound":   {"newestStream"},
	"newestCancel":       {"newestStream"},
	"effectAck":          {"newestStream"},
	"cancelPolicy":       {"newestStream"},
	"conflatePolicy":     {"conflateStream"},
	"exhaustPolicy":      {"exhaustStream"},
	"keepItem":           {"when"},
	"useValue":           {"projectStream"},
	"oneAtATime":         {"handleOneStream"},
	"thenStopNormally":   {"takeForStream"},
}

// streamChildOnlyLine reports whether the kind is a continuation line that
// only carries meaning under its stream construction.
func streamChildOnlyLine(kind string) bool {
	_, ok := streamChildOnlyParents[kind]
	return ok
}

// streamChildAllowedUnder reports whether a child-only line kind may appear
// directly under the given parent statement kind.
func streamChildAllowedUnder(kind, parent string) bool {
	for _, p := range streamChildOnlyParents[kind] {
		if p == parent {
			return true
		}
	}
	return false
}
