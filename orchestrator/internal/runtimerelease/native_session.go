package runtimerelease

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NativeSessionVersion identifies the current tree-structured native history.
const NativeSessionVersion = 1
const NativeSessionPath = NativeRoot + "/" + PiPackage + "/dist/core/session-manager.js"
const nativeSessionOriginal = "sha256:b8dfa0f23c75146cf6037617ab1f3c51dcda301c2864750d44f8595daea516df"
const nativeSessionPatched = "sha256:3126e7e06689ed017e487c66a7778238e4af009b88b0d530c9f5c6aa219dd665"

const currentSessionValidation = `function assertCurrentSession(entries) {
    const header = entries[0];
    if (!header || header.type !== "session" || header.version !== CURRENT_SESSION_VERSION || typeof header.id !== "string") {
        throw new Error("Unsupported native session format; a fresh v1 session is required");
    }
    assertValidSessionId(header.id);
    for (const entry of entries.slice(1)) {
        if (entry.type === "session" || typeof entry.id !== "string" || !(entry.parentId === null || typeof entry.parentId === "string")) {
            throw new Error("Invalid native session entry");
        }
        if (entry.message?.role === "hookMessage" || "firstKeptEntryIndex" in entry) {
            throw new Error("Unsupported native session entry shape");
        }
    }
}
`

func patchNativeSession(raw []byte) ([]byte, error) {
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	if digest == nativeSessionPatched {
		return raw, nil
	}
	if digest != nativeSessionOriginal {
		return nil, errors.New("Pi Session source differs from pinned input")
	}
	source := string(raw)
	start := strings.Index(source, "/** Migrate v1")
	end := strings.Index(source, "/** Exported for compaction.test.ts */")
	if start < 0 || end <= start {
		return nil, errors.New("Pi Session patch boundary missing")
	}
	source = source[:start] + currentSessionValidation + source[end:]
	for _, replacement := range [][2]string{
		{`export const CURRENT_SESSION_VERSION = 3;`, "export const CURRENT_SESSION_VERSION = " + fmt.Sprint(NativeSessionVersion) + ";"},
		{`    return entries;
}
export function getLatestCompactionEntry`, `    assertCurrentSession(entries);
    return entries;
}
export function getLatestCompactionEntry`},
		{`    // Validate session header
    if (entries.length === 0)
        return entries;
    const header = entries[0];
    if (header.type !== "session" || typeof header.id !== "string") {
        return [];
    }
    return entries;`, `    assertCurrentSession(entries);
    return entries;`},
		{`header.type !== "session" || typeof header.id !== "string"`, `header.type !== "session" || header.version !== CURRENT_SESSION_VERSION || typeof header.id !== "string"`},
		{`if (header.type !== "session")`, `if (header.type !== "session" || header.version !== CURRENT_SESSION_VERSION)`},
		{`            this.sessionId = header?.id ?? createSessionId();
            if (migrateToCurrentVersion(this.fileEntries)) {
                this._rewriteFile();
            }`, `            this.sessionId = header.id;`},
		{`//# sourceMappingURL=session-manager.js.map`, `// Application native session v1; upstream source map does not apply.`},
		{`            // If file was empty or corrupted (no valid header), truncate and start fresh
            // to avoid appending messages without a session header (which breaks the session)
            if (this.fileEntries.length === 0) {
                const explicitPath = this.sessionFile;
                this.newSession();
                this.sessionFile = explicitPath;
                this._rewriteFile();
                this.flushed = true;
                return;
            }
`, ``},
	} {
		if !strings.Contains(source, replacement[0]) {
			return nil, errors.New("Pi Session patch marker differs from pinned source")
		}
		source = strings.ReplaceAll(source, replacement[0], replacement[1])
	}
	if fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(source))) != nativeSessionPatched {
		return nil, errors.New("Pi Session patch output differs from pin")
	}
	return []byte(source), nil
}

func patchNativeSessionFile(root string) error {
	file, err := resolve(root, NativeSessionPath)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	patched, err := patchNativeSession(raw)
	if err != nil {
		return err
	}
	if err = os.WriteFile(file, patched, 0644); err != nil {
		return err
	}
	for _, companion := range nativeSessionCompanions {
		file, err := resolve(root, NativeRoot+"/"+PiPackage+"/"+companion.suffix)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		patched, err := patchNativeSessionCompanion(raw, companion.original, companion.patched)
		if err != nil {
			return err
		}
		if err = os.WriteFile(file, patched, 0644); err != nil {
			return err
		}
	}
	// Source maps contain the replaced upstream implementation. Fresh runtime
	// images ship only the current code and declarations.
	for _, path := range []string{NativeRPCPath + ".map", NativeSessionPath + ".map", NativeRoot + "/" + PiPackage + "/dist/index.js.map", NativeRoot + "/" + PiPackage + "/dist/index.d.ts.map", NativeRoot + "/" + PiPackage + "/dist/core/session-manager.d.ts.map"} {
		file := filepath.Join(root, strings.TrimPrefix(path, "/"))
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
func verifyNativeSession(root string) error {
	file, err := resolve(root, NativeSessionPath)
	if err != nil {
		return err
	}
	digest, err := hashFile(file)
	if err != nil {
		return err
	}
	if digest != nativeSessionPatched {
		return errors.New("native session v1 implementation is absent or modified")
	}
	for _, companion := range nativeSessionCompanions {
		file, err := resolve(root, NativeRoot+"/"+PiPackage+"/"+companion.suffix)
		if err != nil {
			return err
		}
		digest, err := hashFile(file)
		if err != nil {
			return err
		}
		if digest != companion.patched {
			return errors.New("native session exports are absent or modified")
		}
	}
	return nil
}

// Public exports and declarations must agree with the single current session
// implementation. Each companion has independently pinned input/output bytes.
var nativeSessionCompanions = []struct{ suffix, fixture, original, patched string }{
	{"dist/index.js", "pi-index-0.77.0.js", "sha256:b0c72a74ebbd766c09517744a3777e93900a23995990fe57a3bb82fd884385ef", "sha256:d17cf27238b2bc355fa7847b24e4a451f1ca040ee2802898dedf3ad917b4483d"},
	{"dist/index.d.ts", "pi-index-0.77.0.d.ts", "sha256:bcaa7d54984b36fa3046464acfee97baa499dfd6ce65914f5585c1cd59f5a1f0", "sha256:62a7a28957547649a84112988a26577448fa78698b9094dea7683d5b09ef36b5"},
	{"dist/core/session-manager.d.ts", "pi-session-0.77.0.d.ts", "sha256:b8e098727215ba8fa7d3e3021225c71fc72febe4f6e06236e39a4a0e68cacada", "sha256:979045c6361d9e5df341767af89ee44644dfd51601c09d50ddd9de5dd3ea4f64"},
}

func patchNativeSessionCompanion(raw []byte, original, patched string) ([]byte, error) {
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	if digest == patched {
		return raw, nil
	}
	if digest != original {
		return nil, errors.New("Pi Session export source differs from pin")
	}
	source := strings.ReplaceAll(string(raw), "migrateSessionEntries, ", "")
	source = strings.ReplaceAll(source, "export declare const CURRENT_SESSION_VERSION = 3;", "export declare const CURRENT_SESSION_VERSION = "+fmt.Sprint(NativeSessionVersion)+";")
	source = strings.ReplaceAll(source, "    version?: number;", "    version: typeof CURRENT_SESSION_VERSION;")
	source = strings.ReplaceAll(source, "/** Exported for testing */\nexport declare function migrateSessionEntries(entries: FileEntry[]): void;\n", "")
	source = strings.ReplaceAll(source, "//# sourceMappingURL=index.js.map", "// Application native session v1 exports; upstream source map does not apply.")
	if fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(source))) != patched {
		return nil, errors.New("Pi Session export output differs from pin")
	}
	return []byte(source), nil
}
