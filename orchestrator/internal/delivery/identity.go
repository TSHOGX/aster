package delivery

import (
	"context"
	"debug/buildinfo"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type SourceIdentity struct {
	PID             int    `json:"pid"`
	BinaryPath      string `json:"binary_path"`
	BinarySHA256    string `json:"binary_sha256"`
	VCSRevision     string `json:"vcs_revision"`
	VCSModified     string `json:"vcs_modified"`
	VCSTime         string `json:"vcs_time"`
	FrontendBuildID string `json:"frontend_build_id"`
}

func InspectSource(ctx context.Context, i Target, r Runner) (SourceIdentity, error) {
	var s SourceIdentity
	out, err := r.Run(ctx, []string{"systemctl", "show", i.BackendService, "-p", "MainPID", "--value"}, nil)
	if err != nil {
		return s, err
	}
	s.PID, err = strconv.Atoi(strings.TrimSpace(out))
	if err != nil || s.PID <= 1 {
		return s, fmt.Errorf("backend is not running; cannot identify its actual executable")
	}
	exe := fmt.Sprintf("/proc/%d/exe", s.PID)
	s.BinaryPath, err = os.Readlink(exe)
	if err != nil {
		return s, err
	}
	s.BinarySHA256, err = HashFile(exe)
	if err != nil {
		return s, err
	}
	if info, err := buildinfo.ReadFile(exe); err == nil {
		for _, v := range info.Settings {
			switch v.Key {
			case "vcs.revision":
				s.VCSRevision = v.Value
			case "vcs.modified":
				s.VCSModified = v.Value
			case "vcs.time":
				s.VCSTime = v.Value
			}
		}
	}
	if i.FrontendBuildIDFile != "" {
		b, err := os.ReadFile(i.FrontendBuildIDFile)
		if err != nil {
			return s, err
		}
		s.FrontendBuildID = strings.TrimSpace(string(b))
	}
	return s, nil
}
func CheckSource(ctx context.Context, i Target, r Runner) error {
	s, err := InspectSource(ctx, i, r)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(i.Current, "release-manifest.json")); err == nil {
		h, err := HashFile(filepath.Join(i.Current, "backend/bin/orchestrator"))
		if err != nil {
			return err
		}
		if h != s.BinarySHA256 {
			return fmt.Errorf("running executable differs from current release")
		}
		return nil
	}
	if !validHash(i.LegacyBinarySHA256) || s.BinarySHA256 != i.LegacyBinarySHA256 {
		return fmt.Errorf("legacy/dirty backend requires its actual running executable SHA256 recorded as legacy_binary_sha256; source HEAD is not deployment identity")
	}
	return nil
}
