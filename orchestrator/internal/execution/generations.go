package execution

import "errors"

var ErrBusy = errors.New("generation lifecycle is busy")

type Session struct{ ID, Driver, ActiveGenerationID string }

var ErrResumeConflict = errors.New("retained generation changed")
