package sos

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/DonaldMurillo/system-one-playground/typesafe"
	"os"
)

func semanticProviderFingerprint(ctx context.Context) string {
	values, _ := ctx.Value(environmentContextKey{}).(map[string]string)
	get := func(name string) string {
		if value, ok := values[name]; ok {
			return value
		}
		return os.Getenv(name)
	}
	sum := sha256.Sum256([]byte(get("TYPESAFE_BASE_URL") + "\x00" + get("TYPESAFE_API_KEY")))
	return hex.EncodeToString(sum[:])
}

type environmentContextKey struct{}

// WithEnvironment supplies per-invocation provider settings without mutating
// process environment. Values are copied and never added to results or traces.
func WithEnvironment(ctx context.Context, values map[string]string) context.Context {
	copy := make(map[string]string, len(values))
	for k, v := range values {
		copy[k] = v
	}
	return context.WithValue(ctx, environmentContextKey{}, copy)
}

// providerModel applies explicit source/config choices before project and process
// defaults. Resolve it before building cache/replay keys as well as HTTP requests.
func providerModel(ctx context.Context, explicit string) string {
	if explicit != "" {
		return explicit
	}
	values, _ := ctx.Value(environmentContextKey{}).(map[string]string)
	model, present := values["TYPESAFE_DEFAULT_MODEL"]
	if !present {
		model = os.Getenv("TYPESAFE_DEFAULT_MODEL")
	}
	if model == "" {
		model = typesafe.DefaultModel
	}
	return model
}
