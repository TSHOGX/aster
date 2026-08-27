package runtime

import (
	"testing"
)

// The gofer is the reason this classifier exists: it is the one remnant that
// never carries the container ID, so a naive substring match on the container ID
// leaves it running. Killing boot without the gofer was the shape that made the
// manual remediation fiddly during the 2026-08-19 incident.
func TestClassifySandboxRemnantMatchesAllThreeProcessKinds(t *testing.T) {
	const containerID = "harness-gen-gen_f152a2e0-7a27-4761-82e3-b005f1da02c4"
	const bundleNeedle = "/gen-gen_f152a2e0-7a27-4761-82e3-b005f1da02c4"

	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "run carries the container id",
			args: []string{"/usr/local/bin/runsc", "-root", "/var/lib/harness-publish/runsc", "run", "-bundle", "/var/lib/harness-publish/run/runtime/gen-gen_f152a2e0-7a27-4761-82e3-b005f1da02c4", containerID},
			want: "run",
		},
		{
			name: "boot carries the container id",
			args: []string{"runsc-sandbox", "--root=/var/lib/harness-publish/runsc", "boot", "--bundle=/var/lib/harness-publish/run/runtime/gen-gen_f152a2e0-7a27-4761-82e3-b005f1da02c4", containerID},
			want: "boot",
		},
		{
			name: "gofer is identified by bundle path alone",
			args: []string{"runsc-gofer", "--root=/var/lib/harness-publish/runsc", "gofer", "--bundle=/var/lib/harness-publish/run/runtime/gen-gen_f152a2e0-7a27-4761-82e3-b005f1da02c4", "--io-fds=6"},
			want: "gofer",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, ok := classifySandboxRemnant(tc.args, containerID, bundleNeedle)
			if !ok {
				t.Fatalf("expected a match for %v", tc.args)
			}
			if kind != tc.want {
				t.Fatalf("kind = %q, want %q", kind, tc.want)
			}
		})
	}
}

// SIGKILL targets must be narrow. A process that merely mentions the generation
// on its command line — a shell, a grep, a log tailer — must never be a target,
// and neither must another generation's sandbox.
func TestClassifySandboxRemnantRejectsUnrelatedProcesses(t *testing.T) {
	const containerID = "harness-gen-gen_f152a2e0-7a27-4761-82e3-b005f1da02c4"
	const bundleNeedle = "/gen-gen_f152a2e0-7a27-4761-82e3-b005f1da02c4"

	cases := []struct {
		name string
		args []string
	}{
		{
			name: "non-runsc process naming the generation",
			args: []string{"/usr/bin/grep", "-r", containerID, "/var/log"},
		},
		{
			name: "shell holding the bundle path",
			args: []string{"/bin/bash", "-c", "ls /var/lib/harness-publish/run/runtime/gen-gen_f152a2e0-7a27-4761-82e3-b005f1da02c4"},
		},
		{
			name: "another generation's boot process",
			args: []string{"runsc-sandbox", "boot", "--bundle=/var/lib/harness-publish/run/runtime/gen-gen_dc527345-6bbc-40ac-b721-fdd89b1a92c2", "harness-gen-gen_dc527345-6bbc-40ac-b721-fdd89b1a92c2"},
		},
		{
			name: "another generation's gofer",
			args: []string{"runsc-gofer", "gofer", "--bundle=/var/lib/harness-publish/run/runtime/gen-gen_dc527345-6bbc-40ac-b721-fdd89b1a92c2"},
		},
		{
			name: "empty command line",
			args: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if kind, ok := classifySandboxRemnant(tc.args, containerID, bundleNeedle); ok {
				t.Fatalf("expected no match, got kind %q for %v", kind, tc.args)
			}
		})
	}
}

// boot must be signalled first so the gofer and run exit with it, rather than
// leaving a gofer briefly holding the bundle's mounts.
func TestRemnantKillOrderPutsBootFirst(t *testing.T) {
	if remnantKillOrder("boot") >= remnantKillOrder("gofer") {
		t.Fatal("boot must be killed before the gofer")
	}
	if remnantKillOrder("gofer") >= remnantKillOrder("run") {
		t.Fatal("gofer must be killed before run")
	}
}

// A gofer whose bundle belongs to a different generation must not be attributed
// to this container even though its other flags look identical. This is the case
// a bundle-path check exists to catch.
func TestFindSandboxRemnantsRequiresContainerID(t *testing.T) {
	if _, err := findSandboxRemnants(""); err == nil {
		t.Fatal("expected an error for an empty container id")
	}
}

func TestSandboxOOMKilledReadsCgroupCounter(t *testing.T) {
	// sandboxOOMKilled reads an absolute /sys path, so a temp dir cannot stand in
	// for the cgroup. Exercise the parser through the same file format instead.
	cases := []struct {
		name       string
		contents   string
		wantKilled bool
		wantOK     bool
	}{
		{
			name:       "oom_kill greater than zero",
			contents:   "low 0\nhigh 3\nmax 12\noom 1\noom_kill 1\noom_group_kill 0\n",
			wantKilled: true,
			wantOK:     true,
		},
		{
			name:       "healthy sandbox",
			contents:   "low 0\nhigh 0\nmax 0\noom 0\noom_kill 0\noom_group_kill 0\n",
			wantKilled: false,
			wantOK:     true,
		},
		{
			name:     "counter absent",
			contents: "low 0\nhigh 0\n",
			wantOK:   false,
		},
		{
			name:     "unparseable counter",
			contents: "oom_kill not-a-number\n",
			wantOK:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			killed, ok := parseMemoryEventsOOMKill(tc.contents)
			if ok != tc.wantOK || killed != tc.wantKilled {
				t.Fatalf("killed=%v ok=%v, want killed=%v ok=%v", killed, ok, tc.wantKilled, tc.wantOK)
			}
		})
	}
}

// An absent cgroup must read as "unknown", never as "not OOM-killed": reporting a
// wrong cause to the user is worse than admitting we do not know.
func TestSandboxOOMKilledUnknownWhenCgroupMissing(t *testing.T) {
	if killed, ok := sandboxOOMKilled("harness-gen-gen_does-not-exist"); ok || killed {
		t.Fatalf("missing cgroup should be unknown, got killed=%v ok=%v", killed, ok)
	}
	if killed, ok := sandboxOOMKilled(""); ok || killed {
		t.Fatalf("empty container id should be unknown, got killed=%v ok=%v", killed, ok)
	}
}
