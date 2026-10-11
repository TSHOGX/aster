package product

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// runtimeConfigurationDigest identifies persisted configuration, not transient
// usage, credentials or session traffic. It is collected under the same writer
// transaction as the handoff fence and rechecked under the lifetime lock on open.
func runtimeConfigurationDigest(ctx context.Context, q queryer) (string, error) {
	statements := []string{
		`SELECT id,version,digest,store_id,template_id,manifest,enabled,revision,retired_at FROM registered_sandboxes ORDER BY id,version`,
		`SELECT id,version,digest,store_id,publication_purpose,minimum_bytes,roles,clean_runtime,entries,enabled,revision,retired_at FROM builtin_volumes ORDER BY id,version`,
		`SELECT DISTINCT p.store_id,p.purpose,p.revision_digest,p.capacity,p.published_volume_id FROM content_publications p JOIN builtin_volumes v ON v.store_id=p.store_id AND v.publication_purpose=p.purpose AND v.digest=p.revision_digest WHERE p.stage='published' ORDER BY p.store_id,p.purpose,p.revision_digest`,
		`SELECT revision,document FROM default_environment WHERE id=1`,
		`SELECT configuration_revision,revision,policy FROM network_defaults WHERE id=1`,
		`SELECT id,role,status FROM accounts ORDER BY id`,
		`SELECT id,execution_suspended,execution_hold_reason FROM projects WHERE execution_suspended=1 ORDER BY id`,
		`SELECT account_id,revision,policy FROM account_network_settings ORDER BY account_id`,
		`SELECT id,revision FROM account_groups ORDER BY id`,
		`SELECT account_id,group_id FROM account_memberships ORDER BY account_id,group_id`,
		`SELECT id,revision,statements FROM permission_policies ORDER BY id`,
		`SELECT group_id,policy_id FROM group_policy_bindings ORDER BY group_id,policy_id`,
		`SELECT group_id,dimension,default_value,ceiling FROM group_resource_policy ORDER BY group_id,dimension`,
		`SELECT dimension,maximum,revision FROM platform_resource_limits ORDER BY dimension`,
		`SELECT account_id,revision FROM account_resource_settings ORDER BY account_id`,
		`SELECT account_id,dimension,requested FROM account_resource_overrides ORDER BY account_id,dimension`,
	}
	return configurationDigest(ctx, q, statements)
}

func configurationDigest(ctx context.Context, q queryer, statements []string) (string, error) {
	digest := sha256.New()
	for _, statement := range statements {
		rows, err := q.QueryContext(ctx, statement)
		if err != nil {
			return "", err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return "", err
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				break
			}
			var raw []byte
			raw, err = json.Marshal(values)
			if err != nil {
				break
			}
			digest.Write(raw)
			digest.Write([]byte{0})
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return "", err
		}
		digest.Write([]byte{1})
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}
