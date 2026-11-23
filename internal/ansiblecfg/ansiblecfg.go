// Package ansiblecfg works out the ANSIBLE_CALLBACK_PLUGINS value that loads
// kyo's plugin without dropping the user's own. Setting that variable replaces
// the callback_plugins from ansible.cfg rather than adding to it, so kyo has to
// read the effective value and prepend its dir.
package ansiblecfg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// EnvKey is the variable ansible reads the callback plugin path from.
const EnvKey = "ANSIBLE_CALLBACK_PLUGINS"

// pathSetting is the config name of callback_plugins in ansible-config output.
const pathSetting = "DEFAULT_CALLBACK_PLUGIN_PATH"

// Lookup returns the effective callback plugin path as ansible would resolve
// it in dir with env.
type Lookup func(ctx context.Context, dir string, env []string) ([]string, error)

// CallbackPath returns the value to export as ANSIBLE_CALLBACK_PLUGINS: ours
// first, then whatever the user would otherwise have had. If the user set the
// variable themselves it is used as is, with no lookup. A failed lookup falls
// back to ours alone plus ansible's documented default, and reports the error
// so the caller can note it.
func CallbackPath(ctx context.Context, ours, dir string, env []string, lookup Lookup) (string, error) {
	if v, ok := envValue(env, EnvKey); ok && v != "" {
		return join(ours, splitPath(v)), nil
	}
	theirs, err := lookup(ctx, dir, env)
	if err != nil {
		return join(ours, defaultPath()), err
	}
	return join(ours, theirs), nil
}

// ConfigDump is the Lookup that asks `ansible-config dump`, so the answer
// matches the ansible.cfg search order exactly (ANSIBLE_CONFIG, ./ansible.cfg,
// ~/.ansible.cfg, /etc/ansible/ansible.cfg). ansibleConfig is the binary;
// empty means "ansible-config" on PATH.
func ConfigDump(ansibleConfig string) Lookup {
	if ansibleConfig == "" {
		ansibleConfig = "ansible-config"
	}
	return func(ctx context.Context, dir string, env []string) ([]string, error) {
		cmd := exec.CommandContext(ctx, ansibleConfig, "dump", "--format", "json")
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) && len(ee.Stderr) > 0 {
				return nil, fmt.Errorf("ansible-config dump: %w: %s", err, strings.TrimSpace(string(ee.Stderr)))
			}
			return nil, fmt.Errorf("ansible-config dump: %w", err)
		}
		return parseDump(out)
	}
}

// parseDump pulls the callback plugin path out of `ansible-config dump
// --format json` output: a list of {name, origin, value} objects.
func parseDump(out []byte) ([]string, error) {
	var entries []struct {
		Name  string `json:"name"`
		Value any    `json:"value"`
	}
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("parsing ansible-config dump: %w", err)
	}
	for _, e := range entries {
		if e.Name != pathSetting {
			continue
		}
		switch v := e.Value.(type) {
		case []any:
			paths := make([]string, 0, len(v))
			for _, p := range v {
				if s, ok := p.(string); ok && s != "" {
					paths = append(paths, s)
				}
			}
			return paths, nil
		case string:
			return splitPath(v), nil
		case nil:
			return nil, nil
		}
	}
	return nil, fmt.Errorf("parsing ansible-config dump: no %s entry", pathSetting)
}

// defaultPath is ansible's built-in callback_plugins default.
func defaultPath() []string {
	var paths []string
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".ansible", "plugins", "callback"))
	}
	return append(paths, "/usr/share/ansible/plugins/callback")
}

func join(ours string, theirs []string) string {
	parts := []string{ours}
	for _, p := range theirs {
		if p != "" && p != ours {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, string(os.PathListSeparator))
}

func splitPath(v string) []string {
	var out []string
	for p := range strings.SplitSeq(v, string(os.PathListSeparator)) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, e := range slices.Backward(env) { // last one wins, as in exec
		if v, ok := strings.CutPrefix(e, prefix); ok {
			return v, true
		}
	}
	return "", false
}
