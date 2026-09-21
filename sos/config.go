package sos

import (
	"fmt"
	"time"

	"github.com/DonaldMurillo/system-one-playground/sosconfig"
)

// EffectiveConfig resolves host configuration and file frontmatter without
// executing code or contacting a provider. dir is the run working directory.
func EffectiveConfig(source, dir string) (sosconfig.Effective, error) {
	layers, err := sosconfig.Load(dir)
	if err != nil {
		return sosconfig.Effective{}, err
	}
	return fileConfig(source, layers)
}

func fileConfig(source string, layers []sosconfig.Layer) (sosconfig.Effective, error) {
	_, header, err := sosconfig.Extract(source)
	if err != nil {
		return sosconfig.Effective{}, err
	}
	if header != nil {
		layers = append(layers, sosconfig.Layer{Name: "frontmatter", Config: *header})
	}
	return sosconfig.Resolve(layers...)
}

func configureRun(p *Program, opts *Options) (time.Duration, error) {
	var policy sosconfig.Effective
	var err error
	if opts.Config == nil {
		policy, err = EffectiveConfig(p.Source, opts.Dir)
	} else {
		// A packaged policy is already resolved; the source header may only narrow
		// its ceilings. Explicit fields also validate externally supplied policies.
		base := sosconfig.Config{Version: 1}
		base.Editor.Assistance = opts.Config.Editor
		base.Interpretation.Mode = opts.Config.Interpretation
		base.Runtime.Judgment = opts.Config.Runtime
		base.External.Process = &opts.Config.ExternalProcess
		base.External.Network = &opts.Config.ExternalNetwork
		base.External.Filesystem = opts.Config.ExternalFilesystem
		secrets := append([]string(nil), opts.Config.ExternalSecrets...)
		base.External.Secrets = &secrets
		base.Budget.Run.Requests = &opts.Config.Requests
		if opts.Config.Timeout != 0 {
			base.Budget.Run.Timeout = opts.Config.Timeout.String()
		}
		policy, err = fileConfig(p.Source, []sosconfig.Layer{{Name: "resolved policy", Config: base}})
	}
	if err != nil {
		return 0, fmt.Errorf("configuration: %w", err)
	}
	if opts.MaxCalls > 0 && opts.MaxCalls < policy.Requests {
		policy.Requests = opts.MaxCalls
	}
	opts.Config = &policy
	opts.MaxCalls = policy.Requests
	if opts.Budget == nil {
		opts.Budget, err = NewRequestBudget(policy.Requests, nil)
		if err != nil {
			return 0, err
		}
	}
	if err := opts.Budget.Constrain(policy.Requests); err != nil {
		return 0, err
	}
	return policy.Timeout, nil
}
