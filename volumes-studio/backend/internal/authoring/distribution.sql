CREATE TABLE exports(
 id TEXT PRIMARY KEY,client_id TEXT NOT NULL,grant_revision INTEGER NOT NULL,
 version_id TEXT NOT NULL REFERENCES versions(id),descriptor_digest TEXT NOT NULL,
 expires_at INTEGER NOT NULL,state TEXT NOT NULL CHECK(state IN('open','closed'))
);
CREATE INDEX exports_version ON exports(version_id,state,expires_at);
CREATE TABLE reader_pins(
 id TEXT PRIMARY KEY,export_id TEXT NOT NULL REFERENCES exports(id),
 digest TEXT NOT NULL REFERENCES blobs(digest)
);
CREATE INDEX reader_pin_blobs ON reader_pins(digest);
