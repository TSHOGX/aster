-- Version 12: explicit, same-contract owner handoff with retained native work.
ALTER TABLE application_release ADD COLUMN mode TEXT NOT NULL DEFAULT 'quiescent' CHECK(mode IN ('quiescent','owner'));
ALTER TABLE application_release ADD COLUMN compatibility_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE application_release ADD COLUMN schema_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE application_release ADD COLUMN gateway_snapshot TEXT NOT NULL DEFAULT '';

CREATE VIEW owner_transferable_turns AS
SELECT t.id,c.incarnation_id FROM turns t INDEXED BY release_turn_pending
JOIN agent_commands c INDEXED BY agent_prompt_turn ON c.turn_id=t.id AND c.kind='prompt' AND c.state='applied'
JOIN agent_incarnations i ON i.id=c.incarnation_id AND i.session_id=t.session_id AND i.state='running'
JOIN sessions s ON s.id=t.session_id AND s.deletion_intent=0 AND s.state='running'
JOIN projects p ON p.id=t.project_id AND p.state='active'
WHERE (t.state IN ('starting','running','stopping') OR (t.state='unknown' AND t.acknowledged=0)) AND t.state='running';

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

DROP TRIGGER release_model_request_insert;
DROP TRIGGER release_model_request_update;
CREATE TRIGGER release_model_request_insert BEFORE INSERT ON service_request_slots
WHEN NEW.closed=0 AND (SELECT phase FROM application_release WHERE id=1) NOT IN ('open','draining')
 AND NOT EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase IN ('ready','activating'))
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;
CREATE TRIGGER release_model_request_update BEFORE UPDATE ON service_request_slots
WHEN NEW.closed=0 AND (SELECT phase FROM application_release WHERE id=1) NOT IN ('open','draining')
 AND NOT EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase IN ('ready','activating'))
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;

-- Any replacement of a pinned gateway invalidates pending owner readiness.
CREATE TRIGGER release_ready_gateway_change AFTER UPDATE OF instance,generation,slots ON service_gateways
WHEN (NEW.instance<>OLD.instance OR NEW.generation<>OLD.generation OR NEW.slots<>OLD.slots) AND EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase='ready')
BEGIN UPDATE application_release SET phase='draining',reason='gateway_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

-- Losing the applied/running evidence or grant authority invalidates the active
-- checkpoint in the same transaction, including evidence that finishes a turn
-- before its gateway has acknowledged forwarding settlement.
CREATE TRIGGER release_owner_prompt_changed AFTER UPDATE OF state ON agent_commands
WHEN NEW.kind='prompt' AND OLD.state='applied' AND NEW.state<>'applied'
 AND EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase='ready')
BEGIN UPDATE application_release SET phase='draining',reason='retained_work_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_owner_turn_finished AFTER UPDATE OF state ON turns
WHEN OLD.state='running' AND NEW.state<>'running'
 AND EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase='ready')
BEGIN UPDATE application_release SET phase='draining',reason='retained_work_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_owner_session_changed AFTER UPDATE OF state ON sessions
WHEN OLD.state='running' AND NEW.state<>'running'
 AND EXISTS(SELECT 1 FROM turns WHERE session_id=NEW.id AND state='running')
 AND EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase='ready')
BEGIN UPDATE application_release SET phase='draining',reason='retained_work_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_owner_grant_changed AFTER UPDATE OF state,expires_at ON service_grants
WHEN (NEW.state<>'active' OR NEW.expires_at<=strftime('%Y-%m-%dT%H:%M:%f000000Z','now'))
 AND EXISTS(SELECT 1 FROM service_request_slots WHERE grant_id=NEW.id AND closed=0)
 AND EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase='ready')
BEGIN UPDATE application_release SET phase='draining',reason='retained_authority_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_owner_request_insert AFTER INSERT ON service_request_slots
WHEN NEW.closed=0
 AND EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase='ready')
 AND NOT EXISTS(SELECT 1 FROM owner_transferable_turns t JOIN service_grants g ON g.id=NEW.grant_id AND g.incarnation_id=t.incarnation_id AND g.audience=NEW.audience AND g.state='active' AND g.expires_at>strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE t.id=NEW.turn_id)
BEGIN UPDATE application_release SET phase='draining',reason='retained_authority_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
CREATE TRIGGER release_owner_request_update AFTER UPDATE ON service_request_slots
WHEN NEW.closed=0
 AND EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase='ready')
 AND NOT EXISTS(SELECT 1 FROM owner_transferable_turns t JOIN service_grants g ON g.id=NEW.grant_id AND g.incarnation_id=t.incarnation_id AND g.audience=NEW.audience AND g.state='active' AND g.expires_at>strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE t.id=NEW.turn_id)
BEGIN UPDATE application_release SET phase='draining',reason='retained_authority_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;
