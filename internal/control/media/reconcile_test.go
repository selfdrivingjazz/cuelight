package media

import (
	"reflect"
	"testing"

	"github.com/jmill/cable/internal/media/localcas"
)

func TestPlanCASReconciliationClassifiesTempBlobAndRowCrashes(t *testing.T) {
	result := localcas.ReconcileResult{
		ExpiredPartials:   []string{"/cas/.tmp/z.partial", "/cas/.tmp/a.partial"},
		OrphanDigests:     []string{"bbbb", "aaaa"},
		MissingReferenced: []string{"dddd", "cccc"},
	}
	got := PlanCASReconciliation(result)
	want := []ReconciliationAction{
		{Kind: MarkArtifactMissing, Digest: "cccc"},
		{Kind: MarkArtifactMissing, Digest: "dddd"},
		{Kind: RemoveExpiredTemporary, Path: "/cas/.tmp/a.partial"},
		{Kind: RemoveExpiredTemporary, Path: "/cas/.tmp/z.partial"},
		{Kind: RemoveOrphanBlob, Digest: "aaaa"},
		{Kind: RemoveOrphanBlob, Digest: "bbbb"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reconciliation actions:\n got %#v\nwant %#v", got, want)
	}
}

func TestDecideUploadRecovery(t *testing.T) {
	tests := []struct {
		name     string
		snapshot UploadRecoverySnapshot
		want     UploadRecoveryDecision
	}{
		{
			name:     "open partial resumes",
			snapshot: UploadRecoverySnapshot{Upload: Upload{State: UploadOpen, ReceivedLength: 4}, TemporaryPresent: true},
			want:     RecoveryResumeUpload,
		},
		{
			name:     "open row without partial fails",
			snapshot: UploadRecoverySnapshot{Upload: Upload{State: UploadOpen, ReceivedLength: 4}},
			want:     RecoveryFailUpload,
		},
		{
			name:     "finalizing partial retries CAS convergence",
			snapshot: UploadRecoverySnapshot{Upload: Upload{State: UploadFinalizing}, TemporaryPresent: true},
			want:     RecoveryRetryFinalization,
		},
		{
			name:     "finalizing artifact and blob completes row",
			snapshot: UploadRecoverySnapshot{Upload: Upload{State: UploadFinalizing}, BlobPresent: true, ArtifactRowReady: true},
			want:     RecoveryCompleteFromBlob,
		},
		{
			name:     "complete row without blob marks missing",
			snapshot: UploadRecoverySnapshot{Upload: Upload{State: UploadComplete}, ArtifactRowReady: true},
			want:     RecoveryMarkArtifactMissing,
		},
		{
			name:     "complete upload cleans stale partial",
			snapshot: UploadRecoverySnapshot{Upload: Upload{State: UploadComplete}, TemporaryPresent: true, BlobPresent: true, ArtifactRowReady: true},
			want:     RecoveryCleanTemporary,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := DecideUploadRecovery(test.snapshot); got != test.want {
				t.Fatalf("decision = %q, want %q", got, test.want)
			}
		})
	}
}
