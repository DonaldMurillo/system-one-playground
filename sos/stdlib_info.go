package sos

import (
	"sort"
	"strings"
)

// StandardOperationInfo is the offline tooling view of an executable native
// operation. Params come from the same registry used to validate runtime calls.
type StandardOperationInfo struct {
	ImportPath  string        `json:"importPath"`
	Package     string        `json:"package"`
	Name        string        `json:"name"`
	Params      []NativeParam `json:"params"`
	Result      string        `json:"result"`
	Description string        `json:"description"`
	Effects     []string      `json:"effects"`
	Targets     []string      `json:"targets"`
}

// StandardOperations returns a detached, deterministic catalog. It performs no
// filesystem or network work and cannot consume a Jev request budget.
func StandardOperations() []StandardOperationInfo {
	var out []StandardOperationInfo
	for path, ops := range stdRegistry {
		for name, op := range ops {
			doc := stdDocs[path+"."+name]
			targets := op.Targets
			if targets == nil {
				targets = []string{"native", "wasm", "wasip1"}
			}
			out = append(out, StandardOperationInfo{
				ImportPath:  path,
				Package:     path[strings.LastIndex(path, "/")+1:],
				Name:        name,
				Params:      append([]NativeParam(nil), op.Params...),
				Result:      doc.result,
				Description: doc.description,
				Effects:     append([]string(nil), doc.effects...),
				Targets:     append([]string(nil), targets...),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ImportPath != out[j].ImportPath {
			return out[i].ImportPath < out[j].ImportPath
		}
		return out[i].Name < out[j].Name
	})
	return out
}
