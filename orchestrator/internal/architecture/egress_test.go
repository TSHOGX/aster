package architecture

import (
	"strings"
	"testing"
)

// Traversal through module packages prevents hiding I/O behind a helper.
func impurePolicyDependency(root string, imports func(string) []string) string {
	seen := map[string]bool{}
	var visit func(string) string
	visit = func(pkg string) string {
		if seen[pkg] {
			return ""
		}
		seen[pkg] = true
		for _, dependency := range imports(pkg) {
			switch dependency {
			case "os", "os/exec", "syscall", "unsafe", "net", "net/http", "net/rpc", "database/sql", "io/fs":
				return dependency
			}
			if strings.HasPrefix(dependency, modulePrefix) {
				if bad := visit(dependency); bad != "" {
					return bad
				}
			}
			if strings.Contains(strings.Split(dependency, "/")[0], ".") && !strings.HasPrefix(dependency, modulePrefix) {
				return dependency
			}
		}
		return ""
	}
	return visit(root)
}

func TestEgressCompilerHasNoIOOrProviderDependencies(t *testing.T) {
	root := moduleRoot(t)
	bad := impurePolicyDependency(modulePrefix+"internal/egress", func(pkg string) []string {
		set := productionImports(t, root, strings.TrimPrefix(pkg, modulePrefix))
		var imports []string
		for dependency := range set {
			imports = append(imports, dependency)
		}
		return imports
	})
	if bad != "" {
		t.Fatalf("egress policy compiler reaches I/O or external dependency %s", bad)
	}
}

func TestEgressPurityGuardRejectsTransitiveIO(t *testing.T) {
	root, helper := modulePrefix+"internal/egress", modulePrefix+"internal/helper"
	for _, bad := range []string{"os/exec", "net/http", "database/sql", "github.com/TSHOGX/khora/pkg/khora"} {
		graph := map[string][]string{root: {helper}, helper: {bad}}
		if got := impurePolicyDependency(root, func(pkg string) []string { return graph[pkg] }); got != bad {
			t.Fatalf("missed transitive dependency %s: %s", bad, got)
		}
	}
}
