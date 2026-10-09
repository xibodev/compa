package evolution

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xibodev/compa/v4/pkg/logger"
)

// Drafts are written to skills only after a human accepts them (AcceptDraft),
// and only in the "apply" mode: there accepting a draft writes it at once;
// in the other modes the draft is marked approved, and a run in the "apply"
// mode writes it (applyApprovedDrafts). Nothing writes a draft the LLM
// produced on its own.

// FindDraft returns the draft with id in workspace.
func FindDraft(store *Store, workspace, id string) (SkillDraft, error) {
	drafts, err := store.LoadDrafts()
	if err != nil {
		return SkillDraft{}, err
	}
	id = strings.TrimSpace(id)
	for _, draft := range drafts {
		if draft.ID == id && draft.WorkspaceID == workspace {
			return draft, nil
		}
	}
	return SkillDraft{}, fmt.Errorf("draft %q not found", id)
}

// AcceptDraft records a human's acceptance of a candidate draft: the draft
// is marked approved. With write, as the "apply" mode asks, it is also
// written to its skill at once, with a backup of the previous version;
// otherwise it waits for a run in the "apply" mode. A draft that fails
// review or can't be written is quarantined and an error is returned.
func AcceptDraft(
	ctx context.Context, paths Paths, workspace, id string, write bool, now func() time.Time,
) (SkillDraft, error) {
	if now == nil {
		now = time.Now
	}
	store := NewStore(paths)
	draft, err := FindDraft(store, workspace, id)
	if err != nil {
		return SkillDraft{}, err
	}
	switch draft.Status {
	case DraftStatusCandidate, DraftStatusApproved:
	default:
		return draft, fmt.Errorf("draft %q is %s; only candidate or approved drafts can be accepted", id, draft.Status)
	}
	review := ReviewDraft(draft)
	if review.Status != DraftStatusCandidate {
		draft.Status = review.Status
		draft.ReviewNotes = appendUniqueStrings(draft.ReviewNotes, review.ReviewNotes...)
		draft.ScanFindings = appendUniqueStrings(draft.ScanFindings, review.Findings...)
		if saveErr := store.SaveDrafts([]SkillDraft{draft}); saveErr != nil {
			return draft, saveErr
		}
		return draft, fmt.Errorf("draft %q failed review: %s", id, strings.Join(review.Findings, "; "))
	}
	draft.Status = DraftStatusApproved
	draft.ReviewNotes = appendUniqueStrings(draft.ReviewNotes, "accepted by a human")
	stamp := now()
	draft.UpdatedAt = &stamp
	if saveErr := store.SaveDrafts([]SkillDraft{draft}); saveErr != nil {
		return draft, saveErr
	}
	if !write {
		return draft, nil
	}
	rt := &Runtime{now: now}
	return rt.applyCandidateDraft(ctx, workspace, store, NewApplier(paths, now), draft, "manual")
}

// RejectDraft marks a candidate or approved draft rejected, so it is never
// written and no new draft is generated for the same pattern.
func RejectDraft(paths Paths, workspace, id string, now func() time.Time) (SkillDraft, error) {
	if now == nil {
		now = time.Now
	}
	store := NewStore(paths)
	draft, err := FindDraft(store, workspace, id)
	if err != nil {
		return SkillDraft{}, err
	}
	switch draft.Status {
	case DraftStatusCandidate, DraftStatusApproved, DraftStatusQuarantined:
	default:
		return draft, fmt.Errorf("draft %q is %s and can't be rejected", id, draft.Status)
	}
	draft.Status = DraftStatusRejected
	draft.ReviewNotes = appendUniqueStrings(draft.ReviewNotes, "rejected by a human")
	stamp := now()
	draft.UpdatedAt = &stamp
	return draft, store.SaveDrafts([]SkillDraft{draft})
}

// saveJudgements stores success-judge decisions on their task records, so
// later runs reuse them instead of judging every record again. A failure only
// costs a re-judge, so it is logged.
func (rt *Runtime) saveJudgements(workspace string, judged map[string]bool) {
	err := rt.storeForWorkspace(workspace).UpdateTaskRecords(func(records []LearningRecord) bool {
		changed := false
		for i := range records {
			decision, ok := judged[records[i].ID]
			if !ok || records[i].WorkspaceID != workspace || records[i].JudgedSuccess != nil {
				continue
			}
			records[i].JudgedSuccess = &decision
			changed = true
		}
		return changed
	})
	if err != nil {
		logger.WarnCF("evolution", "Could not save success judge results", map[string]any{
			"workspace": workspace,
			"error":     err.Error(),
		})
	}
}

// applyApprovedDrafts writes the drafts a human approved (apply mode). The
// approved content is written as reviewed: it is not regenerated or
// normalized again.
func (rt *Runtime) applyApprovedDrafts(
	ctx context.Context, workspace string, store *Store, applier *Applier, runID string,
) error {
	drafts, err := store.LoadDrafts()
	if err != nil {
		return err
	}
	for _, draft := range drafts {
		if draft.WorkspaceID != workspace || draft.Status != DraftStatusApproved {
			continue
		}
		review := ReviewDraft(draft)
		if review.Status != DraftStatusCandidate {
			draft.Status = review.Status
			draft.ScanFindings = appendUniqueStrings(draft.ScanFindings, review.Findings...)
			if saveErr := store.SaveDrafts([]SkillDraft{draft}); saveErr != nil {
				return saveErr
			}
			continue
		}
		if _, applyErr := rt.applyCandidateDraft(ctx, workspace, store, applier, draft, runID); applyErr != nil {
			// The draft is already quarantined with the reason.
			logger.WarnCF("evolution", "Approved draft not applied", map[string]any{
				"workspace": workspace,
				"draft_id":  draft.ID,
				"error":     applyErr.Error(),
				"run_id":    runID,
			})
			return applyErr
		}
	}
	return nil
}
