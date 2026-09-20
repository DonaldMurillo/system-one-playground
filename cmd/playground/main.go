// Command playground runs a series of scenarios against the TypeSafe API.
//
//	go run ./cmd/playground          # all scenarios
//	go run ./cmd/playground 03       # only scenarios whose name contains "03"
//
// It reads TYPESAFE_API_KEY from the environment or from a .env file in the
// working directory.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

type scenario struct {
	name string
	run  func(context.Context, *typesafe.Client) error
}

var scenarios = []scenario{
	{"01-primitives", primitives},
	{"02-structured-state", structuredState},
	{"03-confidence", confidence},
	{"04-fan-out", fanOut},
	{"05-composite", composite},
	{"06-selection", selection},
	{"07-errors", errorsAndMetadata},
}

func main() {
	loadDotEnv(".env")
	client, err := typesafe.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, "copy .env.example to .env and add your key from https://console.typesafe.ai/settings/keys")
		os.Exit(1)
	}

	filter := ""
	if len(os.Args) > 1 {
		filter = os.Args[1]
	}

	ctx := context.Background()
	ran, failed := 0, 0
	for _, s := range scenarios {
		if !strings.Contains(s.name, filter) {
			continue
		}
		ran++
		heading(s.name)
		start := time.Now()
		if err := s.run(ctx, client); err != nil {
			failed++
			fmt.Printf("  FAILED: %v\n", err)
		}
		fmt.Printf("  (%s)\n", time.Since(start).Round(time.Millisecond))
	}
	fmt.Printf("\n%d/%d scenarios passed\n", ran-failed, ran)
	if failed > 0 {
		os.Exit(1)
	}
}

func heading(s string) { fmt.Printf("\n=== %s ===\n", s) }

func pct(p float64) string { return fmt.Sprintf("%.1f%%", p*100) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// loadDotEnv sets KEY=VALUE lines from path that are not already in the environment.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
		if v != "" && os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}
