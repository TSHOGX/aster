// Command evolution-space-backup creates an offline projection snapshot.
package main

import (
	"context"
	"flag"
	"log"
	"path/filepath"

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
	if err = store.Backup(context.Background(), *target); err != nil {
		log.Fatal(err)
	}
	log.Print("Evolution Space backup complete; retain backup.json with the private snapshot")
}
