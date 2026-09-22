-- Version 5: one recoverable helper binding per Project, never per read.
-- No user file bytes, outcomes or activity leases belong in this table.
CREATE TABLE workspace_readers (
 project_id TEXT PRIMARY KEY REFERENCES projects(id),
 store_id TEXT NOT NULL,
 sandbox_id TEXT NOT NULL,
 epoch INTEGER NOT NULL CHECK(epoch>0),
 handle TEXT NOT NULL UNIQUE,
 created_at TEXT NOT NULL
);
