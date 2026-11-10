package version

import "testing"

func TestString(t *testing.T) {
	oldV, oldC := Version, Commit
	t.Cleanup(func() { Version, Commit = oldV, oldC })

	cases := []struct {
		version, commit, want string
	}{
		{"0.0.0-dev", "unknown", "BeeIRCd 0.0.0-dev"},
		{"0.1.0", "", "BeeIRCd 0.1.0"},
		{"0.1.0", "8e0aa2f", "BeeIRCd 0.1.0 (8e0aa2f)"},
	}
	for _, c := range cases {
		Version, Commit = c.version, c.commit
		if got := String(); got != c.want {
			t.Errorf("String() with %q/%q = %q, want %q", c.version, c.commit, got, c.want)
		}
	}
}
