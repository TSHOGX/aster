package main

import (
	"fmt"
	"os"
	"strings"
)

// Accept --config before or after a subcommand without changing its flags.
func globalOptions() error {
	args := []string{os.Args[0]}
	for i := 1; i < len(os.Args); i++ {
		a := os.Args[i]
		if a == "--config" {
			i++
			if i == len(os.Args) || os.Args[i] == "" {
				return fmt.Errorf("--config requires a path")
			}
			if err := os.Setenv("HARNESS_CONFIG_PATH", os.Args[i]); err != nil {
				return err
			}
		} else if strings.HasPrefix(a, "--config=") {
			p := strings.TrimPrefix(a, "--config=")
			if p == "" {
				return fmt.Errorf("--config requires a path")
			}
			if err := os.Setenv("HARNESS_CONFIG_PATH", p); err != nil {
				return err
			}
		} else {
			args = append(args, a)
		}
	}
	os.Args = args
	return nil
}
