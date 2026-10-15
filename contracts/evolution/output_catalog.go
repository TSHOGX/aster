package evolution

import (
	manifest "aster/contracts/volumemanifest"
	"encoding/json"
	"fmt"
)

// OutputCatalogQuery selects metadata only. A ticket binds this whole query to a
// currently authenticated central group reader; caller-supplied actors are absent.
type OutputCatalogQuery struct {
	GroupID   string `json:"group_id"`
	LibraryID string `json:"library_id,omitempty"`
	Search    string `json:"search,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	Limit     int    `json:"limit"`
}

func (q OutputCatalogQuery) Validate() error {
	if !manifest.ValidID(q.GroupID) || (q.LibraryID != "" && !manifest.ValidID(q.LibraryID)) || len(q.Search) > 256 || len(q.Cursor) > 4096 || q.Limit < 1 || q.Limit > 50 {
		return fmt.Errorf("invalid output catalog query")
	}
	return nil
}
func (q OutputCatalogQuery) Digest() string { raw, _ := json.Marshal(q); return Digest(raw) }

type OutputCatalogScope struct {
	Ticket string             `json:"ticket"`
	Query  OutputCatalogQuery `json:"query"`
}
type OutputLibrary struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Profile      string `json:"profile"`
	Archived     bool   `json:"archived"`
	OwnerGroup   string `json:"owner_group"`
	CanConfigure bool   `json:"can_configure"`
}
type OutputCatalogPage struct {
	Items []OutputLibrary `json:"items"`
	Next  string          `json:"next"`
	Total int             `json:"total"`
}
type OutputReservation struct {
	Prefix     string `json:"prefix"`
	GroupID    string `json:"group_id"`
	BindingID  string `json:"binding_id"`
	Generation int64  `json:"generation"`
	State      string `json:"state"`
}
type OutputLibraryDetail struct {
	Library      OutputLibrary       `json:"library"`
	Main         ContentRef          `json:"main"`
	Published    *ReleaseRef         `json:"published,omitempty"`
	Reservations []OutputReservation `json:"reservations"`
}
