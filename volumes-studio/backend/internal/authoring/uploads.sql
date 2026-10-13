CREATE TABLE uploads(
 id TEXT PRIMARY KEY,library_id TEXT NOT NULL REFERENCES libraries(id),
 path TEXT NOT NULL,issuer TEXT NOT NULL,subject TEXT NOT NULL,
 size INTEGER NOT NULL CHECK(size>=0),digest TEXT NOT NULL,
 offset INTEGER NOT NULL DEFAULT 0 CHECK(offset>=0),
 expected_revision INTEGER NOT NULL,target_identity TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN('open','complete','canceled','expired','failed')),
 reserved_bytes INTEGER NOT NULL CHECK(reserved_bytes>=0),
 expires_at INTEGER NOT NULL,created_at INTEGER NOT NULL
);
CREATE INDEX uploads_library ON uploads(library_id,state);
CREATE INDEX uploads_actor ON uploads(issuer,subject,state);
CREATE TABLE upload_chunks(
 upload_id TEXT NOT NULL REFERENCES uploads(id),offset INTEGER NOT NULL,
 size INTEGER NOT NULL,digest TEXT NOT NULL,
 PRIMARY KEY(upload_id,offset)
);
