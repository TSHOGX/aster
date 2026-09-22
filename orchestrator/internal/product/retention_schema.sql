-- Version 6: bounded replay accounting and actual ordinal pruning frontiers.
DELETE FROM events WHERE kind='agent.record';
ALTER TABLE project_event_frontiers ADD COLUMN event_count INTEGER NOT NULL DEFAULT 0;
INSERT INTO project_event_frontiers(project_id,retained_after,event_count)
 SELECT project_id,0,count(*) FROM events WHERE 1 GROUP BY project_id
 ON CONFLICT(project_id) DO UPDATE SET event_count=excluded.event_count;
CREATE TRIGGER project_event_account_insert AFTER INSERT ON events BEGIN
 INSERT INTO project_event_frontiers(project_id,retained_after,event_count) VALUES(NEW.project_id,0,1)
 ON CONFLICT(project_id) DO UPDATE SET event_count=event_count+1;
END;
CREATE TRIGGER project_event_account_delete AFTER DELETE ON events BEGIN
 UPDATE project_event_frontiers SET event_count=event_count-1 WHERE project_id=OLD.project_id;
END;
CREATE INDEX project_event_pressure ON project_event_frontiers(event_count,project_id);
ALTER TABLE conversation_changes ADD COLUMN retained_bytes INTEGER NOT NULL DEFAULT 0;
UPDATE conversation_changes SET retained_bytes=length(CAST(data AS BLOB))+length(item_id)+128+
 coalesce((SELECT length(CAST(content AS BLOB)) FROM conversation_details d WHERE d.session_id=conversation_changes.session_id AND d.item_id=conversation_changes.item_id AND d.item_revision=conversation_changes.revision),0);
CREATE TABLE conversation_retention (
 session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
 change_count INTEGER NOT NULL DEFAULT 0,
 change_bytes INTEGER NOT NULL DEFAULT 0,
	pruning INTEGER NOT NULL DEFAULT 0 CHECK(pruning IN (0,1)),
 pruned_revision INTEGER NOT NULL DEFAULT 0,
 pruned_ordinal INTEGER NOT NULL DEFAULT -1
);
INSERT INTO conversation_retention(session_id,change_count,change_bytes)
 SELECT session_id,count(*),sum(retained_bytes) FROM conversation_changes GROUP BY session_id;
CREATE TRIGGER conversation_change_account_insert AFTER INSERT ON conversation_changes BEGIN
 UPDATE conversation_changes SET retained_bytes=length(CAST(NEW.data AS BLOB))+length(NEW.item_id)+128+
 coalesce((SELECT length(CAST(content AS BLOB)) FROM conversation_details WHERE session_id=NEW.session_id AND item_id=NEW.item_id AND item_revision=NEW.revision),0)
 WHERE session_id=NEW.session_id AND revision=NEW.revision AND ordinal=NEW.ordinal;
 INSERT INTO conversation_retention(session_id,change_count,change_bytes)
 SELECT NEW.session_id,1,retained_bytes FROM conversation_changes WHERE session_id=NEW.session_id AND revision=NEW.revision AND ordinal=NEW.ordinal
 ON CONFLICT(session_id) DO UPDATE SET change_count=change_count+1,change_bytes=change_bytes+excluded.change_bytes;
END;
CREATE TRIGGER conversation_change_account_delete AFTER DELETE ON conversation_changes BEGIN
 UPDATE conversation_retention SET change_count=change_count-1,change_bytes=change_bytes-OLD.retained_bytes,
 pruned_ordinal=CASE WHEN OLD.revision>pruned_revision THEN OLD.ordinal WHEN OLD.revision=pruned_revision THEN max(pruned_ordinal,OLD.ordinal) ELSE pruned_ordinal END,
 pruned_revision=max(pruned_revision,OLD.revision) WHERE session_id=OLD.session_id;
END;
CREATE INDEX conversation_retention_pressure ON conversation_retention(change_bytes,session_id);
CREATE INDEX conversation_changes_detail ON conversation_changes(session_id,item_id,revision);
CREATE TABLE conversation_detail_gc (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 item_id TEXT NOT NULL,
 item_revision INTEGER NOT NULL,
 PRIMARY KEY(session_id,item_id,item_revision)
);
INSERT INTO conversation_detail_gc SELECT d.session_id,d.item_id,d.item_revision FROM conversation_details d
 WHERE NOT EXISTS(SELECT 1 FROM conversation_items i WHERE i.session_id=d.session_id AND i.id=d.item_id AND i.item_revision=d.item_revision)
 AND NOT EXISTS(SELECT 1 FROM conversation_changes c WHERE c.session_id=d.session_id AND c.item_id=d.item_id AND c.revision=d.item_revision);
CREATE TRIGGER conversation_detail_after_change AFTER DELETE ON conversation_changes BEGIN
 INSERT OR IGNORE INTO conversation_detail_gc SELECT OLD.session_id,OLD.item_id,OLD.revision
 WHERE EXISTS(SELECT 1 FROM conversation_details WHERE session_id=OLD.session_id AND item_id=OLD.item_id AND item_revision=OLD.revision);
END;
CREATE TRIGGER conversation_detail_after_update AFTER UPDATE OF item_revision ON conversation_items BEGIN
 INSERT OR IGNORE INTO conversation_detail_gc SELECT OLD.session_id,OLD.id,OLD.item_revision
 WHERE EXISTS(SELECT 1 FROM conversation_details WHERE session_id=OLD.session_id AND item_id=OLD.id AND item_revision=OLD.item_revision);
END;
CREATE TRIGGER conversation_detail_after_item AFTER DELETE ON conversation_items BEGIN
 INSERT OR IGNORE INTO conversation_detail_gc SELECT OLD.session_id,OLD.id,OLD.item_revision
 WHERE EXISTS(SELECT 1 FROM conversation_details WHERE session_id=OLD.session_id AND item_id=OLD.id AND item_revision=OLD.item_revision);
END;

-- Obsolete read-command rows become pure process cleanup, without preserving
-- user paths or outcomes. Mutations and their uncertain identities are untouched.
CREATE TABLE workspace_reader_retirements (
 project_id TEXT NOT NULL REFERENCES projects(id),
 store_id TEXT NOT NULL,
 sandbox_id TEXT NOT NULL,
 epoch INTEGER NOT NULL,
 handle TEXT PRIMARY KEY
);
CREATE INDEX workspace_reader_retirements_project ON workspace_reader_retirements(project_id,handle);
INSERT INTO workspace_reader_retirements SELECT project_id,store_id,sandbox_id,epoch,handle FROM file_operations
 WHERE settled=0 AND json_extract(request,'$.operation') IN ('list','read');
DELETE FROM project_activity WHERE id IN (SELECT 'file:'||id FROM file_operations WHERE json_extract(request,'$.operation') IN ('list','read'));
DELETE FROM file_operations WHERE json_extract(request,'$.operation') IN ('list','read');
