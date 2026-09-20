//go:build ignore

// Run through go generate ./internal/sosbuild to refresh the standalone source bundle.
package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, pkg := range []string{"sos", "typesafe", "sosconfig", "internal/semcore"} {
		paths, e := filepath.Glob(filepath.Join("..", "..", pkg, "*.go"))
		if e != nil {
			panic(e)
		}
		rules, _ := filepath.Glob(filepath.Join("..", "..", pkg, "rules", "*.json"))
		paths = append(paths, rules...)
		for _, path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			data, e := os.ReadFile(path)
			if e != nil {
				panic(e)
			}
			w, e := z.Create(pkg + "/" + strings.TrimPrefix(filepath.ToSlash(path), "../../"+pkg+"/"))
			if e != nil {
				panic(e)
			}
			if _, e = w.Write(data); e != nil {
				panic(e)
			}
		}
	}
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			panic(err)
		}
		w, err := z.Create(name)
		if err != nil {
			panic(err)
		}
		if _, err = w.Write(data); err != nil {
			panic(err)
		}
	}
	if e := z.Close(); e != nil {
		panic(e)
	}
	if e := os.WriteFile("sources.zip", b.Bytes(), 0644); e != nil {
		panic(e)
	}
	fmt.Println("Updated standalone compiler source bundle")
}
