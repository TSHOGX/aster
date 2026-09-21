package agentrunner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type processIdentity struct {
	pid, ppid    int
	start, state string
}

func processInfo(pid int) (processIdentity, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return processIdentity{}, err
	}
	close := strings.LastIndexByte(string(raw), ')')
	if close < 0 {
		return processIdentity{}, errors.New("invalid proc stat")
	}
	fields := strings.Fields(string(raw)[close+1:])
	if len(fields) < 20 {
		return processIdentity{}, errors.New("short proc stat")
	}
	parent, err := strconv.Atoi(fields[1])
	return processIdentity{pid: pid, ppid: parent, state: fields[0], start: fields[19]}, err
}
func descendants(root int) ([]processIdentity, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	all := map[int]processIdentity{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		p, err := processInfo(pid)
		if err == nil {
			all[pid] = p
		}
		if len(all) > 32768 {
			return nil, errors.New("process inspection bound exceeded")
		}
	}
	belongs := map[int]bool{root: true}
	var out []processIdentity
	for pass := 0; pass < len(all); pass++ {
		added := false
		for pid, p := range all {
			if !belongs[pid] && belongs[p.ppid] {
				belongs[pid] = true
				out = append(out, p)
				added = true
			}
		}
		if !added {
			break
		}
	}
	return out, nil
}
func becomeSubreaper() error { return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) }

// settleDescendants kills only children of this runner, including setsid orphans
// adopted by the subreaper. PID start time fences reuse; Pi may be retained between
// turns. Linux process ancestry is evidence, never a sandbox-wide kill selector.
func settleDescendants(pi int, retainPi bool) error {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		list, err := descendants(os.Getpid())
		if err != nil {
			return err
		}
		live := false
		for _, p := range list {
			if retainPi && p.pid == pi {
				continue
			}
			if p.state == "Z" {
				if p.pid != pi {
					var status syscall.WaitStatus
					_, _ = syscall.Wait4(p.pid, &status, syscall.WNOHANG, nil)
				}
				continue
			}
			current, err := processInfo(p.pid)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if current.start != p.start {
				continue
			}
			live = true
			if err = syscall.Kill(p.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
		}
		if !live {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("runner descendants remain unresolved")
}
func validIdentity(s string) bool {
	return s != "" && len(s) <= 128 && s != "." && s != ".." && filepath.Base(s) == s && !strings.ContainsAny(s, "/\\\x00")
}
