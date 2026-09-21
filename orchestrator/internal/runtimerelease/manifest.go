// Package runtimerelease identifies the immutable application runtime installed
// in a Khora template. It has no product database or lifecycle responsibility.
package runtimerelease

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"harness-platform/orchestrator/internal/agentwire"
)

const EngineModule = "v0.0.2-0.20260920024301-e8cd021a9c7c"
const EngineRevision = "e8cd021a9c7c"
const PiPackage = "@earendil-works/pi-coding-agent"
const PiVersion = "0.77.0"
const PiIntegrity = "sha512-huS+k+dhQRR9PlTK7crLfeSRUw3a96V6JYfP0ZH3Zkko/m10gsYk8dKQmwScSy5Dll516pXorz19BURfD6S2qQ=="
const NodeVersion = "24.15.0"
const ManifestPath = "/etc/project-runtime/manifest.json"
const NativeRoot = "/usr/local/lib/node_modules"

type Manifest struct {
	Version        int               `json:"version"`
	Platform       string            `json:"platform"`
	EngineModule   string            `json:"engine_module"`
	EngineRevision string            `json:"engine_revision"`
	AgentProtocol  int               `json:"agent_protocol"`
	Workspace      string            `json:"workspace"`
	RuntimeState   string            `json:"runtime_state"`
	PiPackage      string            `json:"pi_package"`
	PiVersion      string            `json:"pi_version"`
	PiIntegrity    string            `json:"pi_integrity"`
	NodeVersion    string            `json:"node_version"`
	NativeTree     string            `json:"native_tree"`
	Files          map[string]string `json:"files"`
}

var Components = []string{"/usr/sbin/sandboxd", "/usr/local/bin/agent-runner", "/usr/local/bin/workspace-files", "/usr/local/bin/runtime-cleanup", "/usr/local/bin/runtime-verify", "/usr/local/bin/pi", "/opt/node-v" + NodeVersion + "/bin/node", "/usr/local/share/project-runtime/system.md"}

func ValidDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}
func Bytes(manifest Manifest) ([]byte, error) { return agentwire.MarshalJSON(manifest) }
func Digest(raw []byte) (string, error) {
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", err
	}
	canonical, err := Bytes(manifest)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return fmt.Sprintf("sha256:%x", sum), nil
}
func Load(reader io.Reader, expected string) (Manifest, error) {
	var manifest Manifest
	raw, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil {
		return manifest, err
	}
	if len(raw) > 1<<20 {
		return manifest, errors.New("runtime manifest exceeds bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&manifest); err != nil {
		return manifest, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return manifest, errors.New("trailing manifest data")
	}
	digest, err := Digest(raw)
	if err != nil || !ValidDigest(expected) || digest != expected {
		return manifest, errors.New("runtime manifest does not match its release digest")
	}
	if manifest.Version != 1 || manifest.EngineModule != EngineModule || manifest.EngineRevision != EngineRevision || manifest.AgentProtocol != agentwire.Version || manifest.Workspace != "/workspace" || manifest.RuntimeState != "/runtime" || manifest.PiPackage != PiPackage || manifest.PiVersion != PiVersion || manifest.PiIntegrity != PiIntegrity || manifest.NodeVersion != NodeVersion || !ValidDigest(manifest.NativeTree) {
		return manifest, errors.New("incompatible Project runtime release")
	}
	if manifest.Platform != "linux/amd64" && manifest.Platform != "linux/arm64" {
		return manifest, errors.New("unsupported runtime platform")
	}
	if len(manifest.Files) != len(Components) {
		return manifest, errors.New("unexpected runtime component set")
	}
	for _, component := range Components {
		if !ValidDigest(manifest.Files[component]) {
			return manifest, fmt.Errorf("missing component digest: %s", component)
		}
	}
	return manifest, nil
}

// resolve interprets absolute symlinks inside the guest root, not the build host.
func resolve(root, guest string) (string, error) {
	if !path.IsAbs(guest) || path.Clean(guest) != guest {
		return "", errors.New("clean absolute guest path required")
	}
	pending := strings.Split(strings.TrimPrefix(guest, "/"), "/")
	var selected []string
	links := 0
	for len(pending) > 0 {
		component := pending[0]
		pending = pending[1:]
		if component == "" || component == "." {
			continue
		}
		if component == ".." {
			if len(selected) == 0 {
				return "", errors.New("guest path escapes template root")
			}
			selected = selected[:len(selected)-1]
			continue
		}
		candidate := filepath.Join(append([]string{root}, append(selected, component)...)...)
		info, err := os.Lstat(candidate)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 40 {
				return "", errors.New("guest symlink resolution bound exceeded")
			}
			target, err := os.Readlink(candidate)
			if err != nil {
				return "", err
			}
			if path.IsAbs(target) {
				selected = nil
			}
			pending = append(strings.Split(target, "/"), pending...)
			continue
		}
		selected = append(selected, component)
	}
	return filepath.Join(append([]string{root}, selected...)...), nil
}
func hashFile(file string) (string, error) {
	opened, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer opened.Close()
	info, err := opened.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("runtime component is not regular")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, opened); err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
}
func nativeTree(root string) (string, error) {
	native, err := resolve(root, NativeRoot)
	if err != nil {
		return "", err
	}
	var entries []string
	err = filepath.WalkDir(native, func(file string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if file == native {
			return nil
		}
		if len(entries) >= 100000 {
			return errors.New("native runtime tree exceeds entry bound")
		}
		relative, err := filepath.Rel(native, file)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(file)
			if err != nil {
				return err
			}
			resolved, err := resolve(root, path.Join(NativeRoot, filepath.ToSlash(relative)))
			if err != nil {
				return err
			}
			within, err := filepath.Rel(native, resolved)
			if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
				return errors.New("native dependency symlink escapes hashed tree")
			}
			entries = append(entries, filepath.ToSlash(relative)+"\x00link\x00"+target)
			return nil
		}

		digest, err := hashFile(file)
		if err != nil {
			return err
		}
		entries = append(entries, filepath.ToSlash(relative)+"\x00file\x00"+digest)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(entries)
	hash := sha256.New()
	for _, entry := range entries {
		hash.Write([]byte(entry))
		hash.Write([]byte{0})
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
}
func Generate(root string) (Manifest, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Manifest{}, err
	}
	if root == "/" {
		return Manifest{}, errors.New("release generation requires a separate build root")
	}
	if err := cleanRoot(root); err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{Version: 1, EngineModule: EngineModule, EngineRevision: EngineRevision, AgentProtocol: agentwire.Version, Workspace: "/workspace", RuntimeState: "/runtime", PiPackage: PiPackage, PiVersion: PiVersion, PiIntegrity: PiIntegrity, NodeVersion: NodeVersion, Files: map[string]string{}}
	packagePath, err := resolve(root, NativeRoot+"/"+PiPackage+"/package.json")
	if err != nil {
		return manifest, err
	}
	raw, err := os.ReadFile(packagePath)
	if err != nil {
		return manifest, err
	}
	var pkg struct{ Name, Version string }
	if err = json.Unmarshal(raw, &pkg); err != nil {
		return manifest, err
	}
	if pkg.Name != PiPackage || pkg.Version != PiVersion {
		return manifest, errors.New("native Pi package differs from release pin")
	}
	typesPath, err := resolve(root, NativeRoot+"/"+PiPackage+"/dist/modes/rpc/rpc-types.d.ts")
	if err != nil {
		return manifest, err
	}
	types, err := os.ReadFile(typesPath)
	if err != nil {
		return manifest, err
	}
	if !regexp.MustCompile(`type:\s*"abort"`).Match(types) || !regexp.MustCompile(`type:\s*"steer";\s*message:\s*string`).Match(types) {
		return manifest, errors.New("native Pi RPC control contract differs from release")
	}
	for _, component := range Components {
		file, err := resolve(root, component)
		if err != nil {
			return manifest, err
		}
		if component != "/usr/local/share/project-runtime/system.md" {
			info, e := os.Stat(file)
			if e != nil {
				return manifest, e
			}
			if info.Mode().Perm()&0111 == 0 {
				return manifest, fmt.Errorf("runtime executable lacks execute permission: %s", component)
			}
		}
		digest, err := hashFile(file)
		if err != nil {
			return manifest, err
		}
		manifest.Files[component] = digest
		if component != "/usr/local/bin/pi" && component != "/usr/local/share/project-runtime/system.md" {
			var platform string
			binary, err := elf.Open(file)
			if err != nil {
				return manifest, err
			}
			switch binary.Machine {
			case elf.EM_X86_64:
				platform = "linux/amd64"
			case elf.EM_AARCH64:
				platform = "linux/arm64"
			default:
				binary.Close()
				return manifest, errors.New("unsupported runtime binary architecture")
			}
			binary.Close()
			if manifest.Platform != "" && manifest.Platform != platform {
				return manifest, errors.New("runtime binaries have mixed architectures")
			}
			manifest.Platform = platform
		}
	}
	manifest.NativeTree, err = nativeTree(root)
	return manifest, err
}
func Verify(root, expected string) error {
	file, err := resolve(root, ManifestPath)
	if err != nil {
		return err
	}
	opened, err := os.Open(file)
	if err != nil {
		return err
	}
	manifest, err := Load(opened, expected)
	opened.Close()
	if err != nil {
		return err
	}
	for _, component := range Components {
		file, err := resolve(root, component)
		if err != nil {
			return err
		}
		if component != "/usr/local/share/project-runtime/system.md" {
			info, e := os.Stat(file)
			if e != nil {
				return e
			}
			if info.Mode().Perm()&0111 == 0 {
				return fmt.Errorf("runtime executable lacks execute permission: %s", component)
			}
		}
		digest, err := hashFile(file)
		if err != nil {
			return err
		}
		if digest != manifest.Files[component] {
			return fmt.Errorf("runtime component differs from manifest: %s", component)
		}
	}
	digest, err := nativeTree(root)
	if err != nil {
		return err
	}
	if digest != manifest.NativeTree {
		return errors.New("native dependency tree differs from release")
	}
	return nil
}

func cleanRoot(root string) error {
	// These names are rejection guards, not compatibility execution paths.
	for _, name := range []string{"etc/harness-image", "harness-bridge", "harness-config", "harness-secrets", "sessions", "root/.pi", "root/.agents", "root/.ssh", "usr/local/share/harness", "usr/local/bin/harness-khora-launcher", "usr/local/bin/harness-bridge-client", "usr/local/bin/harness-workspace-helper", "usr/local/bin/harness-journal-replay", "usr/local/lib/harness_launch.py", "usr/local/lib/harness_stream.py", "usr/local/lib/harness_files.py", "usr/local/lib/harness_recovery.py", "usr/local/lib/harness_manifest.py"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			return fmt.Errorf("template contains retired application state: %s", name)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	for _, name := range []string{"workspace", "runtime"} {
		entries, err := os.ReadDir(filepath.Join(root, name))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if len(entries) > 0 {
			return fmt.Errorf("template contains user runtime data: %s", name)
		}
	}
	return nil
}
