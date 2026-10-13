CREATE INDEX libraries_workbench_name ON libraries(name COLLATE NOCASE,id);
CREATE INDEX versions_workbench_time ON versions(library_id,created_at,id);
CREATE INDEX audit_workbench_time ON audit(library_id,created_at);
ALTER TABLE versions ADD COLUMN published_at INTEGER;
