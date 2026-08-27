package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Reclaiming a sandbox whose control socket wedged.
//
// A memcg OOM driven by shmem kills only the workload inside the sandbox; the
// three host processes (`runsc run`, `runsc-gofer`, `runsc-sandbox` boot) survive
// with a control socket that still accepts connections but never answers. Every
// runsc CLI call naming that container then blocks until its own timeout —
// `kill`, `delete -force`, even `list`, which is why a hanging `list` does not
// mean the runsc root is broken.
//
// Nothing in the runsc CLI recovers from this, because every subcommand talks to
// the socket that stopped answering. The only thing that works is SIGKILL on the
// host processes: killing the boot process tears the gofer and `run` down with
// it, after which `delete -force` returns immediately and the container is gone.
//
// This is the codified form of the manual remediation from the 2026-08-19
// incident (and the 2026-08-16 one before it, which cost 34h). See
// docs/proposals/runsc-cleanup-hang-blocks-maintenance.md.

// sandboxRemnant is one surviving host process of a wedged sandbox.
type sandboxRemnant struct {
	PID  int
	Kind string // "boot", "gofer", or "run"
}

// killSandboxRemnants SIGKILLs the host processes left behind by containerID.
//
// Ordering matters: the boot process goes first because the gofer and `run` exit
// on their own once it is gone, which avoids leaving a gofer briefly holding the
// bundle's mounts. The remaining PIDs are still signalled afterwards in case
// they did not follow, and an already-exited PID is not an error.
//
// Returns the remnants it signalled. An empty slice means there was nothing left
// to kill, which is the normal case for a healthy teardown.
func (r *Runtime) killSandboxRemnants(containerID string) ([]sandboxRemnant, error) {
	remnants, err := findSandboxRemnants(containerID)
	if err != nil {
		return nil, err
	}
	if len(remnants) == 0 {
		return nil, nil
	}
	// boot first, then gofer, then run.
	sort.SliceStable(remnants, func(i, j int) bool {
		return remnantKillOrder(remnants[i].Kind) < remnantKillOrder(remnants[j].Kind)
	})
	killed := make([]sandboxRemnant, 0, len(remnants))
	for _, remnant := range remnants {
		// ESRCH means it already exited — expected once boot has been killed.
		if err := syscall.Kill(remnant.PID, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return killed, fmt.Errorf("sigkill %s pid %d: %w", remnant.Kind, remnant.PID, err)
		}
		killed = append(killed, remnant)
	}
	return killed, nil
}

func remnantKillOrder(kind string) int {
	switch kind {
	case "boot":
		return 0
	case "gofer":
		return 1
	default:
		return 2
	}
}

// findSandboxRemnants scans /proc for the host processes belonging to
// containerID.
//
// Matching is deliberately per-process-kind rather than one substring test,
// because the three processes name the container differently and a naive match
// misses one of them:
//
//   - `runsc run` and `runsc-sandbox boot` carry the container ID as an argument.
//   - `runsc-gofer` does NOT. It names the generation only inside
//     `--bundle=.../gen-<generation-id>`, so matching on the container ID alone
//     silently skips the gofer.
//
// The bundle path is what ties the gofer back to the container: the container ID
// is "harness-gen-<generation-id>" and the bundle dir is ".../gen-<generation-id>",
// so the generation ID recovered from the container ID identifies both.
func findSandboxRemnants(containerID string) ([]sandboxRemnant, error) {
	containerID = strings.TrimSpace(containerID)
	if containerID == "" {
		return nil, fmt.Errorf("runsc container id is required")
	}
	generationID := strings.TrimPrefix(containerID, "harness-gen-")
	// Anchored with the path separator so gen-<id> cannot match a longer id that
	// merely starts with the same characters.
	bundleNeedle := "/gen-" + generationID

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("read /proc: %w", err)
	}
	var remnants []sandboxRemnant
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		// A process that exits mid-scan is not an error; skip it.
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if kind, ok := classifySandboxRemnant(args, containerID, bundleNeedle); ok {
			remnants = append(remnants, sandboxRemnant{PID: pid, Kind: kind})
		}
	}
	return remnants, nil
}

// classifySandboxRemnant decides whether a process belongs to the container, and
// which of the three roles it plays.
//
// Both a container-ID match and a bundle-path match are required to be about the
// same sandbox, so an unrelated runsc process for another generation is never
// touched. Only runsc processes are considered at all: without that guard, an
// unrelated process that merely mentions the generation ID on its command line
// (a shell, a grep, a log tailer) would be a SIGKILL target.
func classifySandboxRemnant(args []string, containerID, bundleNeedle string) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	exe := filepath.Base(args[0])
	if !strings.HasPrefix(exe, "runsc") {
		return "", false
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, containerID) && !strings.Contains(joined, bundleNeedle) {
		return "", false
	}
	switch {
	case exe == "runsc-gofer" || hasArg(args, "gofer"):
		// The gofer never carries the container ID, so it must be identified by
		// its bundle path alone.
		if !strings.Contains(joined, bundleNeedle) {
			return "", false
		}
		return "gofer", true
	case exe == "runsc-sandbox" || hasArg(args, "boot"):
		return "boot", true
	case hasArg(args, "run"):
		return "run", true
	}
	return "", false
}

func hasArg(args []string, want string) bool {
	for _, arg := range args[1:] {
		if arg == want {
			return true
		}
	}
	return false
}

// sandboxOOMKilled reports whether the sandbox's memcg recorded an OOM kill.
//
// Read from cgroup v2's memory.events (`oom_kill` counter) rather than parsed out
// of dmesg: the counter is per-sandbox, needs no privileges beyond reading
// sysfs, and cannot be lost to a ring-buffer wrap. It stays readable for as long
// as the cgroup directory exists, which outlives the killed workload.
//
// ok=false means the answer is unknown (cgroup already gone, cgroup v1 host, or
// unreadable) and callers must not report an OOM on that basis.
func sandboxOOMKilled(containerID string) (killed bool, ok bool) {
	containerID = strings.TrimSpace(containerID)
	if containerID == "" {
		return false, false
	}
	raw, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", containerID, "memory.events"))
	if err != nil {
		return false, false
	}
	return parseMemoryEventsOOMKill(string(raw))
}

// parseMemoryEventsOOMKill pulls the oom_kill counter out of a cgroup v2
// memory.events body. Split out from the file read so the parsing is testable
// without an absolute /sys path.
func parseMemoryEventsOOMKill(contents string) (killed bool, ok bool) {
	for _, line := range strings.Split(contents, "\n") {
		field, value, found := strings.Cut(strings.TrimSpace(line), " ")
		if !found || field != "oom_kill" {
			continue
		}
		count, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return false, false
		}
		return count > 0, true
	}
	return false, false
}

// SandboxOOMKilled reports whether the sandbox behind containerID was OOM-killed,
// for callers deciding how to describe a dead generation to the user.
func (r *Runtime) SandboxOOMKilled(ctx context.Context, containerID string) (killed bool, ok bool) {
	return sandboxOOMKilled(containerID)
}

// SandboxRemnantsGone reports whether every host process for containerID is gone.
//
// This is the safety gate for releasing a generation whose teardown failed: the
// DB row may only be marked terminal once nothing can still be writing to the
// sandbox's overlay upperdir, because a replacement generation will mount that
// same upperdir and nothing else enforces single-attach.
func (r *Runtime) SandboxRemnantsGone(ctx context.Context, containerID string) (bool, error) {
	remnants, err := findSandboxRemnants(containerID)
	if err != nil {
		return false, err
	}
	return len(remnants) == 0, nil
}

// SandboxMemoryUsage reports the sandbox's current memcg usage and its ceiling.
//
// Read from cgroup v2 (memory.current / memory.max) for the same reasons as the
// oom_kill counter: per-sandbox, no privileges beyond sysfs, no parsing of kernel
// log text. ok=false means the answer is unavailable (cgroup gone, cgroup v1, or
// an unlimited "max" ceiling), in which case callers must not infer pressure.
func (r *Runtime) SandboxMemoryUsage(ctx context.Context, containerID string) (used int64, limit int64, ok bool) {
	containerID = strings.TrimSpace(containerID)
	if containerID == "" {
		return 0, 0, false
	}
	dir := filepath.Join("/sys/fs/cgroup", containerID)
	used, usedOK := readCgroupInt64(filepath.Join(dir, "memory.current"))
	limit, limitOK := readCgroupInt64(filepath.Join(dir, "memory.max"))
	if !usedOK || !limitOK || limit <= 0 {
		return 0, 0, false
	}
	return used, limit, true
}

// readCgroupInt64 reads a single-value cgroup file. The literal "max" (an
// unlimited ceiling) is reported as unavailable rather than as a number, because
// there is no threshold to compare against.
func readCgroupInt64(path string) (int64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	text := strings.TrimSpace(string(raw))
	if text == "max" {
		return 0, false
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}
