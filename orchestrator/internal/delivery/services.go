package delivery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func unitQuote(s string) string { return strconv.Quote(strings.ReplaceAll(s, "%", "%%")) }

// InstallUnits writes only this instance's previously absent unit definitions.
// Existing installations must already point to Current and runtime.env: their
// service policies are never overwritten by an application release.
func (e Engine) InstallUnits(ctx context.Context) error {
	i := e.Target
	env := filepath.Join(filepath.Dir(i.Config), "runtime.env")
	units := map[string]string{
		i.BackendService:  fmt.Sprintf("[Unit]\nDescription=Harness orchestrator %s\nAfter=network-online.target\n[Service]\nType=simple\nUser=root\nEnvironmentFile=%s\nWorkingDirectory=%s\nExecStart=%s --config %s\nKillMode=control-group\nTimeoutStopSec=120\nRestart=on-failure\n[Install]\nWantedBy=multi-user.target\n", i.ID(), unitQuote(env), unitQuote(filepath.Join(i.Current, "backend")), unitQuote(filepath.Join(i.Current, "backend/bin/orchestrator")), unitQuote(i.Config)),
		i.FrontendService: fmt.Sprintf("[Unit]\nDescription=Harness frontend %s\nAfter=network-online.target\n[Service]\nType=simple\nUser=%s\nEnvironmentFile=%s\nWorkingDirectory=%s\nExecStart=%s %s\nKillMode=control-group\nTimeoutStopSec=120\nRestart=on-failure\n[Install]\nWantedBy=multi-user.target\n", i.ID(), i.FrontendUser, unitQuote(env), unitQuote(filepath.Join(i.Current, "frontend")), unitQuote(filepath.Join(i.Current, "frontend/node/bin/node")), unitQuote(filepath.Join(i.Current, "frontend/server.js"))),
	}
	for name, body := range units {
		path := filepath.Join("/etc/systemd/system", name)
		if existing, err := os.ReadFile(path); err == nil {
			if string(existing) != body {
				return fmt.Errorf("existing unit %s differs; review adoption before deploy", name)
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := AtomicWrite(path, []byte(body), 0644); err != nil {
			return err
		}
	}
	_, err := e.run(ctx, "systemctl", "daemon-reload")
	return err
}
