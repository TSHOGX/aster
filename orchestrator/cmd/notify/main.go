// Command notify inserts a system notification directly into the
// orchestrator's SQLite store. Intended for deploy scripts / release
// automation to announce things like "what's new in this version" to all
// users. It writes straight to the database file, independent of whether the
// orchestrator server is running — so it does not trigger a live SSE push;
// connected clients pick up the new notification on their next poll/reload.
//
// Usage:
//
//	go run ./cmd/notify -title "v2.3 released" -body "Changelog: ..."
//	go run ./cmd/notify -title "..." -body "..." -db /var/lib/harness/state/orchestrator.db
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/store"
)

func main() {
	title := flag.String("title", "", "notification title (required)")
	body := flag.String("body", "", "notification body (required)")
	dbPath := flag.String("db", "/var/lib/harness/state/orchestrator.db", "path to the orchestrator SQLite database")
	flag.Parse()

	if strings.TrimSpace(*title) == "" || strings.TrimSpace(*body) == "" {
		fmt.Fprintln(os.Stderr, "error: -title and -body are required")
		os.Exit(1)
	}

	ctx := context.Background()
	st, err := store.Open(ctx, *dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: open store:", err)
		os.Exit(1)
	}
	defer func() { _ = st.Close() }()

	id := newID("notif")
	if err := st.CreateNotification(ctx, store.Notification{
		ID:        id,
		Title:     *title,
		Body:      *body,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		fmt.Fprintln(os.Stderr, "error: create notification:", err)
		os.Exit(1)
	}
	fmt.Println(id)
}

func newID(prefix string) string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(buf[:])
}
