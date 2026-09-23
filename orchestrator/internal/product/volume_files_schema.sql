-- Version 13: file mutation authority can be a volume without a placement.
ALTER TABLE project_resources ADD COLUMN storage_key TEXT NOT NULL DEFAULT '';
DROP VIEW owner_handoff_blockers;
DROP VIEW release_blockers;
ALTER TABLE file_operations RENAME TO file_operations_v12;
CREATE TABLE file_operations (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 request TEXT NOT NULL,
 store_id TEXT NOT NULL,
 sandbox_id TEXT NOT NULL,
 epoch INTEGER NOT NULL CHECK(epoch>=0),
 handle TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('running','succeeded','failed','unknown')),
 settled INTEGER NOT NULL DEFAULT 0 CHECK(settled IN (0,1)),
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 volume_id TEXT NOT NULL DEFAULT '',
 staging_path TEXT NOT NULL DEFAULT '',
 CHECK((volume_id='' AND epoch>0) OR (volume_id<>'' AND sandbox_id='' AND epoch=0 AND handle=''))
);
INSERT INTO file_operations(id,project_id,request,store_id,sandbox_id,epoch,handle,state,settled,error,created_at,updated_at)
 SELECT id,project_id,request,store_id,sandbox_id,epoch,handle,state,settled,error,created_at,updated_at FROM file_operations_v12;
DROP TABLE file_operations_v12;
CREATE INDEX file_operations_pending ON file_operations(settled,updated_at,project_id);

CREATE INDEX release_file_write_pending ON file_operations(id) WHERE settled=0;
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
CREATE VIEW owner_handoff_blockers AS
SELECT b.kind,b.id FROM release_blockers b WHERE
 (b.kind NOT IN ('turn','model_request'))
 OR (b.kind='turn' AND NOT EXISTS(SELECT 1 FROM owner_transferable_turns t WHERE t.id=b.id))
 OR (b.kind='model_request' AND NOT EXISTS(
  SELECT 1 FROM service_request_slots r JOIN owner_transferable_turns t ON t.id=r.turn_id
  JOIN service_grants g ON g.id=r.grant_id AND g.incarnation_id=t.incarnation_id AND g.audience=r.audience
   AND g.state='active' AND g.expires_at>strftime('%Y-%m-%dT%H:%M:%f000000Z','now')
  WHERE r.audience||':'||r.slot=b.id AND r.closed=0
 ));
CREATE TRIGGER release_file_write BEFORE INSERT ON file_operations
WHEN (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;
CREATE TRIGGER release_ready_file_write_insert AFTER INSERT ON file_operations
WHEN (NEW.settled=0) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_file_write_update AFTER UPDATE OF settled ON file_operations
WHEN (NEW.settled=0) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
