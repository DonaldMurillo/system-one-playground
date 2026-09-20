package sosbuild

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"io/fs"
)

//go:generate go run gen/main.go
//go:embed sources.zip
var sourceBundle []byte

func bundledSources() (fs.FS, fs.FS, error) {
	z, e := zip.NewReader(bytes.NewReader(sourceBundle), int64(len(sourceBundle)))
	if e != nil {
		return nil, nil, e
	}
	j, e := fs.Sub(z, "sos")
	if e != nil {
		return nil, nil, e
	}
	t, e := fs.Sub(z, "typesafe")
	return j, t, e
}

// bundledConfig contains config sources plus pinned module metadata, so the
// trimpath CLI can build from a different directory without the source checkout.
func bundledConfig() (fs.FS, []byte, []byte, error) {
	z, err := zip.NewReader(bytes.NewReader(sourceBundle), int64(len(sourceBundle)))
	if err != nil {
		return nil, nil, nil, err
	}
	cfg, err := fs.Sub(z, "sosconfig")
	if err != nil {
		return nil, nil, nil, err
	}
	mod, err := fs.ReadFile(z, "go.mod")
	if err != nil {
		return nil, nil, nil, err
	}
	sum, err := fs.ReadFile(z, "go.sum")
	return cfg, mod, sum, err
}

func bundledTree(path string) (fs.FS, error) {
	z, err := zip.NewReader(bytes.NewReader(sourceBundle), int64(len(sourceBundle)))
	if err != nil {
		return nil, err
	}
	return fs.Sub(z, path)
}
