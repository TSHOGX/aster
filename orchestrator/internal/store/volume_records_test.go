package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVolumeRecordPersistencePerformsNoProvisioning(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	createStoreSession(t, ctx, st, "record-only")
	root := filepath.Join(t.TempDir(), "must-not-be-created")
	record := SessionWorkspaceVolume{SessionID: "record-only", HostPath: filepath.Join(root, "workspace"), LayoutVersion: 1, SandboxUID: 7000, SandboxGID: 7001, SandboxSupplementalGIDs: []int{}, RuntimeIdentityDigest: "synthetic-identity", ProvisionedAt: time.Now().UTC(), ProvisioningMarkerPath: filepath.Join(root, "marker.json"), ProvisioningMarkerDigest: "synthetic-marker"}
	if err := st.InsertSessionWorkspaceRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSessionWorkspaceRecord(ctx, record.SessionID)
	if err != nil || got.HostPath != record.HostPath {
		t.Fatalf("record: %+v %v", got, err)
	}
	records, err := st.ListSessionWorkspaceRecords(ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("records: %+v %v", records, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("persistence touched filesystem: %v", err)
	}
}
