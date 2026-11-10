package common

import "testing"

func TestNewRequestIDIsUnique32Hex(t *testing.T) {
	a, b := NewRequestID(), NewRequestID()
	if len(a) != 32 || a == b {
		t.Fatalf("want two distinct 32-char ids, got %q %q", a, b)
	}
}

func TestNodeKeyOf(t *testing.T) {
	x := "c91dfae0c91dfae0c91dfae0c91dfae0c91dfae0c91dfae0c91dfae0c91db891"
	for in, want := range map[string]string{
		x:                x,
		"02" + x:         x,
		"03" + x:         x,
		" 02" + x + "\n": x,
		"04" + x:         "04" + x,
		"02" + upper(x):  x,
	} {
		if got := NodeKeyOf(in); got != want {
			t.Errorf("NodeKeyOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'f' {
			b[i] = c - 32
		}
	}
	return string(b)
}
