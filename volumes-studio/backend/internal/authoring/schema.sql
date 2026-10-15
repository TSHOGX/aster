CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE blobs(
 digest TEXT PRIMARY KEY,size INTEGER NOT NULL CHECK(size>=0),
 state TEXT NOT NULL DEFAULT 'live' CHECK(state IN ('live','deleting'))
);
CREATE TABLE libraries(
 evolution_owner_group TEXT NOT NULL DEFAULT '',
 history_state TEXT NOT NULL DEFAULT 'preparing' CHECK(history_state IN('preparing','ready')),
 id TEXT PRIMARY KEY,name TEXT NOT NULL UNIQUE,description TEXT NOT NULL,
 profile TEXT NOT NULL DEFAULT 'authored-v1' CHECK(profile IN('authored-v1','shared-assets-v1')),
 metadata_revision INTEGER NOT NULL DEFAULT 1,
 issuer TEXT NOT NULL,subject TEXT NOT NULL,
 archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN(0,1)),created_at INTEGER NOT NULL
);
CREATE TABLE versions(
 accepted_commit TEXT NOT NULL DEFAULT '',
 id TEXT PRIMARY KEY,library_id TEXT NOT NULL REFERENCES libraries(id),
 label TEXT NOT NULL,note TEXT NOT NULL,source_revision INTEGER NOT NULL,
 descriptor BLOB NOT NULL,descriptor_digest TEXT NOT NULL UNIQUE,
 state TEXT NOT NULL CHECK(state IN('pending','validating','published','archived','failed','retired')),
 issuer TEXT NOT NULL,subject TEXT NOT NULL,created_at INTEGER NOT NULL,
 error TEXT NOT NULL DEFAULT '',generation INTEGER NOT NULL DEFAULT 0,
 published_at INTEGER,UNIQUE(library_id,label)
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

CREATE TABLE uploads(
 branch_id TEXT NOT NULL REFERENCES branches(id),
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

CREATE INDEX libraries_workbench_name ON libraries(name COLLATE NOCASE,id);
CREATE INDEX versions_workbench_time ON versions(library_id,created_at,id);
CREATE INDEX audit_workbench_time ON audit(library_id,created_at);

CREATE TRIGGER immutable_library_profile BEFORE UPDATE OF profile ON libraries
BEGIN SELECT RAISE(ABORT,'immutable library content profile'); END;

CREATE TABLE branches(
 id TEXT PRIMARY KEY,library_id TEXT NOT NULL REFERENCES libraries(id),
 name TEXT NOT NULL,initial_base TEXT NOT NULL,draft_revision INTEGER NOT NULL DEFAULT 1,
 state TEXT NOT NULL DEFAULT 'preparing' CHECK(state IN('preparing','ready')),
 issuer TEXT NOT NULL,subject TEXT NOT NULL,created_at INTEGER NOT NULL,
 UNIQUE(library_id,name)
);
CREATE TABLE branch_entries(
 branch_id TEXT NOT NULL REFERENCES branches(id),path TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN('file','directory')),size INTEGER NOT NULL CHECK(size>=0),
 digest TEXT REFERENCES blobs(digest),PRIMARY KEY(branch_id,path),
 CHECK((kind='directory' AND size=0 AND digest IS NULL) OR (kind='file' AND digest IS NOT NULL))
);
CREATE INDEX branch_blob_refs ON branch_entries(digest);
CREATE TABLE checkpoints(
 id TEXT PRIMARY KEY,branch_id TEXT NOT NULL REFERENCES branches(id),
 old_head TEXT NOT NULL,new_head TEXT NOT NULL,draft_revision INTEGER NOT NULL,
 state TEXT NOT NULL CHECK(state IN('prepared','settled'))
);
CREATE TABLE prepared_merges(
 digest TEXT PRIMARY KEY,library_id TEXT NOT NULL REFERENCES libraries(id),
 identity BLOB NOT NULL,conflicts INTEGER NOT NULL DEFAULT 0 CHECK(conflicts>=0),
 created_at INTEGER NOT NULL
);
CREATE TABLE merge_reviews(
 id TEXT PRIMARY KEY,prepared_digest TEXT NOT NULL REFERENCES prepared_merges(digest),
 issuer TEXT NOT NULL,subject TEXT NOT NULL,verdict TEXT NOT NULL CHECK(verdict IN('approve','changes_requested','reject')),
 rationale TEXT NOT NULL,created_at INTEGER NOT NULL
);
CREATE TABLE library_merge_policies(
 library_id TEXT PRIMARY KEY REFERENCES libraries(id),revision INTEGER NOT NULL,
 digest TEXT NOT NULL,minimum_approvals INTEGER NOT NULL CHECK(minimum_approvals>0)
);
CREATE TABLE prepared_sources(
 prepared_digest TEXT NOT NULL REFERENCES prepared_merges(digest),
 branch_id TEXT NOT NULL REFERENCES branches(id),head TEXT NOT NULL,
 PRIMARY KEY(prepared_digest,branch_id)
);
CREATE TABLE merge_operations(
 id TEXT PRIMARY KEY,library_id TEXT NOT NULL REFERENCES libraries(id),
 prepared_digest TEXT NOT NULL UNIQUE REFERENCES prepared_merges(digest),
 old_head TEXT NOT NULL,new_head TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN('prepared','settled'))
);
CREATE TABLE conflict_workspaces(
 id TEXT PRIMARY KEY,library_id TEXT NOT NULL REFERENCES libraries(id),
 branch_id TEXT NOT NULL REFERENCES branches(id),base_commit TEXT NOT NULL,
 main_commit TEXT NOT NULL,source_commit TEXT NOT NULL,policy_revision INTEGER NOT NULL,
 policy_digest TEXT NOT NULL,conflicts BLOB NOT NULL,revision INTEGER NOT NULL DEFAULT 1,
 state TEXT NOT NULL CHECK(state IN('open','resolved')),created_at INTEGER NOT NULL
);
CREATE TABLE conflict_resolutions(
 workspace_id TEXT NOT NULL REFERENCES conflict_workspaces(id),path TEXT NOT NULL,
 choice TEXT NOT NULL CHECK(choice IN('main','branch','delete','text')),
 text TEXT NOT NULL,PRIMARY KEY(workspace_id,path)
);

CREATE TABLE evolution_prefix_reservations (
 library_id TEXT NOT NULL REFERENCES libraries(id),
 prefix TEXT NOT NULL,
 owner_group_id TEXT NOT NULL,
 binding_id TEXT NOT NULL,
 generation INTEGER NOT NULL CHECK(generation>0),
 state TEXT NOT NULL CHECK(state IN('pending','active','released')),
 operation_id TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(library_id,prefix),
 UNIQUE(operation_id,binding_id)
);
CREATE INDEX evolution_prefix_owner ON evolution_prefix_reservations(owner_group_id,state);

CREATE TRIGGER immutable_prepared_identity
BEFORE UPDATE OF digest,library_id,identity,created_at ON prepared_merges
BEGIN SELECT RAISE(ABORT,'immutable prepared integration identity'); END;
CREATE TRIGGER immutable_merge_review
BEFORE UPDATE ON merge_reviews
BEGIN SELECT RAISE(ABORT,'append a new review decision'); END;
CREATE TRIGGER retained_merge_review
BEFORE DELETE ON merge_reviews
BEGIN SELECT RAISE(ABORT,'retain review provenance'); END;
CREATE TRIGGER immutable_prepared_sources
BEFORE UPDATE ON prepared_sources
BEGIN SELECT RAISE(ABORT,'immutable prepared source identity'); END;

CREATE TABLE evolution_reservation_operations (
 operation_id TEXT PRIMARY KEY,
 group_id TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0),
 digest TEXT NOT NULL,
 body TEXT NOT NULL CHECK(json_valid(body)),
 state TEXT NOT NULL CHECK(state IN ('reserved','adopted','released'))
);

CREATE TABLE evolution_reservation_candidates (
 library_id TEXT NOT NULL,
 prefix TEXT NOT NULL,
 operation_id TEXT NOT NULL,
 previous_operation_id TEXT NOT NULL,
 generation INTEGER NOT NULL CHECK(generation>1),
 PRIMARY KEY(library_id,prefix),
 FOREIGN KEY(library_id,prefix) REFERENCES evolution_prefix_reservations(library_id,prefix)
);

CREATE TRIGGER immutable_evolution_reservation_identity
BEFORE UPDATE OF operation_id,group_id,revision,digest,body ON evolution_reservation_operations
BEGIN SELECT RAISE(ABORT,'immutable reservation operation identity'); END;
CREATE TRIGGER retained_evolution_reservation_operation
BEFORE DELETE ON evolution_reservation_operations
BEGIN SELECT RAISE(ABORT,'retain reservation operation provenance'); END;
CREATE TRIGGER terminal_evolution_reservation_operation
BEFORE UPDATE OF state ON evolution_reservation_operations
WHEN OLD.state<>NEW.state AND OLD.state<>'reserved'
BEGIN SELECT RAISE(ABORT,'terminal reservation operation'); END;
