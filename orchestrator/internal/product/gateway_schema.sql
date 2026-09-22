-- Version 11: bounded gateway request slots survive application replacement.
CREATE TABLE service_gateways (
 audience TEXT PRIMARY KEY CHECK(audience<>''),
 instance TEXT NOT NULL CHECK(instance<>''),
 generation INTEGER NOT NULL CHECK(generation>0),
 slots INTEGER NOT NULL CHECK(slots BETWEEN 1 AND 256)
);
-- Closed slots are bounded tombstones: a late acquire cannot resurrect a request
-- whose release arrived first. No credential or request body is persisted here.
CREATE TABLE service_request_slots (
 audience TEXT NOT NULL REFERENCES service_gateways(audience),
 slot INTEGER NOT NULL CHECK(slot BETWEEN 0 AND 255),
 sequence INTEGER NOT NULL CHECK(sequence>0),
 closed INTEGER NOT NULL CHECK(closed IN (0,1)),
 grant_id TEXT NOT NULL DEFAULT '',
 turn_id TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(audience,slot)
);
CREATE INDEX release_model_request_pending ON service_request_slots(audience,slot) WHERE closed=0;
CREATE TRIGGER release_model_request_insert BEFORE INSERT ON service_request_slots
WHEN NEW.closed=0 AND (SELECT phase FROM application_release WHERE id=1) NOT IN ('open','draining')
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;
CREATE TRIGGER release_model_request_update BEFORE UPDATE ON service_request_slots
WHEN NEW.closed=0 AND (SELECT phase FROM application_release WHERE id=1) NOT IN ('open','draining')
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;
DROP VIEW release_blockers;
CREATE VIEW release_blockers AS
SELECT 'turn' AS kind,id AS id FROM turns INDEXED BY release_turn_pending WHERE state IN ('starting','running','stopping') OR (state='unknown' AND acknowledged=0)
UNION ALL SELECT 'command' AS kind,id AS id FROM agent_commands INDEXED BY release_command_pending
WHERE state IN ('pending','sending','dispatching','unknown') AND (state<>'unknown' OR NOT EXISTS(
 SELECT 1 FROM turns t JOIN agent_incarnations i ON i.id=agent_commands.incarnation_id
 WHERE t.id=agent_commands.turn_id AND t.acknowledged=1 AND i.state='exited'
 AND i.cleanup_state='settled' AND i.recovery_state NOT IN ('pending','replaying')
))
UNION ALL SELECT 'activity' AS kind,id AS id FROM project_activity INDEXED BY release_activity_pending WHERE state<>'complete'
UNION ALL SELECT 'agent_transition' AS kind,id AS id FROM agent_incarnations INDEXED BY release_agent_transition_pending
WHERE (state IN ('starting','stopping','unknown') OR recovery_state IN ('pending','replaying','failed'))
 AND (state IN ('starting','stopping','unknown') OR recovery_state<>'failed' OR EXISTS(
 SELECT 1 FROM turns t WHERE t.session_id=agent_incarnations.session_id AND t.state='unknown' AND t.acknowledged=0
))
UNION ALL SELECT 'terminal_start' AS kind,id AS id FROM terminal_incarnations INDEXED BY release_terminal_start_pending WHERE state='starting'
UNION ALL SELECT 'project_operation' AS kind,id AS id FROM project_operations INDEXED BY release_project_operation_pending WHERE state<>'succeeded'
UNION ALL SELECT 'project_transition' AS kind,id AS id FROM projects INDEXED BY release_project_transition_pending WHERE state IN ('provisioning','resuming','pausing','deleting')
UNION ALL SELECT 'file_write' AS kind,id AS id FROM file_operations INDEXED BY release_file_write_pending WHERE settled=0
UNION ALL SELECT 'session_cleanup' AS kind,id AS id FROM sessions INDEXED BY release_session_cleanup_pending WHERE deletion_intent=1
UNION ALL SELECT 'terminal_cleanup' AS kind,id AS id FROM terminals INDEXED BY release_terminal_cleanup_pending WHERE deletion_intent=1
UNION ALL SELECT 'content_publication' AS kind,revision_digest AS id FROM content_publications INDEXED BY release_content_publication_pending WHERE stage NOT IN ('published','failed')
UNION ALL SELECT 'native_capture' AS kind,turn_id AS id FROM native_captures INDEXED BY release_native_capture_pending WHERE state='guest'
UNION ALL SELECT 'share_publication' AS kind,id AS id FROM shares INDEXED BY release_share_publication_pending WHERE state='pending' OR (state IN ('revoked','failed') AND cleaned=0)
UNION ALL SELECT 'model_request' AS kind,audience||':'||slot AS id FROM service_request_slots INDEXED BY release_model_request_pending WHERE closed=0;
