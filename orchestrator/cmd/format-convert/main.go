package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"harness-platform/orchestrator/internal/formatconvert"
	"harness-platform/orchestrator/internal/product"
)

func main() {
	source := flag.String("source", "", "quiescent version-1 database copy")
	destination := flag.String("destination", "", "new current-format database path")
	inspect := flag.Bool("inspect", false, "report only the source version and compiled schema fingerprint")
	candidate := flag.Bool("candidate", false, "report the candidate's compiled schema identity")
	rebuild := flag.Bool("rebuild", false, "rebuild derived conversation state in a converted staging copy")
	flag.Parse()
	if *rebuild && *source != "" && *destination == "" && !*inspect && !*candidate && flag.NArg() == 0 {
		if err := formatconvert.Rebuild(context.Background(), *source); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *candidate && *source == "" && *destination == "" && !*inspect && flag.NArg() == 0 {
		version, fingerprint, err := product.SchemaIdentity()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("version=%d schema=%s\n", version, fingerprint)
		return
	}
	if *source == "" || (!*inspect && *destination == "") || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: format-convert --source SNAPSHOT.db [--inspect | --destination NEW.db]")
		os.Exit(2)
	}
	if *inspect {
		version, fingerprint, err := formatconvert.Inspect(context.Background(), *source)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("version=%d schema=%s\n", version, fingerprint)
		return
	}
	if err := formatconvert.Convert(context.Background(), *source, *destination); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
