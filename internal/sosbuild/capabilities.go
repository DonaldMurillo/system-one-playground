package sosbuild

import (
	"fmt"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

// ValidateTarget rejects unavailable host operations before producing an artifact.
// Browser builds currently support the pure language core; WASI additionally
// supports the filesystem explicitly preopened by its runner. Neither ships a
// credential-bearing model proxy or an HTTP host adapter.
func ValidateTarget(p *sos.Program, target Target) error {
	if target == TargetNative || target == "" {
		return nil
	}
	capability := "wasm"
	if target == TargetWasmWasi {
		capability = "wasip1"
	}
	if p.Modules != nil {
		imports := map[string]bool{}
		graph := p.Modules.Graph()
		for _, edge := range graph.Entry {
			imports[edge.Key] = true
		}
		for _, module := range graph.Modules {
			for _, file := range module.Files {
				for _, edge := range file.Imports {
					imports[edge.Key] = true
				}
			}
		}
		for _, library := range graph.Libraries {
			imports[library.Key] = true
			imports[library.Path] = true
		}
		for _, op := range sos.StandardOperations() {
			if !imports[op.ImportPath] {
				continue
			}
			supported := false
			for _, t := range op.Targets {
				if t == capability {
					supported = true
				}
			}
			if !supported {
				return fmt.Errorf("import %s includes %s unavailable on %s", op.ImportPath, op.Name, target)
			}
		}
	}
	var walk func([]*sos.Statement) error
	walk = func(sts []*sos.Statement) error {
		for _, s := range sts {
			if sos.UsesJev(s) {
				return fmt.Errorf("line %d: %s has no Jev provider host; use native for live judgments", s.Line, target)
			}
			if target == TargetWasmBrowser {
				switch s.Kind {
				case "read", "readEach", "find", "folder", "save":
					return fmt.Errorf("line %d: %s needs filesystem access unavailable in wasm-browser", s.Line, s.Kind)
				}
			}
			if e := walk(s.Body); e != nil {
				return e
			}
		}
		return nil
	}
	if err := walk(p.Statements); err != nil {
		return err
	}
	// Imported modules ship inside the artifact; their bodies need the same
	// capability checks. Standard packages are pure native code.
	return walk(p.ImportedStatements())
}
