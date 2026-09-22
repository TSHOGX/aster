-- Version 9: indexed readiness predicates and transactional revocation.
CREATE INDEX release_turn_pending ON turns(id) WHERE state IN ('starting','running','stopping') OR (state='unknown' AND acknowledged=0);
CREATE INDEX release_command_pending ON agent_commands(id) WHERE state IN ('pending','sending','dispatching','unknown');
CREATE INDEX release_activity_pending ON project_activity(id) WHERE state<>'complete';
CREATE INDEX release_agent_transition_pending ON agent_incarnations(id) WHERE state IN ('starting','stopping','unknown') OR recovery_state IN ('pending','replaying','failed');
CREATE INDEX release_terminal_start_pending ON terminal_incarnations(id) WHERE state='starting';
CREATE INDEX release_project_operation_pending ON project_operations(id) WHERE state<>'succeeded';
CREATE INDEX release_project_transition_pending ON projects(id) WHERE state IN ('provisioning','resuming','pausing','deleting');
CREATE INDEX release_file_write_pending ON file_operations(id) WHERE settled=0;
CREATE INDEX release_session_cleanup_pending ON sessions(id) WHERE deletion_intent=1;
CREATE INDEX release_terminal_cleanup_pending ON terminals(id) WHERE deletion_intent=1;
CREATE INDEX release_content_publication_pending ON content_publications(revision_digest) WHERE stage NOT IN ('published','failed');
CREATE INDEX release_native_capture_pending ON native_captures(turn_id) WHERE state='guest';
CREATE INDEX release_share_publication_pending ON shares(id) WHERE state='pending' OR (state IN ('revoked','failed') AND cleaned=0);
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
UNION ALL SELECT 'share_publication' AS kind,id AS id FROM shares INDEXED BY release_share_publication_pending WHERE state='pending' OR (state IN ('revoked','failed') AND cleaned=0);
CREATE TRIGGER release_ready_turn_insert AFTER INSERT ON turns
WHEN (NEW.state IN ('starting','running','stopping') OR (NEW.state='unknown' AND NEW.acknowledged=0)) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_turn_update AFTER UPDATE OF state,acknowledged ON turns
WHEN (NEW.state IN ('starting','running','stopping') OR (NEW.state='unknown' AND NEW.acknowledged=0)) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_command_insert AFTER INSERT ON agent_commands
WHEN (NEW.state IN ('pending','sending','dispatching','unknown')) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_command_update AFTER UPDATE OF state ON agent_commands
WHEN (NEW.state IN ('pending','sending','dispatching','unknown')) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_activity_insert AFTER INSERT ON project_activity
WHEN (NEW.state<>'complete') AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_activity_update AFTER UPDATE OF state ON project_activity
WHEN (NEW.state<>'complete') AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_agent_transition_insert AFTER INSERT ON agent_incarnations
WHEN (NEW.state IN ('starting','stopping','unknown') OR NEW.recovery_state IN ('pending','replaying','failed')) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_agent_transition_update AFTER UPDATE OF state,recovery_state ON agent_incarnations
WHEN (NEW.state IN ('starting','stopping','unknown') OR NEW.recovery_state IN ('pending','replaying','failed')) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_terminal_start_insert AFTER INSERT ON terminal_incarnations
WHEN (NEW.state='starting') AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_terminal_start_update AFTER UPDATE OF state ON terminal_incarnations
WHEN (NEW.state='starting') AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_project_operation_insert AFTER INSERT ON project_operations
WHEN (NEW.state<>'succeeded') AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_project_operation_update AFTER UPDATE OF state ON project_operations
WHEN (NEW.state<>'succeeded') AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_project_transition_insert AFTER INSERT ON projects
WHEN (NEW.state IN ('provisioning','resuming','pausing','deleting')) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_project_transition_update AFTER UPDATE OF state ON projects
WHEN (NEW.state IN ('provisioning','resuming','pausing','deleting')) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_file_write_insert AFTER INSERT ON file_operations
WHEN (NEW.settled=0) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_file_write_update AFTER UPDATE OF settled ON file_operations
WHEN (NEW.settled=0) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_session_cleanup_insert AFTER INSERT ON sessions
WHEN (NEW.deletion_intent=1) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_session_cleanup_update AFTER UPDATE OF deletion_intent ON sessions
WHEN (NEW.deletion_intent=1) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_terminal_cleanup_insert AFTER INSERT ON terminals
WHEN (NEW.deletion_intent=1) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_terminal_cleanup_update AFTER UPDATE OF deletion_intent ON terminals
WHEN (NEW.deletion_intent=1) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_content_publication_insert AFTER INSERT ON content_publications
WHEN (NEW.stage NOT IN ('published','failed')) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_content_publication_update AFTER UPDATE OF stage ON content_publications
WHEN (NEW.stage NOT IN ('published','failed')) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_native_capture_insert AFTER INSERT ON native_captures
WHEN (NEW.state='guest') AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_native_capture_update AFTER UPDATE OF state ON native_captures
WHEN (NEW.state='guest') AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_share_publication_insert AFTER INSERT ON shares
WHEN (NEW.state='pending' OR (NEW.state IN ('revoked','failed') AND NEW.cleaned=0)) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_ready_share_publication_update AFTER UPDATE OF state,cleaned ON shares
WHEN (NEW.state='pending' OR (NEW.state IN ('revoked','failed') AND NEW.cleaned=0)) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_share_start BEFORE INSERT ON shares
WHEN (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;
-- Acknowledged historical uncertainty is not live work only while its original
-- process is gone and cleanup settled. Loss of that settlement revokes ready.
CREATE TRIGGER release_ready_cleanup_loss AFTER UPDATE OF cleanup_state ON agent_incarnations
WHEN NEW.cleanup_state='unknown' AND OLD.cleanup_state='settled'
 AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
