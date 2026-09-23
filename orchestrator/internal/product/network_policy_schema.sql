CREATE TABLE network_defaults (
 id INTEGER PRIMARY KEY CHECK(id=1),
 revision TEXT NOT NULL,
 policy TEXT NOT NULL,
 accepted_at TEXT NOT NULL
);
