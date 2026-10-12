package product

import "context"

type NetworkSelection struct {
	ConfigurationRevision int64         `json:"configuration_revision"`
	Mode                  string        `json:"mode"`
	Desired               NetworkPolicy `json:"desired"`
}

func (s *Store) OperatorProjectNetwork(ctx context.Context, project string) (NetworkSelection, error) {
	return projectNetworkSettings(ctx, s.reader, project)
}
func (s *Store) OperatorNetworkTemplates(ctx context.Context) ([]ResourceTemplate, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT id FROM network_templates ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []ResourceTemplate{}
	for _, id := range ids {
		value, err := readTemplate(ctx, s.reader, "network", id)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
}
func runtimeStore(ctx context.Context, q queryer) (string, error) {
	var id string
	err := missing(q.QueryRowContext(ctx, `SELECT store_id FROM runtime_store WHERE id=1`).Scan(&id))
	return id, err
}
