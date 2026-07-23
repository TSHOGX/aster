package pathutil

import "testing"

func TestWithin(t *testing.T) {
	cases := []struct {
		name string
		root string
		path string
		want bool
	}{
		{"equal", "/a/b", "/a/b", true},
		{"child", "/a/b", "/a/b/c", true},
		{"deep child", "/a/b", "/a/b/c/d/e", true},
		{"parent", "/a/b", "/a", false},
		{"sibling", "/a/b", "/a/c", false},
		{"dotdot escape", "/a/b", "/a/b/../..", false},
		{"unclean child", "/a/b", "/a/b/c/../d", true},
		{"unclean equal", "/a/b/", "/a/b/c/..", true},
		{"prefix not child", "/a/bc", "/a/bcd", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Within(tc.root, tc.path); got != tc.want {
				t.Fatalf("Within(%q, %q) = %v, want %v", tc.root, tc.path, got, tc.want)
			}
		})
	}
}

func TestRel(t *testing.T) {
	cases := []struct {
		name    string
		root    string
		path    string
		wantRel string
		wantOK  bool
	}{
		{"child", "/a/b", "/a/b/c", "c", true},
		{"nested child", "/a/b", "/a/b/c/d", "c/d", true},
		{"equal rejected", "/a/b", "/a/b", "", false},
		{"parent rejected", "/a/b", "/a", "", false},
		{"sibling rejected", "/a/b", "/a/c", "", false},
		{"dotdot escape rejected", "/a/b", "/a/b/../c", "", false},
		{"unclean child", "/a/b", "/a/b/c/../d", "d", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rel, ok := Rel(tc.root, tc.path)
			if ok != tc.wantOK || rel != tc.wantRel {
				t.Fatalf("Rel(%q, %q) = (%q, %v), want (%q, %v)", tc.root, tc.path, rel, ok, tc.wantRel, tc.wantOK)
			}
		})
	}
}
