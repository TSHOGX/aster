package producthttp

import (
	"encoding/json"
	"time"
)

const eventPoll = 250 * time.Millisecond

func closeReason(err error) string {
	_, message := status(err)
	return message
}

func encode(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"error":"projection could not be encoded"}`)
	}
	return raw
}
