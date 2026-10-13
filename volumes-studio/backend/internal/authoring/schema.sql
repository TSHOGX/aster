CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE blobs(
 digest TEXT PRIMARY KEY,size INTEGER NOT NULL CHECK(size>=0),
 state TEXT NOT NULL DEFAULT 'live' CHECK(state IN ('live','deleting'))
);
CREATE TABLE libraries(
 id TEXT PRIMARY KEY,name TEXT NOT NULL UNIQUE,description TEXT NOT NULL,
 metadata_revision INTEGER NOT NULL DEFAULT 1,
 draft_revision INTEGER NOT NULL DEFAULT 1,
 base_version TEXT NOT NULL DEFAULT '',
 issuer TEXT NOT NULL,subject TEXT NOT NULL,
 archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN(0,1)),created_at INTEGER NOT NULL
);
CREATE TABLE draft_entries(
 library_id TEXT NOT NULL REFERENCES libraries(id),path TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN('file','directory')),
 size INTEGER NOT NULL CHECK(size>=0),digest TEXT REFERENCES blobs(digest),
 PRIMARY KEY(library_id,path),
 CHECK((kind='directory' AND size=0 AND digest IS NULL) OR (kind='file' AND digest IS NOT NULL))
);
CREATE INDEX draft_blob_refs ON draft_entries(digest);
CREATE TABLE versions(
 id TEXT PRIMARY KEY,library_id TEXT NOT NULL REFERENCES libraries(id),
 label TEXT NOT NULL,note TEXT NOT NULL,source_revision INTEGER NOT NULL,
 descriptor BLOB NOT NULL,descriptor_digest TEXT NOT NULL UNIQUE,
 state TEXT NOT NULL CHECK(state IN('pending','validating','published','archived','failed','retired')),
 issuer TEXT NOT NULL,subject TEXT NOT NULL,created_at INTEGER NOT NULL,
 error TEXT NOT NULL DEFAULT '',UNIQUE(library_id,label)
);
CREATE TABLE version_entries(
 version_id TEXT NOT NULL REFERENCES versions(id),path TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN('file','directory')),
 size INTEGER NOT NULL CHECK(size>=0),digest TEXT REFERENCES blobs(digest),
 PRIMARY KEY(version_id,path),
 CHECK((kind='directory' AND size=0 AND digest IS NULL) OR (kind='file' AND digest IS NOT NULL))
);
CREATE INDEX version_blob_refs ON version_entries(digest);
CREATE TABLE receipts(
 issuer TEXT NOT NULL,subject TEXT NOT NULL,request_id TEXT NOT NULL,
 body_digest TEXT NOT NULL,response BLOB NOT NULL,
 PRIMARY KEY(issuer,subject,request_id)
);
CREATE TABLE audit(
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,issuer TEXT NOT NULL,subject TEXT NOT NULL,
 action TEXT NOT NULL,library_id TEXT NOT NULL,request_id TEXT NOT NULL,
 revision INTEGER NOT NULL,created_at INTEGER NOT NULL
);
CREATE INDEX audit_library ON audit(library_id,sequence);
