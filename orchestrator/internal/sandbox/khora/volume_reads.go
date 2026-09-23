package khora

import (
	"context"
	"encoding/json"
	"fmt"
	"harness-platform/orchestrator/internal/sandbox"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (b *Backend) validateVolumeRead(ctx context.Context, reference sandbox.VolumeReference, name string) error {
	identity, err := b.Identity(ctx)
	if err != nil {
		return err
	}
	if reference.StoreID != identity.StoreID {
		return failure("volume read", sandbox.IdentityChanged, fmt.Errorf("volume belongs to another engine store"))
	}
	if reference.VolumeID == "" || strings.ContainsAny(reference.VolumeID, "/\\?#") || name == "" || strings.ContainsAny(name, "\\\x00") {
		return failure("volume read", sandbox.Invalid, fmt.Errorf("volume identity and clean relative path required"))
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return failure("volume read", sandbox.Invalid, fmt.Errorf("clean relative volume path required"))
		}
	}
	return nil
}

func (b *Backend) ListVolume(ctx context.Context, reference sandbox.VolumeReference, name string) ([]sandbox.VolumeEntry, error) {
	check := name
	if check == "" {
		check = "root"
	}
	if err := b.validateVolumeRead(ctx, reference, check); err != nil {
		return nil, err
	}
	path := volumePath(name)
	if name == "" {
		path = "."
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, b.endpoint+"/v1/volumes/"+url.PathEscape(reference.VolumeID)+"/list?"+url.Values{"path": []string{path}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	response, err := b.httpClient.Do(request)
	if err != nil {
		return nil, mapError("volume list", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		class := sandbox.Unavailable
		switch response.StatusCode {
		case 404:
			class = sandbox.NotFound
		case 400:
			class = sandbox.Invalid
		case 409:
			class = sandbox.Conflict
		case 429, 503, 507:
			class = sandbox.Capacity
		}
		return nil, failure("volume list", class, fmt.Errorf("engine volume listing returned HTTP %d", response.StatusCode))
	}
	const limit = 32 << 20
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, failure("volume list", sandbox.Capacity, fmt.Errorf("volume directory metadata exceeds bound"))
	}
	var entries []struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Size     int64  `json:"size"`
		Modified string `json:"mod_time"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	if len(entries) > 100000 {
		return nil, failure("volume list", sandbox.Capacity, fmt.Errorf("volume directory entries exceed bound"))
	}
	result := make([]sandbox.VolumeEntry, 0, len(entries))
	for _, entry := range entries {
		stamp, err := time.Parse(time.RFC3339Nano, entry.Modified)
		if err != nil {
			return nil, err
		}
		result = append(result, sandbox.VolumeEntry{Name: entry.Name, Type: entry.Type, Size: entry.Size, ModifiedAt: stamp})
	}
	return result, nil
}
func (b *Backend) LstatVolume(ctx context.Context, reference sandbox.VolumeReference, name string) (sandbox.VolumeEntry, error) {
	if err := b.validateVolumeRead(ctx, reference, name); err != nil {
		return sandbox.VolumeEntry{}, err
	}
	entry, err := b.client.LstatVolume(ctx, reference.VolumeID, volumePath(name))
	if err != nil {
		return sandbox.VolumeEntry{}, mapError("volume lstat", err)
	}
	stamp, err := time.Parse(time.RFC3339Nano, entry.ModTime)
	if err != nil {
		return sandbox.VolumeEntry{}, failure("volume lstat", sandbox.Incompatible, err)
	}
	if entry.Size < 0 {
		return sandbox.VolumeEntry{}, failure("volume lstat", sandbox.Incompatible, fmt.Errorf("negative volume file size"))
	}
	return sandbox.VolumeEntry{Name: entry.Name, Type: entry.Type, Size: entry.Size, ModifiedAt: stamp}, nil
}
func (b *Backend) ReadVolume(ctx context.Context, reference sandbox.VolumeReference, name string, offset, length int64) ([]byte, error) {
	if err := b.validateVolumeRead(ctx, reference, name); err != nil {
		return nil, err
	}
	if offset < 0 || length <= 0 || length > 256<<10 {
		return nil, failure("volume read", sandbox.Invalid, fmt.Errorf("bounded positive volume range required"))
	}
	data, err := b.client.ReadVolume(ctx, reference.VolumeID, volumePath(name), offset, length)
	if err != nil {
		return nil, mapError("volume read", err)
	}
	if int64(len(data)) > length {
		return nil, failure("volume read", sandbox.Incompatible, fmt.Errorf("volume response exceeded requested range"))
	}
	return data, nil
}
