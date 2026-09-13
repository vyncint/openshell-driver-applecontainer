package compat

import (
	"strings"
	"testing"
)

func TestParseAndCompare(t *testing.T) {
	for _, s := range []string{"1.3.0", "v1.3.0", "(1.3.0)", " 1.3.0 "} {
		v, err := Parse(s)
		if err != nil || v != (Version{1, 3, 0}) {
			t.Errorf("Parse(%q) = %v, %v", s, v, err)
		}
	}
	for _, bad := range []string{"", "1.3", "1.3.x", "1.-1.0", "dev", "1.3.0.1"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.0", "1.3.1", -1},
		{"1.3.1", "1.3.1", 0},
		{"1.4.1", "1.3.1", 1},
		{"0.0.116", "0.0.96", 1}, // numeric, not lexical
		{"2.0.0", "1.99.99", 1},
	}
	for _, c := range cases {
		if got := Compare(MustParse(c.a), MustParse(c.b)); got != c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func level(fs []Finding) string {
	worst := "ok"
	for _, f := range fs {
		if f.Level == "error" || (f.Level == "warn" && worst == "ok") {
			worst = f.Level
		}
	}
	return worst
}

func TestCheckAppleContainer(t *testing.T) {
	// Vulnerable but supported: the advisory warning names the upgrade path.
	fs := CheckAppleContainer("1.3.0")
	if level(fs) != "warn" || !strings.Contains(fs[0].Message, "security advisories") || !strings.Contains(fs[0].Message, "update-container.sh") {
		t.Errorf("1.3.0 findings = %+v", fs)
	}
	if fs := CheckAppleContainer("1.3.1"); level(fs) != "ok" {
		t.Errorf("1.3.1 should be clean, got %+v", fs)
	}
	if fs := CheckAppleContainer(AppleContainerMax); level(fs) != "ok" {
		t.Errorf("max should be clean, got %+v", fs)
	}
	if fs := CheckAppleContainer("9.0.0"); level(fs) != "warn" || !strings.Contains(fs[0].Message, "newer") {
		t.Errorf("newer-than-verified should warn, got %+v", fs)
	}
	if fs := CheckAppleContainer("1.1.0"); level(fs) != "error" {
		t.Errorf("older-than-supported should error, got %+v", fs)
	}
	if fs := CheckAppleContainer(""); level(fs) != "warn" {
		t.Errorf("unknown version should warn, got %+v", fs)
	}
}

func TestCheckOpenShell(t *testing.T) {
	if fs := CheckOpenShell("0.0.113"); level(fs) != "ok" {
		t.Errorf("0.0.113 findings = %+v", fs)
	}
	if fs := CheckOpenShell("0.0.95"); level(fs) != "error" {
		t.Errorf("pre-contract should error, got %+v", fs)
	}
	if fs := CheckOpenShell("0.0.999"); level(fs) != "warn" {
		t.Errorf("newer should warn, got %+v", fs)
	}
	if fs := CheckOpenShell("garbage"); level(fs) != "warn" {
		t.Errorf("unknown should warn, got %+v", fs)
	}
}
