// Command experiments stress-tests TypeSafe beyond the happy path: measured
// accuracy and calibration, the documented failure modes, guardrails, large
// option sets, throughput, token-budget edges, a two-stage workflow, and
// judgments as features for a classical model.
//
//	go run ./cmd/experiments                 # every normal experiment
//	go run ./cmd/experiments e3              # only experiments whose name contains "e3"
//	go run ./cmd/experiments -heavy          # include the load tests
//	go run ./cmd/experiments e10-sustained   # an exact name always runs, heavy or not
//
// Experiments marked heavy deliberately push past the API's rate limits. They
// are skipped unless -heavy is given or the experiment is named exactly, so a
// loose filter such as "e1" cannot start them by accident.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

type experiment struct {
	name  string
	run   func(context.Context, *typesafe.Client) error
	heavy bool // generates load far above normal use; opt-in only
}

var experiments = []experiment{
	{name: "e1-calibration", run: calibration},
	{name: "e2-jaggedness", run: jaggedness},
	{name: "e3-guardrails", run: guardrails},
	{name: "e4-scale", run: scale},
	{name: "e5-throughput", run: throughput, heavy: true}, // bursts of 1,000 requests at up to 128 concurrent
	{name: "e6-budget", run: budget},
	{name: "e7-two-stage", run: twoStage},
	{name: "e8-features", run: features},
	{name: "e9-calibration-2", run: calibration2},
	{name: "e10-sustained", run: sustained, heavy: true}, // 90s at ~200 req/s and past the token/s limit
	{name: "e11-scaling-failures", run: scalingFailures},
}

func main() {
	loadDotEnv(".env")
	client, err := typesafe.New(typesafe.WithTimeout(60 * time.Second))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	heavy := flag.Bool("heavy", false, "also run experiments that deliberately exceed the API rate limits")
	flag.Parse()
	filter := flag.Arg(0)

	ctx := context.Background()
	ran, failed := 0, 0
	var skipped []string
	for _, e := range experiments {
		if !strings.Contains(e.name, filter) {
			continue
		}
		if e.heavy && !*heavy && filter != e.name {
			skipped = append(skipped, e.name)
			continue
		}
		ran++
		fmt.Printf("\n=== %s ===\n", e.name)
		start := time.Now()
		before := client.Stats()
		if err := e.run(ctx, client); err != nil {
			failed++
			fmt.Printf("  FAILED: %v\n", err)
		}
		after := client.Stats()
		fmt.Printf("  [%s, %d http attempts, %d retries]\n", time.Since(start).Round(time.Millisecond),
			after.Attempts-before.Attempts, after.Retries-before.Retries)
	}
	fmt.Printf("\n%d/%d experiments completed\n", ran-failed, ran)
	if len(skipped) > 0 {
		fmt.Printf("skipped heavy load tests %s (run with -heavy or name one exactly)\n", strings.Join(skipped, ", "))
	}
	if failed > 0 {
		os.Exit(1)
	}
}

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
		if k, v, ok := strings.Cut(line, "="); ok {
			k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
			if v != "" && os.Getenv(k) == "" {
				os.Setenv(k, v)
			}
		}
	}
}
