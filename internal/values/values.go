// Package values assembles substitution values from explicitly named
// sources. Nothing is read from the environment unless a source names it.
package values

import (
	"fmt"
	"strings"

	"github.com/ashtonian/charmap/v2/internal/token"
)

// Sources lists where values come from. When the same key appears in more
// than one source, the later source in this order wins: EnvPrefix, Env, Set.
type Sources struct {
	// EnvPrefix selects every environment variable whose name starts with
	// the prefix; the key is the remainder of the name. Empty disables it.
	EnvPrefix string
	// Env names environment variables to read; each name is also the key.
	Env []string
	// Set holds KEY=VALUE assignments. Only the first '=' separates the key,
	// so values may contain '=' and ','.
	Set []string
}

// Resolve builds the value map from src. environ is in os.Environ format.
// Errors name keys and variables but never include values.
func Resolve(src Sources, environ []string) (map[string]string, error) {
	env := make(map[string]string, len(environ))
	for _, kv := range environ {
		name, val, ok := strings.Cut(kv, "=")
		if ok && name != "" {
			env[name] = val
		}
	}

	out := make(map[string]string)

	if src.EnvPrefix != "" {
		for name, val := range env {
			key, ok := strings.CutPrefix(name, src.EnvPrefix)
			if !ok {
				continue
			}
			if !token.ValidKey(key) {
				return nil, fmt.Errorf("environment variable %s: %q after prefix %s is not a valid key", name, key, src.EnvPrefix)
			}
			out[key] = val
		}
	}

	seen := make(map[string]bool, len(src.Env))
	for _, name := range src.Env {
		if !token.ValidKey(name) {
			return nil, fmt.Errorf("--env %q: not a valid key", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("--env %s given more than once", name)
		}
		seen[name] = true
		val, ok := env[name]
		if !ok {
			return nil, fmt.Errorf("--env %s: environment variable is not set", name)
		}
		out[name] = val
	}

	seen = make(map[string]bool, len(src.Set))
	for i, assignment := range src.Set {
		key, val, ok := strings.Cut(assignment, "=")
		if !ok {
			// The argument is not echoed: without '=' it may be a bare value.
			return nil, fmt.Errorf("--set argument %d: expected KEY=VALUE", i+1)
		}
		if !token.ValidKey(key) {
			return nil, fmt.Errorf("--set %q: not a valid key", key)
		}
		if seen[key] {
			return nil, fmt.Errorf("--set %s given more than once", key)
		}
		seen[key] = true
		out[key] = val
	}

	return out, nil
}
