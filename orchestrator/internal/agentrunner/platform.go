package agentrunner

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"

	"harness-platform/orchestrator/internal/runtimerelease"
)

// materializePlatform writes credentials outside native history. Only an
// explicitly enabled and measured extension receives this runtime configuration.
func materializePlatform(root string, c Config) (string, error) {
	file := filepath.Join(root, "platform-schedules.json")
	if c.PlatformEndpoint == "" {
		if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		return "", nil
	}
	endpoint, err := url.Parse(c.PlatformEndpoint)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Path != "/platform/v1/schedules" || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.User != nil || c.ServiceCredential == "" {
		return "", errors.New("invalid scheduling configuration")
	}
	info, err := os.Stat(runtimerelease.PlatformExtensionPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("scheduling extension unavailable")
	}
	raw, err := json.Marshal(map[string]string{"endpoint": endpoint.String(), "credential": c.ServiceCredential, "session_id": c.Session})
	if err != nil {
		return "", err
	}
	temp := file + ".tmp"
	if err = os.WriteFile(temp, raw, 0600); err != nil {
		return "", err
	}
	if err = os.Rename(temp, file); err != nil {
		os.Remove(temp)
		return "", err
	}
	return file, nil
}
