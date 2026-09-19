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
	{pkg: "internal/sessionfiles", forbidden: []string{"internal/store", "internal/server", "internal/runtime", "internal/workspace/local"}, why: "retained workspace access uses consumer-owned records and opaque engine references"},
	{pkg: "internal/nativecapture", forbidden: []string{"internal/store", "internal/memorystore", "internal/trajectory", "internal/server", "internal/runtime"}, why: "capture worker coordinates consumer-owned interfaces; adapters own persistence and source mechanisms"},
	{pkg: "internal/workspace", forbidden: []string{"internal/server", "internal/store", "internal/runtime", "internal/config", "internal/workspace/local"}, why: "workspace operations expose portable streams and metadata; provider paths and filesystem mechanisms belong to adapters"},
	{pkg: "internal/execution", forbidden: []string{"internal/server", "internal/store", "internal/runtime", "internal/config", "internal/workspace/local"}, why: "execution coordinates application ownership through consumer-owned operations, independent of HTTP, persistence implementations and substrate adapters"},
	{
		pkg: "internal/store",
		forbidden: []string{
			"internal/server",
			"internal/artifacts",
			"internal/bridge",
			"internal/runtime",
			"internal/workspace/local",
			"internal/driverstate",
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
		pkg: "internal/shareblob",
		forbidden: []string{
			"internal/store",
			"internal/memorystore",
			"internal/server",
			"internal/config",
			"internal/artifacts",
			"internal/runtime",
			"internal/sessionstate",
			"internal/agents",
		},
		why: "shareblob is the content-addressed byte store for public share links and must stay a " +
			"leaf: write bytes / read by digest / unlink, with no knowledge of sessions, links, or " +
			"who references what. Reference counting belongs with the table holding the references " +
			"(share_link_files), because only there can it be decided in the same transaction as " +
			"the delete -- a blob store that knew about links would invite deciding it somewhere " +
			"a concurrent link creation could race it.",
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

	// Process entry points open both databases and compose consumer-owned ports.
	// Neither capture orchestration nor its archive sink imports both stores.
	permitted := map[string]bool{"cmd/orchestrator": true, "cmd/native-capture": true}
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
	for _, pkg := range []string{"internal/execution", "internal/sandbox", "internal/sandbox/khora", "internal/workspace", "internal/egress"} {
		if bad := forbiddenApplicationDependency(modulePrefix+pkg, imports); bad != "" {
			t.Errorf("%s reaches forbidden substrate dependency %s", pkg, bad)
		}
	}
}

func TestApplicationPortGuardRejectsIndirectSubstrateAccess(t *testing.T) {
	root, helper := modulePrefix+"internal/execution", modulePrefix+"internal/helper"
	for _, forbidden := range []string{modulePrefix + "internal/runtime", "os/exec", "syscall", "golang.org/x/sys/unix", "github.com/TSHOGX/khora/internal/lifecycle"} {
		graph := map[string][]string{root: {helper}, helper: {forbidden}}
		if got := forbiddenApplicationDependency(root, func(pkg string) []string { return graph[pkg] }); got != forbidden {
			t.Fatalf("missed indirect substrate edge: %s", forbidden)
		}
	}
}
