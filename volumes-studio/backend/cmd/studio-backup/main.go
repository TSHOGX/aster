// studio-backup takes an offline, independently restorable Studio snapshot.
package main

import (
	"context"
	"flag"
	"log"
	"volume-studio/backend/internal/authoring"
)

func main() {
	root := flag.String("state-directory", "", "stopped Studio state directory")
	target := flag.String("output", "", "new private backup directory")
	publisher := flag.String("publisher-id", "", "retained publisher instance ID")
	flag.Parse()
	store, err := authoring.Open(context.Background(), authoring.Config{Directory: *root, PublisherID: *publisher, QuotaBytes: 1 << 62})
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	if err = store.Backup(context.Background(), *target); err != nil {
		log.Fatal(err)
	}
	log.Print("Studio backup complete; retain backup.json with the private snapshot")
}
