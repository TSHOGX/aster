// Command authhash reads a password from stdin and prints a PBKDF2-SHA256 hash
// for an explicitly managed credential registry.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"harness-platform/orchestrator/internal/auth"
)

func main() {
	iterations := flag.Int("iterations", auth.DefaultIterations, "PBKDF2 iteration count")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "password input must use stdin")
		os.Exit(1)
	}
	line, err := bufio.NewReader(io.LimitReader(os.Stdin, 4097)).ReadString('\n')
	if (err != nil && err != io.EOF) || len(line) > 4096 {
		fmt.Fprintln(os.Stderr, "invalid password input")
		os.Exit(1)
	}
	password := strings.TrimRight(line, "\r\n")
	if password == "" {
		fmt.Fprintln(os.Stderr, "empty password")
		os.Exit(1)
	}
	hash, err := auth.HashPasswordWithIterations(password, *iterations)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(hash)
}
