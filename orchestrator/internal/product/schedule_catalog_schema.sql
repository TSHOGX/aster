-- Owner-scoped epochs make chronological cursors explicitly invalidatable.
CREATE TABLE schedule_catalog (
    owner_id TEXT PRIMARY KEY,
    revision INTEGER NOT NULL DEFAULT 1 CHECK(revision > 0)
);
INSERT INTO schedule_catalog(owner_id) SELECT DISTINCT owner_id FROM projects;
CREATE TRIGGER schedules_catalog_insert AFTER INSERT ON schedules BEGIN
    INSERT INTO schedule_catalog(owner_id,revision)
    SELECT owner_id,1 FROM projects WHERE id=NEW.project_id
    ON CONFLICT(owner_id) DO UPDATE SET revision=revision+1;
END;
CREATE TRIGGER schedules_catalog_update AFTER UPDATE ON schedules BEGIN
    UPDATE schedule_catalog SET revision=revision+1
    WHERE owner_id=(SELECT owner_id FROM projects WHERE id=NEW.project_id);
END;
CREATE TRIGGER schedules_catalog_delete BEFORE DELETE ON schedules BEGIN
    UPDATE schedule_catalog SET revision=revision+1
    WHERE owner_id=(SELECT owner_id FROM projects WHERE id=OLD.project_id);
END;
CREATE TRIGGER schedules_catalog_project_update AFTER UPDATE OF title,state,owner_id ON projects
WHEN OLD.title<>NEW.title OR OLD.owner_id<>NEW.owner_id OR
     (OLD.state IN ('deleting','deleted'))<>(NEW.state IN ('deleting','deleted')) BEGIN
    UPDATE schedule_catalog SET revision=revision+1 WHERE owner_id IN (OLD.owner_id,NEW.owner_id);
END;
CREATE TRIGGER schedules_catalog_project_delete BEFORE DELETE ON projects BEGIN
    UPDATE schedule_catalog SET revision=revision+1 WHERE owner_id=OLD.owner_id;
END;
CREATE INDEX schedules_overview_order ON schedules(
    deleted,
    CASE WHEN enabled=0 THEN 2 WHEN cron='' THEN 1 ELSE 0 END,
    coalesce(next_at,''),id
);
