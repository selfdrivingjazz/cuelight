package media

import (
	"fmt"
	"sort"

	"github.com/jmill/cable/internal/media/localcas"
)

type ReconciliationActionKind string

const (
	RemoveExpiredTemporary ReconciliationActionKind = "remove_expired_temporary"
	RemoveOrphanBlob       ReconciliationActionKind = "remove_orphan_blob"
	MarkArtifactMissing    ReconciliationActionKind = "mark_artifact_missing"
)

type ReconciliationAction struct {
	Kind   ReconciliationActionKind
	Path   string
	Digest string
}

// PlanCASReconciliation turns LocalCAS observations into deterministic durable
// decisions. The caller executes them transactionally or records their failure
// for the next startup pass.
func PlanCASReconciliation(result localcas.ReconcileResult) []ReconciliationAction {
	actions := make([]ReconciliationAction, 0, len(result.ExpiredPartials)+len(result.OrphanDigests)+len(result.MissingReferenced))
	for _, path := range result.ExpiredPartials {
		actions = append(actions, ReconciliationAction{Kind: RemoveExpiredTemporary, Path: path})
	}
	for _, digest := range result.OrphanDigests {
		actions = append(actions, ReconciliationAction{Kind: RemoveOrphanBlob, Digest: digest})
	}
	for _, digest := range result.MissingReferenced {
		actions = append(actions, ReconciliationAction{Kind: MarkArtifactMissing, Digest: digest})
	}
	sort.Slice(actions, func(left, right int) bool {
		leftKey := fmt.Sprintf("%s\x00%s\x00%s", actions[left].Kind, actions[left].Digest, actions[left].Path)
		rightKey := fmt.Sprintf("%s\x00%s\x00%s", actions[right].Kind, actions[right].Digest, actions[right].Path)
		return leftKey < rightKey
	})
	return actions
}

type UploadRecoverySnapshot struct {
	Upload           Upload
	TemporaryPresent bool
	BlobPresent      bool
	ArtifactRowReady bool
}

type UploadRecoveryDecision string

const (
	RecoveryNone                UploadRecoveryDecision = "none"
	RecoveryResumeUpload        UploadRecoveryDecision = "resume_upload"
	RecoveryRetryFinalization   UploadRecoveryDecision = "retry_finalization"
	RecoveryCompleteFromBlob    UploadRecoveryDecision = "complete_from_blob"
	RecoveryFailUpload          UploadRecoveryDecision = "fail_upload"
	RecoveryCleanTemporary      UploadRecoveryDecision = "clean_temporary"
	RecoveryMarkArtifactMissing UploadRecoveryDecision = "mark_artifact_missing"
)

// DecideUploadRecovery classifies the durable state after a process crash. It
// never guesses a digest: CompleteFromBlob is only returned when the caller has
// already associated the observed blob with this upload.
func DecideUploadRecovery(snapshot UploadRecoverySnapshot) UploadRecoveryDecision {
	switch snapshot.Upload.State {
	case UploadOpen:
		if snapshot.Upload.ReceivedLength == 0 || snapshot.TemporaryPresent {
			return RecoveryResumeUpload
		}
		return RecoveryFailUpload
	case UploadFinalizing:
		if snapshot.BlobPresent && snapshot.ArtifactRowReady {
			return RecoveryCompleteFromBlob
		}
		if snapshot.TemporaryPresent {
			return RecoveryRetryFinalization
		}
		return RecoveryFailUpload
	case UploadComplete:
		if !snapshot.BlobPresent || !snapshot.ArtifactRowReady {
			return RecoveryMarkArtifactMissing
		}
		if snapshot.TemporaryPresent {
			return RecoveryCleanTemporary
		}
		return RecoveryNone
	case UploadFailed:
		if snapshot.TemporaryPresent {
			return RecoveryCleanTemporary
		}
		return RecoveryNone
	default:
		return RecoveryFailUpload
	}
}
