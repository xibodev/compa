package evolution

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/xibodev/compa/v2/pkg/skills"
)

type LifecycleRunSummary struct {
	EvaluatedProfiles    int
	TransitionedProfiles int
	DeletedSkills        int
}

// retentionHalfLife is how long an unused skill takes to lose half its
// retention score.
const retentionHalfLife = 30 * 24 * time.Hour

// decayedRetention is the retention score after idle time without use.
// Scores only grow while a skill is used, so without decay a skill used a
// few times early would never go cold, however long it then sat unused.
func decayedRetention(score float64, idle time.Duration) float64 {
	if idle <= 0 {
		return score
	}
	return score * math.Pow(0.5, float64(idle)/float64(retentionHalfLife))
}

func NextLifecycleState(profile SkillProfile, now time.Time) SkillStatus {
	if profile.Origin == "manual" || profile.LastUsedAt.IsZero() {
		return profile.Status
	}

	idle := now.Sub(profile.LastUsedAt)
	retention := decayedRetention(profile.RetentionScore, idle)
	switch profile.Status {
	case SkillStatusActive:
		if idle > 90*24*time.Hour && retention < 0.3 {
			return SkillStatusCold
		}
	case SkillStatusCold:
		if idle > 180*24*time.Hour && retention < 0.2 {
			return SkillStatusArchived
		}
	case SkillStatusArchived:
		if idle > 365*24*time.Hour && retention < 0.1 {
			return SkillStatusDeleted
		}
	}

	return profile.Status
}

func ApplyLifecycleState(paths Paths, profile SkillProfile, next SkillStatus) error {
	if next != SkillStatusDeleted {
		return nil
	}

	workspace := profile.WorkspaceID
	if workspace == "" {
		workspace = inferWorkspaceFromPaths(paths)
	}
	if workspace == "" {
		return fmt.Errorf("resolve lifecycle delete workspace for skill %q: workspace is required", profile.SkillName)
	}
	if err := skills.ValidateSkillName(profile.SkillName); err != nil {
		return fmt.Errorf("resolve lifecycle delete skill name: %w", err)
	}

	skillPath := filepath.Join(workspace, "skills", profile.SkillName, "SKILL.md")
	err := os.Remove(skillPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func RunLifecycleOnce(store *Store, paths Paths, workspace string, now time.Time) (LifecycleRunSummary, error) {
	if store == nil {
		return LifecycleRunSummary{}, nil
	}

	profiles, err := store.LoadProfiles()
	if err != nil {
		return LifecycleRunSummary{}, err
	}

	summary := LifecycleRunSummary{}
	for _, profile := range profiles {
		if !profileBelongsToWorkspace(paths, workspace, profile) {
			continue
		}

		summary.EvaluatedProfiles++
		next := NextLifecycleState(profile, now)
		if next == profile.Status {
			continue
		}

		if err := ApplyLifecycleState(paths, profile, next); err != nil {
			return summary, err
		}
		profile.VersionHistory = append(profile.VersionHistory, SkillVersionEntry{
			Version:   profile.CurrentVersion,
			Action:    "lifecycle:" + string(next),
			Timestamp: now,
			Summary:   fmt.Sprintf("lifecycle transition: %s -> %s", profile.Status, next),
		})
		profile.Status = next
		if err := store.SaveProfile(profile); err != nil {
			return summary, err
		}

		summary.TransitionedProfiles++
		if next == SkillStatusDeleted {
			summary.DeletedSkills++
		}
	}

	return summary, nil
}

func inferWorkspaceFromPaths(paths Paths) string {
	root := filepath.Clean(paths.RootDir)
	if filepath.Base(root) != "evolution" {
		return ""
	}
	stateDir := filepath.Dir(root)
	if filepath.Base(stateDir) != "state" {
		return ""
	}
	return filepath.Dir(stateDir)
}

func profileBelongsToWorkspace(paths Paths, workspace string, profile SkillProfile) bool {
	if profile.WorkspaceID == workspace {
		return true
	}
	return profile.WorkspaceID == "" && usesDefaultWorkspaceState(paths, workspace)
}

func usesDefaultWorkspaceState(paths Paths, workspace string) bool {
	return paths.RootDir == NewPaths(workspace, "").RootDir
}
