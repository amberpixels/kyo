package ansiblecfg

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func fixed(paths []string, err error) Lookup {
	return func(context.Context, string, []string) ([]string, error) { return paths, err }
}

func TestCallbackPath(t *testing.T) {
	ctx := context.Background()

	t.Run("prepends ours to the effective path", func(t *testing.T) {
		got, err := CallbackPath(ctx, "/c/kyo", ".", nil, fixed([]string{"/u/plugins", "/c/kyo"}, nil))
		if err != nil || got != "/c/kyo:/u/plugins" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("user env wins and skips the lookup", func(t *testing.T) {
		env := []string{"ANSIBLE_CALLBACK_PLUGINS=/a:/b"}
		got, err := CallbackPath(ctx, "/c/kyo", ".", env, fixed(nil, errors.New("must not be called")))
		if err != nil || got != "/c/kyo:/a:/b" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("lookup failure falls back to the default and reports", func(t *testing.T) {
		got, err := CallbackPath(ctx, "/c/kyo", ".", nil, fixed(nil, errors.New("boom")))
		if err == nil || !strings.HasPrefix(got, "/c/kyo:") ||
			!strings.HasSuffix(got, "/usr/share/ansible/plugins/callback") {
			t.Fatalf("got %q, %v", got, err)
		}
	})
}

func TestParseDump(t *testing.T) {
	out := []byte(`[
		{"name":"CALLBACKS_ENABLED","origin":"default","value":[]},
		{"name":"DEFAULT_CALLBACK_PLUGIN_PATH","origin":"/p/ansible.cfg","value":["/p/other","/q"]}
	]`)
	got, err := parseDump(out)
	if err != nil || strings.Join(got, ",") != "/p/other,/q" {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := parseDump([]byte(`[{"name":"X","value":1}]`)); err == nil {
		t.Fatal("missing entry should fail")
	}
}
