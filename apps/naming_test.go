package apps

import (
	"strings"
	"testing"
)

func TestGenerateChildName(t *testing.T) {
	t.Run("includes hub name and identity prefix", func(t *testing.T) {
		name := GenerateChildName("My Hub", "abcdef0123456789")
		if !strings.HasPrefix(name, "My Hub · abcdef01 · ") {
			t.Fatalf("unexpected name: %q", name)
		}
		if len(name) != len("My Hub · abcdef01 · ")+childNameRandomLen {
			t.Fatalf("unexpected length: %q", name)
		}
	})

	t.Run("omits identity segment when empty", func(t *testing.T) {
		name := GenerateChildName("My Hub", "")
		if !strings.HasPrefix(name, "My Hub · ") {
			t.Fatalf("unexpected name: %q", name)
		}
		if strings.Count(name, "·") != 1 {
			t.Fatalf("expected exactly one segment separator, got: %q", name)
		}
	})

	t.Run("caps total length by trimming the hub name", func(t *testing.T) {
		longHubName := strings.Repeat("x", 200)
		name := GenerateChildName(longHubName, "abcdef0123456789")
		if len([]rune(name)) > maxChildAppNameLen {
			t.Fatalf("name exceeds max length: %d chars: %q", len([]rune(name)), name)
		}
		if !strings.Contains(name, "abcdef01") {
			t.Fatalf("identity label should be preserved over hub name: %q", name)
		}
	})

	t.Run("two calls with the same inputs are not identical", func(t *testing.T) {
		a := GenerateChildName("Hub", "abcdef0123456789")
		b := GenerateChildName("Hub", "abcdef0123456789")
		if a == b {
			t.Fatalf("expected random suffix to differ across calls, got %q twice", a)
		}
	})
}
