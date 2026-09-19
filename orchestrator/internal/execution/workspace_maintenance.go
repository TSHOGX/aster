package execution

// WorkspaceMaintenance describes the next application-owned lifecycle action.
// The content mutation remains fenced separately through verified final pause.
type WorkspaceMaintenance struct {
	Stage     string
	Operation Operation
}
