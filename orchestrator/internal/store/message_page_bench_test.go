package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkWorkbenchPages(b *testing.B) {
	for _, n := range []int{500, 10000} {
		b.Run(fmt.Sprintf("messages_%d", n), func(b *testing.B) {
			ctx := context.Background()
			st, e := Open(ctx, filepath.Join(b.TempDir(), "synthetic.db"))
			if e != nil {
				b.Fatal(e)
			}
			defer st.Close()
			now := time.Now()
			e = st.CreateSession(ctx, Session{ID: "synthetic", UserID: "synthetic", Agent: "pi", Mode: "agent", Status: "created", CreatedAt: now, UpdatedAt: now, Workspace: "/synthetic"})
			if e != nil {
				b.Fatal(e)
			}
			tx, e := st.db.BeginTx(ctx, nil)
			if e != nil {
				b.Fatal(e)
			}
			for i := 0; i < n; i++ {
				role := "assistant"
				if i%3 == 0 {
					role = "user"
				}
				if _, e = tx.Exec(`INSERT INTO messages(session_id,role,content,created_at,stop_reason) VALUES('synthetic',?,'synthetic content',?,'stop')`, role, formatTime(now)); e != nil {
					b.Fatal(e)
				}
			}
			if e = tx.Commit(); e != nil {
				b.Fatal(e)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p, e := st.ListMessagesPage(ctx, MessagePageParams{SessionID: "synthetic"})
				if e != nil || len(p.Messages) != 40 {
					b.Fatalf("page %d %v", len(p.Messages), e)
				}
			}
		})
	}
	for _, n := range []int{2000, 10000, 50000} {
		b.Run(fmt.Sprintf("files_%d", n), func(b *testing.B) {
			ctx := context.Background()
			st, e := Open(ctx, filepath.Join(b.TempDir(), "synthetic.db"))
			if e != nil {
				b.Fatal(e)
			}
			defer st.Close()
			now := time.Now()
			if e = st.CreateSession(ctx, Session{ID: "synthetic", UserID: "synthetic", Agent: "pi", Mode: "agent", Status: "created", CreatedAt: now, UpdatedAt: now, Workspace: "/synthetic"}); e != nil {
				b.Fatal(e)
			}
			tx, e := st.db.BeginTx(ctx, nil)
			if e != nil {
				b.Fatal(e)
			}
			for i := 0; i < n; i++ {
				if _, e = tx.Exec(`INSERT INTO artifacts(session_id,path,size,mod_time,created_at,updated_at) VALUES('synthetic',?,100,?,?,?)`, fmt.Sprintf("group%03d/file%07d.txt", i%100, i), formatTime(now), formatTime(now), formatTime(now)); e != nil {
					b.Fatal(e)
				}
			}
			if e = tx.Commit(); e != nil {
				b.Fatal(e)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p, e := st.ArtifactPage(ctx, ArtifactPageParams{SessionID: "synthetic"})
				if e != nil || len(p.Entries) != 100 {
					b.Fatalf("page %d %v", len(p.Entries), e)
				}
			}
		})
	}
}
