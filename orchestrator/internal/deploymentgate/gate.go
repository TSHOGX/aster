// Package deploymentgate shares a host-controlled drain across HTTP and
// background producers. File errors fail closed; no remote unauthenticated
// endpoint can initiate or cancel a deployment.
package deploymentgate

import (
	"os"
	"sync/atomic"
)

var active atomic.Int64

func present(key string) bool {
	p := os.Getenv(key)
	if p == "" {
		return false
	}
	_, e := os.Stat(p)
	return !os.IsNotExist(e)
}
func Draining() bool       { return present("HARNESS_DRAIN_FILE") }
func BackgroundHeld() bool { return present("HARNESS_BACKGROUND_HOLD_FILE") }
func Paused() bool         { return Draining() || BackgroundHeld() }
func Active() int64        { return active.Load() }
func Run(fn func()) {
	if Paused() {
		return
	}
	active.Add(1)
	defer active.Add(-1)
	fn()
}
