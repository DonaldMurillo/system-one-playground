// Command sos-studio serves the SysOneScript editor workbench on a localhost
// ephemeral port and opens the default browser. The URL carries an
// unpredictable per-session token; the API rejects cross-origin requests.
package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/DonaldMurillo/system-one-playground/internal/studio"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

func main() {
	dir := flag.String("dir", ".", "working directory for runs (also where .env is loaded from)")
	addr := flag.String("addr", "127.0.0.1:0", "listen address; default is an ephemeral localhost port")
	noBrowser := flag.Bool("no-browser", false, "do not open a browser window")
	flag.Parse()

	absDir, err := filepath.Abs(*dir)
	if err != nil {
		fatal(err)
	}
	if info, err := os.Stat(absDir); err != nil || !info.IsDir() {
		fatal(fmt.Errorf("workdir %s is not a usable directory", absDir))
	}

	// Load .env from the workdir through the core if present. Values are
	// never read or printed here; only load errors surface.
	// Project credentials are loaded per Run/Analyze, never into process-global state.

	srv, err := studio.New(studio.Options{Dir: absDir})
	if err != nil {
		fatal(err)
	}

	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		fatal(err)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && !(ip != nil && ip.IsLoopback()) {
		fatal(fmt.Errorf("listen address must be loopback"))
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fatal(err)
	}
	url := fmt.Sprintf("http://%s/?token=%s", ln.Addr().String(), srv.Token())

	fmt.Printf("SysOneScript Studio %s\n", sos.Version)
	fmt.Printf("  url:     %s\n", url)
	fmt.Printf("  workdir: %s\n", absDir)
	fmt.Println("  local only; press Ctrl-C to stop")

	if !*noBrowser {
		openBrowser(url)
	}

	// Shut down cleanly on interrupt while a run may hold a cancel handle.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		os.Exit(0)
	}()

	if err := http.Serve(ln, srv); err != nil {
		fatal(err)
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "sos-studio: open browser: %v\n", err)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "sos-studio: %v\n", err)
	os.Exit(1)
}
