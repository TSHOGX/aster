package sandbox

// FileInvalidation says cached directory observations need a bounded rescan.
// It is deliberately not a durable file audit or an Agent output record.
type FileInvalidation struct {
	Sequence  uint64 `json:"sequence,string"`
	Epoch     uint64 `json:"epoch,string"`
	Reset     bool   `json:"reset"`
	Path      string `json:"path,omitempty"`
	OldPath   string `json:"old_path,omitempty"`
	Operation string `json:"operation,omitempty"`
	EntryType string `json:"entry_type,omitempty"`
	Reason    string `json:"reason,omitempty"`
}
type FileWatch interface {
	Next() (FileInvalidation, error)
	Close() error
}
