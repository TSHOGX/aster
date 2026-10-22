package authoring

import (
	"context"
	"io"
	"testing"

	manifest "aster/contracts/volumemanifest"
)

func TestMaterializeVerifiesBytesBeforeAttachment(t *testing.T) {
	for _, value := range []string{"wrong", "too long", ""} {
		t.Run(value, func(t *testing.T) {
			s, _ := testStore(t)
			e := manifest.Entry{Path: "file", Kind: "file", Size: "5", Digest: digestBytes([]byte("right"))}
			err := s.materialize(context.Background(), e, func(w io.Writer) error { _, err := io.WriteString(w, value); return err })
			if err == nil {
				t.Fatal("invalid source accepted")
			}
			var count int
			if err := s.db.QueryRow("SELECT count(*) FROM blobs").Scan(&count); err != nil || count != 0 {
				t.Fatal("invalid bytes registered", count, err)
			}
		})
	}
	s, _ := testStore(t)
	e := manifest.Entry{Path: "file", Kind: "file", Size: "5", Digest: digestBytes([]byte("right"))}
	if err := s.materialize(context.Background(), e, func(w io.Writer) error { _, err := io.WriteString(w, "right"); return err }); err != nil {
		t.Fatal(err)
	}
	if err := s.materialize(context.Background(), e, func(io.Writer) error { t.Fatal("existing blob refetched"); return nil }); err != nil {
		t.Fatal(err)
	}
}
