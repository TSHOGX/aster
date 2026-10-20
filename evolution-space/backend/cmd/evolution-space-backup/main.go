// Command evolution-space-backup creates an offline projection snapshot.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"

	"aster/evolution-space/internal/modelconn"
	"aster/evolution-space/internal/projections"
)

func main() {
	state := flag.String("state-directory", "", "stopped Evolution Space state directory")
	target := flag.String("output", "", "new private backup directory")
	flag.Parse()
	if *state == "" || *target == "" {
		log.Fatal("-state-directory and -output are required")
	}
	store, err := projections.Open(context.Background(), filepath.Join(*state, "projections.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	// Model connections are a companion database; keys stay encrypted and need
	// the same host secret after restore.
	var extra []projections.SnapshotFile
	if _, err := os.Stat(filepath.Join(*state, "modelconn.db")); err == nil {
		connections, err := modelconn.Open(context.Background(), filepath.Join(*state, "modelconn.db"), "")
		if err != nil {
			log.Fatal(err)
		}
		defer connections.Close()
		extra = append(extra, projections.SnapshotFile{Path: "modelconn.db", Write: connections.Backup})
	}
	if err = store.BackupWith(context.Background(), *target, extra); err != nil {
		log.Fatal(err)
	}
	log.Print("Evolution Space backup complete; retain backup.json with the private snapshot")
}
