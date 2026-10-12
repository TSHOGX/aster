package product

import "context"

type fileMountContextKey struct{}

// WithFileMount identifies a declared project mount, never a volume or host path.
func WithFileMount(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, fileMountContextKey{}, key)
}
func fileMount(ctx context.Context) string {
	key, _ := ctx.Value(fileMountContextKey{}).(string)
	return key
}
func fileResourceView(ctx context.Context, q queryer, scope Scope, r Resources, write bool) (Resources, error) {
	key := fileMount(ctx)
	if key == "" {
		return r, nil
	}
	if _, err := ownedProject(ctx, q, scope); err != nil {
		return Resources{}, err
	}
	if _, err := authorizeProjectConfiguration(ctx, q, scope.Actor, scope.ProjectID); err != nil {
		return Resources{}, err
	}
	plans, err := authorizedVolumePlan(ctx, q, scope.Actor, scope.ProjectID, r.Profile.Volumes)
	if err != nil {
		return Resources{}, err
	}
	for _, m := range plans {
		if m.Key != key || m.Storage != "shared" {
			continue
		}
		if write && m.Access != "read_write" {
			return Resources{}, Admission("volume_read_only", ErrForbidden)
		}
		// Only this ephemeral file view aliases the selected mount as workspace.
		// Runtime plans and persisted resources retain their original roles.
		r.Profile.Volumes = append([]VolumePlan{}, r.Profile.Volumes...)
		for i := range r.Profile.Volumes {
			if r.Profile.Volumes[i].Role == "workspace" {
				r.Profile.Volumes[i].Role = "data"
			}
			if r.Profile.Volumes[i].Key == key {
				r.Profile.Volumes[i] = m
				r.Profile.Volumes[i].Role = "workspace"
			}
		}
		return r, nil
	}
	return Resources{}, ErrNotFound
}

type FileMount struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Access string `json:"access"`
}

func (s *Store) FileMounts(ctx context.Context, scope Scope) ([]FileMount, error) {
	if _, err := projectAction(ctx, s.reader, scope, ActionFilesList); err != nil {
		return nil, err
	}
	r, err := s.Resources(ctx, scope)
	if err != nil {
		return nil, err
	}
	plans, err := authorizedVolumePlan(ctx, s.reader, scope.Actor, scope.ProjectID, r.Profile.Volumes)
	if err != nil {
		return nil, err
	}
	out := []FileMount{}
	for _, p := range plans {
		if p.Storage != "shared" {
			continue
		}
		v, e := sharedVolume(ctx, s.reader, p.SharedVolumeID)
		if e != nil {
			return nil, e
		}
		out = append(out, FileMount{Key: p.Key, Name: v.Name, Access: p.Access})
	}
	return out, nil
}
