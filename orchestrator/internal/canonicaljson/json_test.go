package canonicaljson

import "testing"

func TestEvidenceCanonicalizationPreservesIntegerAndTextBytes(t *testing.T) {
	got, err := Bytes([]byte(` {"n":9007199254740993,"text":"<>&中文"} `))
	if err != nil || string(got) != `{"n":9007199254740993,"text":"<>&中文"}` {
		t.Fatalf("canonical evidence changed: %s %v", got, err)
	}
	for _, invalid := range []string{`{"n":1.0}`, `{"n":1e3}`, `{"n":[1.5]}`, `{} {}`} {
		if _, err := Bytes([]byte(invalid)); err == nil {
			t.Fatalf("accepted ambiguous evidence: %s", invalid)
		}
	}
}
