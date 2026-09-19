// Package sandbox defines the execution mechanism consumed by application
// services. It carries opaque engine identities, never product authorization.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

type ErrorClass string

const Backpressure ErrorClass = "backpressure"

const (
	Invalid         ErrorClass = "invalid"
	NotFound        ErrorClass = "not_found"
	Conflict        ErrorClass = "conflict"
	Precondition    ErrorClass = "precondition"
	Capacity        ErrorClass = "capacity"
	Unavailable     ErrorClass = "unavailable"
	IdentityChanged ErrorClass = "identity_changed"
	Incompatible    ErrorClass = "incompatible"
	Uncertain       ErrorClass = "uncertain"
	CursorExpired   ErrorClass = "cursor_expired"
)

type Error struct {
	Class     ErrorClass
	Operation string
	Cause     error
}

func (e *Error) Error() string {
	return fmt.Sprintf("sandbox %s: %s: %v", e.Operation, e.Class, e.Cause)
}
func (e *Error) Unwrap() error { return e.Cause }
func Is(err error, class ErrorClass) bool {
	var result *Error
	return errors.As(err, &result) && result.Class == class
}

type Identity struct {
	StoreID, Version   string
	ManualColdRecovery bool
}
type Reference struct {
	StoreID, SandboxID, MutationRevision string
	Epoch                                uint64
}
type Observation struct {
	Reference
	State, PlacementState, DeathCause string
	DiskBytes                         *int64
	// CPU and memory measurements remain unknown when the engine cannot report them.
	CPU, Memory *int64
}

type Mount struct{ VolumeID, Destination, Access string }
type Tuple struct {
	CIDR     netip.Prefix
	Port     uint16
	Protocol string
}
type Route struct {
	Match    Tuple
	Endpoint netip.AddrPort
}
type Network struct {
	Allow []Tuple
	Via   []Route
}
type CreateRequest struct {
	IdempotencyKey, TemplateID        string
	CPUMillis, MemoryBytes, DiskBytes int64
	Mounts                            []Mount
	Network                           Network
}

type Lifecycle interface {
	Identity(context.Context) (Identity, error)
	Create(context.Context, CreateRequest) (Observation, error)
	Inspect(context.Context, Reference) (Observation, error)
	Pause(context.Context, Reference) (Observation, error)
	Resume(context.Context, Reference) (Observation, error)
	Configure(context.Context, Reference, Network, []Mount) (Observation, error)
}

type VolumeReference struct{ StoreID, VolumeID string }
type Volume struct {
	VolumeReference
	Capacity  int64
	UsedBytes *int64
}

type VolumeEntry struct {
	Name, Type string
	Size       int64
	ModifiedAt time.Time
}
type VolumeReads interface {
	LstatVolume(context.Context, VolumeReference, string) (VolumeEntry, error)
	ReadVolume(context.Context, VolumeReference, string, int64, int64) ([]byte, error)
}

type VolumeListings interface {
	ListVolume(context.Context, VolumeReference, string) ([]VolumeEntry, error)
}
