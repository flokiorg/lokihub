package mcp

import (
	"encoding/json"
	"regexp"
	"sort"
	"testing"

	"github.com/flokiorg/lokihub/db"
)

// secretField matches output property names that would carry a credential or
// key material. Any match in any tool's output schema fails the build — the
// CRIT-1 class (a read tool returning live bearer cash tokens) is caught
// mechanically, not by review.
var secretField = regexp.MustCompile(`(?i)(token|secret|connection_?string|mnemonic|macaroon|password|seed|priv(ate)?_?key|preimage|(^|_)nsec|api_?key|credential|jwt|cookie|pairing)`)

// allowedSecretLike are property names that match the pattern but are not
// secrets, each justified.
var allowedSecretLike = map[string]string{
	"autoUnlockPasswordEnabled":   "boolean flag (is auto-unlock on), not the password",
	"autoUnlockPasswordSupported": "boolean capability flag, not the password",
}

func walkSchemaProps(v any, path string, out map[string]string) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if props, ok := m["properties"].(map[string]any); ok {
		for name, sub := range props {
			out[path+"."+name] = name
			walkSchemaProps(sub, path+"."+name, out)
		}
	}
	for _, k := range []string{"items", "additionalProperties"} {
		if sub, ok := m[k]; ok {
			walkSchemaProps(sub, path+"[]", out)
		}
	}
	for _, k := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := m[k].([]any); ok {
			for _, sub := range arr {
				walkSchemaProps(sub, path, out)
			}
		}
	}
	if defs, ok := m["$defs"].(map[string]any); ok {
		for name, sub := range defs {
			walkSchemaProps(sub, path+"#"+name, out)
		}
	}
}

func TestNoSecretsInToolOutputs(t *testing.T) {
	s := NewServer(testAPI{}, &fakeAuth{})
	s.SetAppResolver(func(uint) *db.App { return nil })
	cs, done := connectInMem(t, s, &Conn{AppID: 1, Scopes: AllScopes()})
	defer done()
	lt, err := cs.ListTools(ctxBackground(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	for _, tl := range lt.Tools {
		if tl.OutputSchema == nil {
			continue
		}
		b, _ := json.Marshal(tl.OutputSchema)
		var schema any
		_ = json.Unmarshal(b, &schema)
		props := map[string]string{}
		walkSchemaProps(schema, tl.Name, props)
		for path, name := range props {
			if secretField.MatchString(name) {
				if _, ok := allowedSecretLike[name]; !ok {
					bad = append(bad, path)
				}
			}
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("tool outputs expose secret-like fields (redact them):\n%v", bad)
	}
}
