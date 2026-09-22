-- Version 4: actual pruning boundaries, not guesses from noncontiguous outbox IDs.
CREATE TABLE project_event_frontiers (
 project_id TEXT PRIMARY KEY REFERENCES projects(id),
 retained_after INTEGER NOT NULL DEFAULT 0
);
CREATE TRIGGER project_event_pruned BEFORE DELETE ON events BEGIN
 INSERT INTO project_event_frontiers(project_id,retained_after) VALUES(OLD.project_id,OLD.id)
 ON CONFLICT(project_id) DO UPDATE SET retained_after=max(retained_after,excluded.retained_after);
END;
