package sandbox

// FileInvalidation says cached directory observations need a bounded rescan.
// It is deliberately not a durable file audit or an Agent output record.
type FileInvalidation struct {
	Sequence uint64 `json:"sequence"`
	Epoch    uint64 `json:"epoch"`
	Reset    bool   `json:"reset"`
}
type FileWatch interface {
	Next() (FileInvalidation, error)
	Close() error
}
