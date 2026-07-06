// Command authhash generates a PBKDF2-SHA256 password hash suitable for the
// orchestrator's config/auth-users.yaml. It reads the password from the
// HARNESS_PASSWORD env var, the -password flag, or interactively from stdin,
// and prints the encoded hash line.
//
// Usage:
//
//	echo -n 'my-password' | go run ./cmd/authhash
//	go run ./cmd/authhash -password 'my-password'
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"harness-platform/orchestrator/internal/auth"
)

func main() {
	passwordFlag := flag.String("password", "", "password to hash (prefer stdin or HARNESS_PASSWORD to avoid shell history)")
	iterations := flag.Int("iterations", auth.DefaultIterations, "PBKDF2 iteration count")
	flag.Parse()

	password := *passwordFlag
	if password == "" {
		password = os.Getenv("HARNESS_PASSWORD")
	}
	if password == "" {
		fmt.Fprint(os.Stderr, "Enter password: ")
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(os.Stderr, "error: no password provided")
			os.Exit(1)
		}
		password = strings.TrimRight(line, "\r\n")
	}
	if password == "" {
		fmt.Fprintln(os.Stderr, "error: empty password")
		os.Exit(1)
	}

	hash, err := auth.HashPasswordWithIterations(password, *iterations)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Println(hash)
}
