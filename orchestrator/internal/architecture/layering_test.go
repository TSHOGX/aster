// Package architecture holds tests that enforce dependency direction across the
// orchestrator's internal packages. It has no production code: the point is to
// fail the build when an import edge regresses.
//
// The rules are checked with the stdlib go/build importer, so this adds no
// module dependency. Only *production* imports are checked (GoFiles), not test
// imports: tests legitimately reach across layers to build fixtures (for
// example internal/memorystore's tests open a control store to exercise the
// ingest handoff), and forbidding that would be noise rather than architecture.
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
	{
		pkg: "internal/store",
		forbidden: []string{
			"internal/server",
			"internal/artifacts",
			"internal/bridge",
			"internal/runtime",
			"internal/memorystore",
			"internal/controlsource",
			"internal/dbsplit",
			"internal/config",
			"internal/events",
			"internal/auth",
			"internal/titler",
			"internal/trajectory",
		},
		why: "store is the bottom persistence layer for control.db; consumers depend on it " +
			"through their own narrow interfaces, never the reverse. In particular store must " +
			"not reach back into memorystore -- the memory tables live in a separate database " +
			"and are fed through internal/controlsource.",
	},
	{
		pkg: "internal/memorystore",
		forbidden: []string{
			"internal/store",
			"internal/server",
			"internal/controlsource",
			"internal/bridge",
			"internal/artifacts",
			"internal/runtime",
		},
		why: "memorystore owns its own database file and must stay decoupled from control.db. " +
			"Ingest flows in via memorystore.SessionSource, which internal/controlsource " +
			"implements on top of store -- so the dependency points controlsource -> " +
			"{store, memorystore}, and memorystore never imports store directly. A direct " +
			"edge here would bypass the controlsource seam.",
	},
	{
		pkg: "internal/sqlitex",
		forbidden: []string{
			"internal/store",
			"internal/memorystore",
			"internal/server",
			"internal/agents",
			"internal/sessionstate",
		},
		why: "sqlitex holds shared low-level SQLite helpers and must remain a leaf so both " +
			"store and memorystore can depend on it.",
	},
	{
		pkg:       "internal/agents",
		forbidden: []string{"internal/store", "internal/server", "internal/memorystore", "internal/config"},
		why:       "the agents catalog is a leaf domain package; persistence and transport depend on it.",
	},
	{
		pkg:       "internal/sessionstate",
		forbidden: []string{"internal/store", "internal/server", "internal/memorystore", "internal/agents"},
		why:       "session state is a leaf domain enum; it must not depend on persistence or transport.",
	},
	{
		pkg:       "internal/bridge",
		forbidden: []string{"internal/server", "internal/artifacts", "internal/memorystore"},
		why:       "bridge sits below the HTTP layer and reaches persistence only through bridge.Store.",
	},
	{
		pkg:       "internal/artifacts",
		forbidden: []string{"internal/server", "internal/memorystore", "internal/runtime"},
		why:       "the artifacts watcher is a leaf consumer; the server depends on it, not the reverse.",
	},
	{
		pkg:       "internal/dbsplit",
		forbidden: []string{"internal/server", "internal/store", "internal/memorystore"},
		why: "dbsplit performs a one-window migration over raw SQLite and must not couple to the " +
			"live store types, so the migration stays valid against on-disk databases written by " +
			"any binary version.",
	},
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

// TestControlsourceIsTheOnlyStoreToMemorystoreBridge pins the ingest seam: exactly
// one package may depend on both store and memorystore in production code.
// Anything else joining them means the controlsource seam was bypassed.
func TestControlsourceIsTheOnlyStoreToMemorystoreBridge(t *testing.T) {
	root := moduleRoot(t)
	const allowed = "internal/controlsource"

	var joiners []string
	for _, pkg := range walkPackages(t, root) {
		if pkg == allowed {
			continue
		}
		imports := productionImports(t, root, pkg)
		_, hasStore := imports[modulePrefix+"internal/store"]
		_, hasMemory := imports[modulePrefix+"internal/memorystore"]
		if hasStore && hasMemory {
			joiners = append(joiners, pkg)
		}
	}

	// cmd/orchestrator legitimately wires both together at the process entry
	// point (it opens both databases and constructs the ingestor).
	permitted := map[string]bool{"cmd/orchestrator": true}
	for _, pkg := range joiners {
		if !permitted[pkg] {
			t.Errorf("layering violation: %s imports both store and memorystore in production code; "+
				"cross-database access must go through %s", pkg, allowed)
		}
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
