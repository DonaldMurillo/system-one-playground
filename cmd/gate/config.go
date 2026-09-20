package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config tunes the gate without recompiling it. The gate reads, in order:
//
//	<cwd>/.typesafe-gate.json      per-project
//	~/.config/typesafe-gate.json   per-user
//
// The first file found wins. Environment variables override single fields, so
// a session can disable the gate with TYPESAFE_GATE=off.
type Config struct {
	Enabled    bool       `json:"enabled"`
	Model      string     `json:"model"`
	TimeoutMS  int        `json:"timeout_ms"`
	AuditLog   string     `json:"audit_log"`
	Thresholds Thresholds `json:"thresholds"`
}

// Thresholds are the policy dials. Defaults are deliberately permissive:
// a gate that fires constantly gets switched off, and then it protects nothing.
type Thresholds struct {
	// Intent. These decide whether the call is the thing that was asked for.
	RequestWantsChanges    float64 `json:"request_wants_changes"`
	ModifiesState          float64 `json:"modifies_state"`
	AdvancesRequest        float64 `json:"advances_request"`
	TargetUnrelated        float64 `json:"target_unrelated"`
	MentionsInfrastructure float64 `json:"mentions_infrastructure"`
	DescriptionMatches     float64 `json:"description_matches"`

	// Safety backstop.
	Destructive      float64 `json:"destructive"`
	Reversible       float64 `json:"reversible"`
	OutsideScope     float64 `json:"outside_scope"`
	OutsideWorkspace float64 `json:"outside_workspace"`
	Credentials      float64 `json:"credentials"`
	NetworkWrite     float64 `json:"network_write"`

	AskBlastRadius  float64 `json:"ask_blast_radius"`
	DenyBlastRadius float64 `json:"deny_blast_radius"`
	MinConfidence   float64 `json:"min_confidence"`

	AskOutsideWorkspace bool `json:"ask_outside_workspace"`
	AskNetworkWrite     bool `json:"ask_network_write"`
}

func defaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Enabled:   true,
		Model:     "", // empty means the client default, jev-latest
		TimeoutMS: 3000,
		AuditLog:  filepath.Join(home, ".typesafe-gate", "audit.jsonl"),
		Thresholds: Thresholds{
			// A Noul near 0.5 means genuine uncertainty, so act on clear reads only.

			// Intent. Asking to look at code and getting an edit is the failure
			// worth catching, so these fire on a reasonably clear read.
			RequestWantsChanges: 0.40, // below this, the request reads as look-only
			ModifiesState:       0.60,
			AdvancesRequest:     0.35, // below this, the action is not serving the ask
			TargetUnrelated:     0.60, // above this, the target is a different subject entirely
			// CI and deployment config outlive the task, so the warning is
			// suppressed only when the request clearly did raise the subject.
			MentionsInfrastructure: 0.60,
			DescriptionMatches:     0.40, // below this, the stated intent is misleading

			Destructive:      0.70,
			Reversible:       0.35, // fires when reversibility is doubted, not merely unproven
			OutsideScope:     0.75, // scope is the noisiest judgment; demand confidence
			OutsideWorkspace: 0.80,
			Credentials:      0.60, // a leaked secret is worse than one extra prompt
			NetworkWrite:     0.70,

			AskBlastRadius:  2.5, // "beyond this project"
			DenyBlastRadius: 3.5, // "production or no undo"
			MinConfidence:   0.50,

			AskOutsideWorkspace: true,
			AskNetworkWrite:     true,
		},
	}
}

func (c Config) RequestTimeout() time.Duration {
	if c.TimeoutMS <= 0 {
		return 3 * time.Second
	}
	return time.Duration(c.TimeoutMS) * time.Millisecond
}

func loadConfig(cwd string) Config {
	cfg := defaultConfig()
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(cwd, ".typesafe-gate.json"),
		filepath.Join(home, ".config", "typesafe-gate.json"),
	} {
		if p == "" {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		// Unmarshal onto the defaults so a partial file only overrides what it names.
		if err := json.Unmarshal(b, &cfg); err != nil {
			logf("config %s: %v", p, err)
		}
		break
	}

	switch os.Getenv("TYPESAFE_GATE") {
	case "off", "0", "false":
		cfg.Enabled = false
	case "on", "1", "true":
		cfg.Enabled = true
	}
	if v := os.Getenv("TYPESAFE_GATE_MODEL"); v != "" {
		cfg.Model = v
	}
	if v := os.Getenv("TYPESAFE_GATE_AUDIT"); v != "" {
		cfg.AuditLog = v
	}
	if v := os.Getenv("TYPESAFE_GATE_TIMEOUT_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.TimeoutMS = n
		}
	}
	return cfg
}
