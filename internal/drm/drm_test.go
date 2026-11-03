package drm

import "testing"

func TestTokenHashVectors(t *testing.T) {
	for _, v := range hashVectors {
		cs64, digest := TokenHash([]byte(v.data))
		if digest != v.digest {
			t.Errorf("digest(%q) = %s, want %s", v.data, digest, v.digest)
		}
		if cs64 != v.cs64 {
			t.Errorf("cs64(%q) = %s, want %s", v.data, cs64, v.cs64)
		}
	}
}

func TestGenerateVectors(t *testing.T) {
	for _, v := range tokenVectors {
		if got := Generate(v.ts); got != v.token {
			t.Errorf("Generate(%v) = %s, want %s", v.ts, got, v.token)
		}
	}
}

func TestTokenShape(t *testing.T) {
	tok := Generate(1793692800)
	if len(tok) != 16+1+64 {
		t.Fatalf("token length = %d, want 81: %q", len(tok), tok)
	}
	if tok[16] != '-' {
		t.Fatalf("token separator missing: %q", tok)
	}
}

// TestWindowRounding pins the 5-minute rounding behaviour.
func TestWindowRounding(t *testing.T) {
	a := Generate(1793700000)
	b := Generate(1793700000 + 299)
	if a != b {
		t.Fatalf("timestamps in the same window differ: %s vs %s", a, b)
	}
	c := Generate(1793700000 + 300)
	if a == c {
		t.Fatalf("timestamps in different windows collided: %s", a)
	}
}
