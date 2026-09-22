package product

// Display metadata is independent of the bounded execution-control turn page.
// An older conversation page must not guess completion from Session idle state.
type TurnSummary struct {
	Sequence       int64  `json:"sequence"`
	ID             string `json:"id"`
	State          string `json:"state"`
	CreatedAt      string `json:"created_at"`
	FinishedAt     string `json:"finished_at,omitempty"`
	FinalMessageID string `json:"final_message_id,omitempty"`
}
