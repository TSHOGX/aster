// Command evolution-space-restore validates and restores an offline snapshot.
package main

import (
	"context"
	"flag"
	"log"

	"aster/evolution-space/internal/projections"
)

func main() {
	source := flag.String("input", "", "complete Evolution Space backup directory")
	target := flag.String("output", "", "new state directory")
	flag.Parse()
	if *source == "" || *target == "" {
		log.Fatal("-input and -output are required")
	}
	if err := projections.Restore(context.Background(), *source, *target); err != nil {
		log.Fatal(err)
	}
	log.Print("Evolution Space restore validated against the current schema")
}
