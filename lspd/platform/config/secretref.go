package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// SecretRef holds a config value that carries a secret — today that's
// NodeConfig.FlspdPrivateKey and LndConfig.Cert/Macaroon — so it can be
// provided inline (the historical convention: a plain hex string directly
// in nodes.yaml) or resolved at the point of use from an environment
// variable or a file, without ever committing the raw secret to the config
// file. This is the root fix for C-1 (the dashboard used to leak these
// values to the browser) and prevents a recurrence of SEC-1 (a real secret
// committed to data/nodes.prod.yaml).
//
// The zero value is a valid, empty SecretRef (Resolve returns "").
//
// Deliberately NOT resolved at load time: the dashboard's config-update
// handler round-trips a NodeConfig back out to YAML on every save
// (dashboard/service.go's handleUpdateConfig), copying these fields
// verbatim from the in-memory config rather than the (secret-stripped)
// browser payload. If Resolve() replaced the field's value in place, the
// resolved secret itself — not the original {from_env}/{from_file}
// reference — would get written back to disk on the next save.
type SecretRef struct {
	inline   *string
	fromEnv  *string
	fromFile *string
}

// InlineSecret builds a SecretRef holding a literal value, equivalent to
// what unmarshaling a plain YAML scalar produces. Exists so callers
// outside this package (test fixtures, code generating a NodeConfig in
// memory rather than parsing it from YAML) can construct one without
// reaching into unexported fields.
func InlineSecret(value string) SecretRef {
	return SecretRef{inline: &value}
}

type secretRefYAML struct {
	FromEnv  string `yaml:"from_env,omitempty"`
	FromFile string `yaml:"from_file,omitempty"`
}

// UnmarshalYAML accepts either a plain scalar string (today's flat
// convention) or a single-key mapping, {from_env: VAR} / {from_file: /path}.
func (r *SecretRef) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		*r = SecretRef{inline: &s}
		return nil
	}

	var ref secretRefYAML
	if err := value.Decode(&ref); err != nil {
		return fmt.Errorf("secret value must be a plain string, {from_env: VAR}, or {from_file: /path}: %w", err)
	}
	switch {
	case ref.FromEnv != "" && ref.FromFile != "":
		return fmt.Errorf("secret ref cannot set both from_env and from_file")
	case ref.FromEnv != "":
		*r = SecretRef{fromEnv: &ref.FromEnv}
	case ref.FromFile != "":
		*r = SecretRef{fromFile: &ref.FromFile}
	default:
		return fmt.Errorf("secret ref must set either from_env or from_file")
	}
	return nil
}

// MarshalYAML re-emits the SecretRef's original form — the plain string it
// was loaded from, or the {from_env}/{from_file} reference — never a
// resolved secret, and never in a form other than how it was provided.
func (r SecretRef) MarshalYAML() (interface{}, error) {
	switch {
	case r.inline != nil:
		return *r.inline, nil
	case r.fromEnv != nil:
		return secretRefYAML{FromEnv: *r.fromEnv}, nil
	case r.fromFile != nil:
		return secretRefYAML{FromFile: *r.fromFile}, nil
	default:
		return "", nil
	}
}

// Resolve returns the actual secret value: the inline value verbatim, the
// named environment variable's value verbatim, or a file's contents.
//
// Every field this type replaces was, before ref support existed, always a
// hex string, and every consumer downstream of Resolve() still expects
// one. Inline and from_env values are assumed already hex (unchanged
// behavior — this matches, e.g., `flspd genkey`'s FLSPD_PRIVATE_KEY=<hex>
// output convention). from_file is more lenient, since it's the natural
// way to point at flnd's own tls.cert/*.macaroon files, which are raw
// bytes, not pre-hex-encoded text: the file's trimmed contents are used
// as-is if they already look like valid hex, and hex-encoded otherwise.
// The untrimmed original bytes are what gets hex-encoded in that fallback
// case, not the trimmed copy, so stripping leading/trailing whitespace
// can't corrupt a binary secret that happens to contain whitespace-like
// byte values.
func (r SecretRef) Resolve() (string, error) {
	switch {
	case r.inline != nil:
		return *r.inline, nil
	case r.fromEnv != nil:
		v, ok := os.LookupEnv(*r.fromEnv)
		if !ok {
			return "", fmt.Errorf("environment variable %q (referenced by from_env) is not set", *r.fromEnv)
		}
		return v, nil
	case r.fromFile != nil:
		data, err := os.ReadFile(*r.fromFile)
		if err != nil {
			return "", fmt.Errorf("reading file %q (referenced by from_file): %w", *r.fromFile, err)
		}
		trimmed := strings.TrimSpace(string(data))
		if trimmed != "" {
			if _, err := hex.DecodeString(trimmed); err == nil {
				return trimmed, nil
			}
		}
		return hex.EncodeToString(data), nil
	default:
		return "", nil
	}
}

// IsSet reports whether the ref carries any value at all — inline,
// from_env, or from_file — as opposed to being an unset zero value.
func (r SecretRef) IsSet() bool {
	return r.inline != nil || r.fromEnv != nil || r.fromFile != nil
}

// isInline reports whether the ref holds a literal value rather than a
// from_env/from_file reference. Unexported: only meant for this package's
// own tests to assert that committed example config doesn't carry secret
// material inline.
func (r SecretRef) isInline() bool {
	return r.inline != nil
}
