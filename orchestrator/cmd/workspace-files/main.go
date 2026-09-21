package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/projectfiles"
	"os"
)

func main() {
	var request projectfiles.Request
	if len(os.Args) != 2 || json.Unmarshal([]byte(os.Args[1]), &request) != nil {
		fmt.Fprintln(os.Stderr, "one operation document required")
		os.Exit(2)
	}
	if err := projectfiles.Run("/workspace", request, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, projectfiles.ErrUncertain) {
			os.Exit(3)
		}
		os.Exit(1)
	}
}
