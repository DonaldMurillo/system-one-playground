package sos

import "strings"

// EditorMeaning describes syntax independently of runtime provider use.
// It is local metadata, not proof that a program has passed semantic analysis.
type EditorMeaning struct {
	Kind           string
	Head           string
	Criterion      string
	Description    string
	LocallyDefined bool
}

// EditorMeanings uses the interpretation registry and lexical criterion scope;
// no provider requests are made. Keys are one-based source lines.
func EditorMeanings(source string) map[int]EditorMeaning {
	out := map[int]EditorMeaning{}
	var walk func([]*semNode, map[string]bool)
	walk = func(nodes []*semNode, inherited map[string]bool) {
		criteria := map[string]bool{}
		for k, v := range inherited {
			criteria[k] = v
		}
		for _, n := range nodes {
			fields := strings.Fields(n.text)
			if len(fields) == 0 {
				continue
			}
			switch n.role {
			case "criterion":
				decl := parseCriterionDecl(n)
				valid := len(decl.problems) == 0 && !criteria[decl.name]
				criteria[decl.name] = valid
				out[n.line.num] = EditorMeaning{"Language keyword", fields[0], decl.name, "Declares a reusable judgment criterion. Its question and policy are fixed here; Jev evaluates items only when the criterion is applied at runtime.", valid}
			case "semantic":
				info := EditorMeaning{Kind: "Semantic phrase", Head: fields[0], Description: "A supported sentence variant. Analyze resolves its canonical meaning; Jev is consulted only if multiple valid interpretations remain."}
				if strings.HasPrefix(n.form, "keep-criterion") {
					name := n.m[1]
					info = EditorMeaning{"Language keyword", fields[0], name, "Applies the declared criterion to a collection. The syntax has a fixed meaning; Jev judges each item at runtime using the criterion's question, threshold and uncertainty policy.", criteria[name] && n.form == "keep-criterion-name"}
				} else if fields[0] == "filter" {
					info.Description = "Jev selects a supported keep/remove interpretation of this filter sentence. Analyze to inspect the chosen canonical operation. This is source interpretation, distinct from judging items at runtime."
				}
				out[n.line.num] = info
			}
			if n.role != "criterion" {
				walk(n.children, criteria)
			}
		}
	}
	walk(scanSemantic(source).nodes, nil)
	return out
}
