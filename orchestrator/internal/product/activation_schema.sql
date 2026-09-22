-- Version 10: a replacement owner, not the retiring process, reopens admission.
ALTER TABLE application_release ADD COLUMN current_instance TEXT NOT NULL DEFAULT '';
ALTER TABLE application_release ADD COLUMN activation_instance TEXT NOT NULL DEFAULT '';
ALTER TABLE application_release ADD COLUMN target_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE application_release ADD COLUMN finished_digest TEXT NOT NULL DEFAULT '';
CREATE TABLE release_coordinators (
 owner TEXT PRIMARY KEY CHECK(owner<>''),
 generation INTEGER NOT NULL UNIQUE CHECK(generation>0)
);
INSERT INTO release_coordinators SELECT owner,generation FROM application_release WHERE owner<>'' AND generation>0;

DROP TRIGGER release_publication_start;
CREATE TRIGGER release_publication_start BEFORE INSERT ON content_publications
WHEN NOT EXISTS(SELECT 1 FROM content_publications WHERE store_id=NEW.store_id AND purpose=NEW.purpose AND revision_digest=NEW.revision_digest)
 AND (SELECT phase FROM application_release WHERE id=1)<>'open'
 AND NOT EXISTS(SELECT 1 FROM application_release WHERE id=1 AND phase='activating' AND activation_instance<>'' AND current_instance<>activation_instance)
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;

DROP TRIGGER release_project_start;
CREATE TRIGGER release_project_start BEFORE INSERT ON project_operations
WHEN NEW.kind IN ('create','resume') AND (SELECT phase FROM application_release WHERE id=1)<>'open'
 AND NOT(NEW.kind='resume' AND EXISTS(
 SELECT 1 FROM application_release WHERE id=1 AND phase='activating' AND activation_instance<>'' AND current_instance<>activation_instance
 ) AND EXISTS(SELECT 1 FROM agent_incarnations WHERE project_id=NEW.project_id AND state='exited' AND recovery_state IN ('pending','replaying')))
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;
