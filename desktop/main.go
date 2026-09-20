// Desktop shell for SysOneScript Studio (Wails v2). It reuses the exact asset
// bundle and HTTP backend the browser shell serves: the studio Server is the
// asset-server fallback handler, so browser and native render the same UI and
// hit the same /api endpoints. The session token is injected into the served
// document (see internal/studio handleAssets); no credentials are exposed.
package main

import (
	"context"
	_ "embed"
	"fmt"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"log"
	"os"
	"path/filepath"

	"github.com/DonaldMurillo/system-one-playground/internal/studio"
	"github.com/DonaldMurillo/system-one-playground/sos"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed build/appicon.png
var appIcon []byte

func main() {
	// Optional single argument: the working directory runs execute in and
	// the only place .env is loaded from.
	workdir, _ := os.UserHomeDir()
	if workdir == "" {
		workdir = "."
	}
	if len(os.Args) > 1 && os.Args[1] != "" {
		workdir = os.Args[1]
	}
	absDir, err := filepath.Abs(workdir)
	if err != nil {
		log.Fatal(err)
	}
	if info, err := os.Stat(absDir); err != nil || !info.IsDir() {
		log.Fatalf("sos-studio: workdir %s is not a usable directory", absDir)
	}

	// Values are never read or printed here; only load errors surface.
	// Project credentials are loaded per Run/Analyze, never into process-global state.

	srv, err := studio.New(studio.Options{Dir: absDir})
	if err != nil {
		log.Fatal(err)
	}

	desktop := &Desktop{dir: absDir, server: srv}
	err = wails.Run(&options.App{
		Title:     "SysOneScript Studio",
		Width:     1320,
		Height:    860,
		MinWidth:  940,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			// Assets nil: every request (GET and non-GET alike) is forwarded
			// to the studio handler serving the shared embedded webdist.
			Handler: srv,
		},
		Mac: &mac.Options{
			About: &mac.AboutInfo{
				Title:   "SysOneScript Studio",
				Icon:    appIcon,
				Message: fmt.Sprintf("SysOneScript %s", sos.Version),
			},
		},
		OnStartup: func(ctx context.Context) { desktop.ctx = ctx },
		Bind:      []interface{}{desktop},
	})
	if err != nil {
		log.Fatal(err)
	}
}

// Desktop exposes user-invoked native file dialogs to the shared workbench.
type Desktop struct {
	server *studio.Server
	ctx    context.Context
	dir    string
}

func (d *Desktop) Open() (map[string]string, error) {
	p, e := wailsruntime.OpenFileDialog(d.ctx, wailsruntime.OpenDialogOptions{Title: "Open SysOneScript", DefaultDirectory: d.dir, Filters: []wailsruntime.FileFilter{{DisplayName: "SysOneScript", Pattern: "*.sos"}}})
	if e != nil || p == "" {
		return map[string]string{}, e
	}
	info, e := os.Stat(p)
	if e != nil {
		return nil, e
	}
	if info.Size() > 1<<20 {
		return nil, fmt.Errorf("script exceeds 1 MiB")
	}
	b, e := os.ReadFile(p)
	return map[string]string{"name": filepath.Base(p), "source": string(b)}, e
}
func (d *Desktop) Save(name, source string) error {
	p, e := wailsruntime.SaveFileDialog(d.ctx, wailsruntime.SaveDialogOptions{Title: "Save SysOneScript", DefaultDirectory: d.dir, DefaultFilename: filepath.Base(name), Filters: []wailsruntime.FileFilter{{DisplayName: "SysOneScript", Pattern: "*.sos"}}})
	if e != nil || p == "" {
		return e
	}
	return os.WriteFile(p, []byte(source), 0644)
}

func (d *Desktop) ChooseFolder() (string, error) {
	p, e := wailsruntime.OpenDirectoryDialog(d.ctx, wailsruntime.OpenDialogOptions{Title: "Open a project", DefaultDirectory: d.dir})
	if e != nil || p == "" {
		return "", e
	}
	if e = d.server.SetDir(p); e != nil {
		return "", e
	}
	d.dir = p
	return p, nil
}
