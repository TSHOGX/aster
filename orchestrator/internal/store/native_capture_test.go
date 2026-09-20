package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/nativecapture"
)

func TestNativeCaptureRequestsOrderedAndReceiptsMatchOriginalSource(t *testing.T) {
	ctx := context.Background()
	st, _, binding, _ := boundExecutionFixtureForDriver(t, "pi")
	for version := 2; version <= 3; version++ {
		turn, err := st.EnqueueTurn(ctx, binding.SessionID, "capture", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.db.Exec(`INSERT INTO native_capture_requests(session_id,state_version,state_digest,generation_id,turn_id,store_id,sandbox_id,epoch,native_id,selected_path,snapshot_path,native_digest,size,created_at) VALUES(?,?,?,?,?,?,?,?,'native','native.jsonl',?,?,42,?)`, binding.SessionID, version, fmt.Sprint(version), binding.GenerationID, turn, binding.StoreID, binding.SandboxID, binding.Epoch, fmt.Sprintf("snapshot-%d", version), strings.Repeat("a", 64), formatTime(time.Now()))
		if err != nil {
			t.Fatal(err)
		}
	}
	requests, err := st.PendingNativeCaptures(ctx, 32)
	if err != nil || len(requests) != 1 || requests[0].StateVersion != 2 {
		t.Fatalf("oldest pending request: %+v %v", requests, err)
	}
	first := requests[0]
	wrong := first
	wrong.Epoch++
	if err := st.CompleteNativeCapture(ctx, wrong, nativecapture.Result{ArchivePath: "archive"}); err == nil {
		t.Fatal("capture receipt crossed original epoch")
	}
	if err := st.CompleteNativeCapture(ctx, first, nativecapture.Result{}); err == nil {
		t.Fatal("missing archive was acknowledged")
	}
	for range 2 {
		if err := st.CompleteNativeCapture(ctx, first, nativecapture.Result{ArchivePath: "archive"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CompleteNativeCapture(ctx, first, nativecapture.Result{ArchivePath: "other"}); err == nil {
		t.Fatal("receipt changed archive identity")
	}
	requests, err = st.PendingNativeCaptures(ctx, 32)
	if err != nil || len(requests) != 1 || requests[0].StateVersion != 3 {
		t.Fatalf("next pending request: %+v %v", requests, err)
	}
	if err := st.CompleteNativeCapture(ctx, requests[0], nativecapture.Result{ArchivePath: "second-archive"}); err != nil {
		t.Fatal(err)
	}
	requests, err = st.PendingNativeCaptures(ctx, 32)
	if err != nil || len(requests) != 0 {
		t.Fatalf("completed capture remained pending: %+v %v", requests, err)
	}
}
