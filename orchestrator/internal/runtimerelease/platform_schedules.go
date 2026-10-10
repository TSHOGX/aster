package runtimerelease

import (
	"bytes"
	_ "embed"
	"errors"
	"os"
	"path/filepath"
)

const ScheduleProtocol = 1
const PlatformExtensionPath = "/usr/local/share/project-runtime/platform-schedules.mjs"

//go:embed platform-schedules.mjs
var platformSchedules []byte

func InstallPlatformSchedules(root string) error {
	if root == "/" || !filepath.IsAbs(root) {
		return errors.New("separate absolute build root required")
	}
	directory := filepath.Join(root, filepath.Dir(PlatformExtensionPath))
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	directory, err := resolve(root, filepath.Dir(PlatformExtensionPath))
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(directory, filepath.Base(PlatformExtensionPath)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, err = file.Write(platformSchedules)
	return errors.Join(err, file.Close())
}
func verifyPlatformSchedules(root string) error {
	file, err := resolve(root, PlatformExtensionPath)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, platformSchedules) {
		return errors.New("runtime scheduling extension differs from reviewed source")
	}
	return nil
}
func dataComponent(component string) bool {
	return component == "/usr/local/share/project-runtime/system.md" || component == PlatformExtensionPath
}
