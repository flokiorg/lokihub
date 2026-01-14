package common

import (
	"encoding/json"
	"testing"
)

func TestAmount(t *testing.T) {
	var a Amount = 1000
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"1000"` {
		t.Fatalf("expected \"1000\", got %s", string(b))
	}

	var a2 Amount
	if err := json.Unmarshal(b, &a2); err != nil {
		t.Fatal(err)
	}
	if a2 != 1000 {
		t.Fatalf("expected 1000, got %d", a2)
	}

	// Test string number
	b3 := []byte(`"500"`)
	var a3 Amount
	if err := json.Unmarshal(b3, &a3); err != nil {
		t.Fatal(err)
	}
	if a3 != 500 {
		t.Fatal("expected 500")
	}

	// Test raw number (fallback)
	b4 := []byte(`500`)
	var a4 Amount
	if err := json.Unmarshal(b4, &a4); err != nil {
		t.Fatal(err)
	}
	if a4 != 500 {
		t.Fatal("expected 500 from raw int")
	}
}
