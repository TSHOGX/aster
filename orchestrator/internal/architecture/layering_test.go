// Package architecture holds tests that enforce dependency direction across the
// orchestrator's internal packages. It has no production code: the point is to
// fail the build when an import edge regresses.
//
// The rules are checked with the stdlib go/build importer, so this adds no
// module dependency. Only *production* imports are checked (GoFiles), not test
// imports: focused tests may compose multiple layers to build fixtures.
package architecture

import (
	"go/build"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const modulePrefix = "harness-platform/orchestrator/"

// rule forbids a package from importing any of the listed packages.
type rule struct {
	// pkg is the module-relative importer, e.g. "internal/store".
	pkg string
	// forbidden lists module-relative packages pkg must not import.
	forbidden []string
	// why explains the invariant so a failure is self-describing.
	why string
}

var rules = []rule{
	{pkg: "internal/product", forbidden: []string{"internal/application", "internal/producthttp", "internal/edge", "internal/engineconfig", "internal/servicegateway", "internal/sandbox/khora"}, why: "Project intent and workers use consumer-owned substrate ports"},
	{pkg: "internal/producthttp", forbidden: []string{"internal/application", "internal/engineconfig", "internal/sandbox/khora", "internal/servicegateway"}, why: "HTTP translates authenticated intent and owns no engine or execution loop"},
	{pkg: "internal/agentrunner", forbidden: []string{"internal/product", "internal/producthttp", "internal/application", "internal/sandbox/khora"}, why: "the guest Agent process knows only its native adapter and journal"},
	{pkg: "internal/agentjournal", forbidden: []string{"internal/product", "internal/producthttp", "internal/application", "internal/sandbox"}, why: "per-incarnation journals are independent from product and engine state"},
	{pkg: "internal/content", forbidden: []string{"internal/product", "internal/application", "internal/producthttp", "internal/sandbox/khora"}, why: "publication uses narrow persistence and public substrate ports"},
	{pkg: "internal/sandbox/khora", forbidden: []string{"internal/product", "internal/application", "internal/producthttp"}, why: "engine observations cannot grant product authority"},
	{pkg: "internal/edge", forbidden: []string{"internal/product", "internal/producthttp", "internal/application", "internal/sandbox"}, why: "the edge routes one origin without owning product state"},
	{pkg: "internal/shareblob", forbidden: []string{"internal/product", "internal/application", "internal/producthttp", "internal/sandbox"}, why: "byte storage is independent of ownership and authorization"},
	{pkg: "internal/runtimerelease", forbidden: []string{"internal/product", "internal/application", "internal/producthttp", "internal/sandbox/khora"}, why: "release identity does not depend on live product or provider state"},
	{pkg: "internal/sqlitex", forbidden: []string{"internal/product", "internal/application", "internal/producthttp", "internal/agentrunner"}, why: "SQLite helpers are a leaf"},
}

func TestLayering(t *testing.T) {
	root := moduleRoot(t)
	for _, r := range rules {
		t.Run(r.pkg, func(t *testing.T) {
			imports := productionImports(t, root, r.pkg)
			for _, bad := range r.forbidden {
				if _, ok := imports[modulePrefix+bad]; ok {
					t.Errorf("layering violation: %s must not import %s\n  why: %s", r.pkg, bad, r.why)
				}
			}
		})
	}
}

// productionImports returns the non-test import set of a module-relative package.
func productionImports(t *testing.T, root, pkg string) map[string]struct{} {
	t.Helper()
	ctx := build.Default
	ctx.Dir = root
	p, err := ctx.ImportDir(filepath.Join(root, pkg), 0)
	if err != nil {
		if _, ok := err.(*build.NoGoError); ok {
			return map[string]struct{}{}
		}
		t.Fatalf("import %s: %v", pkg, err)
	}
	set := make(map[string]struct{}, len(p.Imports))
	for _, imp := range p.Imports {
		set[imp] = struct{}{}
	}
	return set
}

// walkPackages lists every module-relative directory containing Go files.
func walkPackages(t *testing.T, root string) []string {
	t.Helper()
	var pkgs []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if path != root && (base == "testdata" || base == "vendor" || strings.HasPrefix(base, ".")) {
			return filepath.SkipDir
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				if rel != "." {
					pkgs = append(pkgs, filepath.ToSlash(rel))
				}
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(pkgs)
	return pkgs
}

// moduleRoot walks up from the test's directory to the directory holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above test directory")
		}
		dir = parent
	}
}

func forbiddenApplicationDependency(root string, imports func(string) []string) string {
	seen := map[string]bool{}
	var visit func(string) string
	visit = func(pkg string) string {
		if seen[pkg] {
			return ""
		}
		seen[pkg] = true
		for _, dependency := range imports(pkg) {
			if dependency == modulePrefix+"internal/runtime" || dependency == modulePrefix+"internal/server" || dependency == "os/exec" || dependency == "syscall" || dependency == "golang.org/x/sys/unix" || strings.HasPrefix(dependency, "github.com/TSHOGX/khora/internal/") {
				return dependency
			}
			if strings.HasPrefix(dependency, modulePrefix) {
				if bad := visit(dependency); bad != "" {
					return bad
				}
			}
		}
		return ""
	}
	return visit(root)
}

func TestApplicationPortsCannotReachSubstrate(t *testing.T) {
	root := moduleRoot(t)
	imports := func(pkg string) []string {
		set := productionImports(t, root, strings.TrimPrefix(pkg, modulePrefix))
		var result []string
		for dependency := range set {
			result = append(result, dependency)
		}
		return result
	}
	for _, pkg := range []string{"internal/sandbox", "internal/sandbox/khora", "internal/edge"} {
		if bad := forbiddenApplicationDependency(modulePrefix+pkg, imports); bad != "" {
			t.Errorf("%s reaches forbidden substrate dependency %s", pkg, bad)
		}
	}
}

func TestApplicationPortGuardRejectsIndirectSubstrateAccess(t *testing.T) {
	root, helper := modulePrefix+"internal/sandbox", modulePrefix+"internal/helper"
	for _, forbidden := range []string{modulePrefix + "internal/runtime", "os/exec", "syscall", "golang.org/x/sys/unix", "github.com/TSHOGX/khora/internal/lifecycle"} {
		graph := map[string][]string{root: {helper}, helper: {forbidden}}
		if got := forbiddenApplicationDependency(root, func(pkg string) []string { return graph[pkg] }); got != forbidden {
			t.Fatalf("missed indirect substrate edge: %s", forbidden)
		}
	}
}
