package history

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
)

// CommitInfo describes immutable history, never a mutable draft or a publication.
type CommitInfo struct {
	ID        string   `json:"id"`
	Parents   []string `json:"parents"`
	CreatedAt int64    `json:"created_at"`
	Message   string   `json:"message"`
}

func (r *Repository) Log(ctx context.Context, head string, offset, limit int) ([]CommitInfo, error) {
	if !validOID(head) || offset < 0 || offset > 1000000 || limit < 1 || limit > 201 {
		return nil, fmt.Errorf("invalid history page")
	}
	var output bytes.Buffer
	err := r.commandTo(ctx, nil, &boundedWriter{w: &output, limit: int64(limit) * 8192}, "log", "--topo-order", "--no-decorate", "--no-show-signature", "--format=%H%n%P%n%ct%n%B%x00", "--max-count="+strconv.Itoa(limit), "--skip="+strconv.Itoa(offset), head, "--")
	if err != nil {
		return nil, err
	}
	result := []CommitInfo{}
	for _, record := range strings.Split(output.String(), "\x00") {
		record = strings.TrimPrefix(record, "\n")
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, "\n", 4)
		if len(fields) != 4 || !validOID(fields[0]) {
			return nil, fmt.Errorf("invalid history record")
		}
		parents := strings.Fields(fields[1])
		if parents == nil {
			parents = []string{}
		}
		for _, parent := range parents {
			if !validOID(parent) {
				return nil, fmt.Errorf("invalid history parent")
			}
		}
		created, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return nil, err
		}
		result = append(result, CommitInfo{fields[0], parents, created, strings.TrimSuffix(fields[3], "\n")})
	}
	return result, nil
}

// HasCommit distinguishes an absent/non-commit object from a failed Git reader.
func (r *Repository) HasCommit(ctx context.Context, id string) (bool, error) {
	if !validOID(id) {
		return false, fmt.Errorf("invalid commit")
	}
	var output bytes.Buffer
	err := r.commandTo(ctx, strings.NewReader(id+"\n"), &boundedWriter{w: &output, limit: 256}, "cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(output.String()) == id+" commit", nil
}
