-- Version 8: release closure and remote-work reservations share one writer.
CREATE TABLE application_release (
 id INTEGER PRIMARY KEY CHECK(id=1),
 generation INTEGER NOT NULL DEFAULT 0 CHECK(generation>=0),
 owner TEXT NOT NULL DEFAULT '',
 phase TEXT NOT NULL DEFAULT 'open' CHECK(phase IN ('open','draining','ready','activating')),
 deadline TEXT NOT NULL DEFAULT '',
 reason TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL DEFAULT ''
);
INSERT INTO application_release(id) VALUES(1);
CREATE TRIGGER release_turn_insert BEFORE INSERT ON turns
WHEN NEW.state<>'held' AND (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;
CREATE TRIGGER release_turn_dispatch BEFORE UPDATE OF state ON turns
WHEN NEW.state IN ('queued','starting') AND NEW.state<>OLD.state
 AND (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;
CREATE TRIGGER release_command_insert BEFORE INSERT ON agent_commands
WHEN NEW.kind IN ('prompt','steer') AND (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;
CREATE TRIGGER release_command_send BEFORE UPDATE OF state ON agent_commands
WHEN NEW.kind IN ('prompt','steer') AND OLD.state='pending' AND NEW.state='sending'
 AND (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;
CREATE TRIGGER release_agent_start BEFORE INSERT ON agent_incarnations
WHEN (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;
CREATE TRIGGER release_terminal_start BEFORE INSERT ON terminal_incarnations
WHEN (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;
CREATE TRIGGER release_file_write BEFORE INSERT ON file_operations
WHEN (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;
CREATE TRIGGER release_project_start BEFORE INSERT ON project_operations
WHEN NEW.kind IN ('create','resume') AND (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;
CREATE TRIGGER release_activity_start BEFORE INSERT ON project_activity
WHEN NEW.kind IN ('agent_start','terminal_start','files') AND (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;
CREATE TRIGGER release_publication_start BEFORE INSERT ON content_publications
WHEN NOT EXISTS(SELECT 1 FROM content_publications WHERE store_id=NEW.store_id AND purpose=NEW.purpose AND revision_digest=NEW.revision_digest)
 AND (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;
