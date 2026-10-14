package manifest

import "testing"

func TestIncompleteAuthoringStillRequiresSafeStructure(t *testing.T) {
	incomplete := []Entry{{Kind: "directory", Path: "skills", Size: "0"}, {Kind: "directory", Path: "skills/unfinished", Size: "0"}}
	if err := ValidateAuthoring(SharedAssetsProfile, incomplete); err != nil {
		t.Fatal(err)
	}
	if _, err := NewProfile("p", "l", "v", SharedAssetsProfile, incomplete); err == nil {
		t.Fatal("incomplete tree published")
	}
	cases := [][]Entry{
		{{Kind: "directory", Path: "missing/child", Size: "0"}},
		{{Kind: "directory", Path: ".git", Size: "0"}},
		{{Kind: "directory", Path: "../escape", Size: "0"}},
		{{Kind: "directory", Path: "same", Size: "0"}, {Kind: "directory", Path: "same", Size: "0"}},
	}
	for _, entries := range cases {
		if ValidateAuthoring(SharedAssetsProfile, entries) == nil {
			t.Fatalf("unsafe draft accepted: %+v", entries)
		}
	}
}
