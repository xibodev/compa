package evolution

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xibodev/compa/v3/pkg/fileutil"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/skills"
)

// recordRetention is how long the store keeps a task or pattern record after
// it last changed.
const recordRetention = 30 * 24 * time.Hour

type Store struct {
	paths Paths
	// now is the clock of record retention: with one, loading or rewriting
	// the task or pattern records drops those older than recordRetention,
	// from the file too. Without one every record is kept. The runtime's
	// stores run on its clock.
	now func() time.Time
}

func NewStore(paths Paths) *Store {
	return &Store{paths: paths}
}

// withClock returns the store for the same paths with now as the clock of
// record retention.
func (s *Store) withClock(now func() time.Time) *Store {
	return &Store{paths: s.paths, now: now}
}

var storeFileLocks sync.Map

func (s *Store) AppendLearningRecord(ctx context.Context, record LearningRecord) error {
	switch record.Kind {
	case RecordKindPattern:
		return s.AppendPatternRecords([]LearningRecord{record})
	default:
		return s.AppendTaskRecord(ctx, record)
	}
}

func (s *Store) AppendLearningRecords(records []LearningRecord) error {
	taskRecords := make([]LearningRecord, 0, len(records))
	patternRecords := make([]LearningRecord, 0, len(records))
	for _, record := range records {
		switch record.Kind {
		case RecordKindPattern:
			patternRecords = append(patternRecords, record)
		default:
			taskRecords = append(taskRecords, record)
		}
	}
	if err := s.AppendTaskRecords(context.Background(), taskRecords); err != nil {
		return err
	}
	return s.AppendPatternRecords(patternRecords)
}

func (s *Store) AppendTaskRecord(ctx context.Context, record LearningRecord) error {
	return s.AppendTaskRecords(ctx, []LearningRecord{record})
}

func (s *Store) AppendTaskRecords(ctx context.Context, records []LearningRecord) error {
	return s.appendJSONLRecords(ctx, s.paths.TaskRecords, records)
}

func (s *Store) AppendPatternRecords(records []LearningRecord) error {
	return s.appendJSONLRecords(context.Background(), s.paths.PatternRecords, records)
}

func (s *Store) appendJSONLRecords(ctx context.Context, path string, records []LearningRecord) error {
	if len(records) == 0 {
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	unlock := lockStoreFile(path)
	defer unlock()

	if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr != nil {
		return mkdirErr
	}
	// A crash mid-write can leave a partial last line; start on a fresh line
	// so the new record isn't glued onto it.
	needsNewline, err := lacksTrailingNewline(path)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if needsNewline {
		if _, err := f.Write([]byte{'\n'}); err != nil {
			return err
		}
	}

	enc := json.NewEncoder(f)
	for _, record := range records {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := enc.Encode(record); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) LoadLearningRecords() ([]LearningRecord, error) {
	taskRecords, err := s.LoadTaskRecords()
	if err != nil {
		return nil, err
	}
	patternRecords, err := s.LoadPatternRecords()
	if err != nil {
		return nil, err
	}
	return append(taskRecords, patternRecords...), nil
}

func (s *Store) LoadTaskRecords() ([]LearningRecord, error) {
	return s.loadRecords(s.paths.TaskRecords)
}

func (s *Store) LoadPatternRecords() ([]LearningRecord, error) {
	return s.loadRecords(s.paths.PatternRecords)
}

// loadRecords loads the records at path. When it left lines of the file out,
// the file is rewritten without them.
func (s *Store) loadRecords(path string) ([]LearningRecord, error) {
	unlock := lockStoreFile(path)
	defer unlock()
	records, unsaved, dropped, err := s.loadRecordsFromPath(path)
	if err != nil || !dropped {
		return records, err
	}
	return records, s.saveJSONLRecordsLocked(path, records, unsaved)
}

// loadRecordsFromPath reads the records at path, without those past
// retention or corrupt. Corrupt lines are saved to <path>.corrupt; unsaved
// holds those that couldn't be, for a rewrite of the file to keep. dropped
// reports that a rewrite would leave lines out: records past retention, or
// corrupt lines saved aside.
func (s *Store) loadRecordsFromPath(path string) (
	records []LearningRecord, unsaved [][]byte, dropped bool, err error,
) {
	var cutoff time.Time
	if s.now != nil {
		cutoff = s.now().Add(-recordRetention)
	}
	corrupt, err := decodeJSONLLines(path, func(line []byte) error {
		var record LearningRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return err
		}
		if !cutoff.IsZero() && recordExpired(record, cutoff) {
			dropped = true
			return nil
		}
		records = append(records, record)
		return nil
	})
	if err != nil {
		return nil, nil, false, err
	}
	if len(corrupt) > 0 {
		// Keep loading what is readable instead of failing every run, and
		// keep the bad lines next to the file for inspection.
		logger.WarnCF("evolution", "Skipped corrupt lines in evolution store", map[string]any{
			"path":  path,
			"lines": len(corrupt),
		})
		if qErr := quarantineLines(path, corrupt); qErr != nil {
			logger.WarnCF("evolution", "Could not save corrupt evolution store lines", map[string]any{
				"path":  path,
				"error": qErr.Error(),
			})
			unsaved = corrupt
		} else {
			dropped = true
		}
	}
	return records, unsaved, dropped, nil
}

// recordExpired reports whether record last changed before cutoff. A record
// without a time is kept.
func recordExpired(record LearningRecord, cutoff time.Time) bool {
	changed := record.CreatedAt
	if record.UpdatedAt != nil && record.UpdatedAt.After(changed) {
		changed = *record.UpdatedAt
	}
	return !changed.IsZero() && changed.Before(cutoff)
}

// quarantineLines appends lines to <path>.corrupt, which keeps each distinct
// line once.
func quarantineLines(path string, lines [][]byte) error {
	qPath := path + ".corrupt"
	existing, err := os.ReadFile(qPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	seen := make(map[string]struct{})
	for _, line := range bytes.Split(existing, []byte{'\n'}) {
		seen[string(bytes.TrimSpace(line))] = struct{}{}
	}
	var buf bytes.Buffer
	for _, line := range lines {
		if _, ok := seen[string(line)]; ok {
			continue
		}
		seen[string(line)] = struct{}{}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if buf.Len() == 0 {
		return nil
	}
	f, err := os.OpenFile(qPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// lacksTrailingNewline reports whether path is a non-empty file whose last
// byte isn't a newline.
func lacksTrailingNewline(path string) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return false, err
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, info.Size()-1); err != nil {
		return false, err
	}
	return last[0] != '\n', nil
}

func (s *Store) SaveTaskRecords(records []LearningRecord) error {
	return s.saveJSONLRecords(s.paths.TaskRecords, records)
}

// UpdateTaskRecords loads the task records, lets update change them in place,
// and saves them when update reports a change or the load left lines out. The
// file stays locked throughout, so concurrent appends aren't lost.
func (s *Store) UpdateTaskRecords(update func(records []LearningRecord) bool) error {
	unlock := lockStoreFile(s.paths.TaskRecords)
	defer unlock()

	records, unsaved, dropped, err := s.loadRecordsFromPath(s.paths.TaskRecords)
	if err != nil {
		return err
	}
	if !update(records) && !dropped {
		return nil
	}
	return s.saveJSONLRecordsLocked(s.paths.TaskRecords, records, unsaved)
}

func (s *Store) MarkTaskRecordsClustered(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	target := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		target[id] = struct{}{}
	}
	if len(target) == 0 {
		return nil
	}

	unlock := lockStoreFile(s.paths.TaskRecords)
	defer unlock()

	records, unsaved, dropped, err := s.loadRecordsFromPath(s.paths.TaskRecords)
	if err != nil {
		return err
	}

	hasTargetRecordInWorkspace := make(map[string]bool, len(target))
	if strings.TrimSpace(s.paths.Workspace) != "" {
		for _, record := range records {
			if _, ok := target[record.ID]; !ok {
				continue
			}
			if record.WorkspaceID == s.paths.Workspace {
				hasTargetRecordInWorkspace[record.ID] = true
			}
		}
	}

	changed := false
	for i := range records {
		if _, ok := target[records[i].ID]; !ok {
			continue
		}
		if hasTargetRecordInWorkspace[records[i].ID] && records[i].WorkspaceID != s.paths.Workspace {
			continue
		}
		records[i].Status = RecordStatus("clustered")
		changed = true
	}
	if !changed && !dropped {
		return nil
	}
	return s.saveJSONLRecordsLocked(s.paths.TaskRecords, records, unsaved)
}

func (s *Store) SavePatternRecords(records []LearningRecord) error {
	return s.saveJSONLRecords(s.paths.PatternRecords, records)
}

func (s *Store) MergePatternRecords(records []LearningRecord) error {
	if len(records) == 0 {
		return nil
	}

	unlock := lockStoreFile(s.paths.PatternRecords)
	defer unlock()

	current, unsaved, _, err := s.loadRecordsFromPath(s.paths.PatternRecords)
	if err != nil {
		return err
	}
	merged := mergeLearningRecordsByID(current, records)
	return s.saveJSONLRecordsLocked(s.paths.PatternRecords, merged, unsaved)
}

func (s *Store) saveJSONLRecords(path string, records []LearningRecord) error {
	unlock := lockStoreFile(path)
	defer unlock()

	return s.saveJSONLRecordsLocked(path, records, nil)
}

// saveJSONLRecordsLocked writes records to path, then the corrupt lines in
// unsaved as they were.
func (s *Store) saveJSONLRecordsLocked(path string, records []LearningRecord, unsaved [][]byte) error {
	if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr != nil {
		return mkdirErr
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, record := range records {
		if err := enc.Encode(record); err != nil {
			return err
		}
	}
	for _, line := range unsaved {
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return fileutil.WriteFileAtomic(path, buf.Bytes(), 0o644)
}

func mergeLearningRecordsByID(base, updates []LearningRecord) []LearningRecord {
	out := append([]LearningRecord(nil), base...)
	indexByID := make(map[string]int, len(out)+len(updates))
	for i, record := range out {
		key := learningRecordMergeKey(record)
		if key == "" {
			continue
		}
		indexByID[key] = i
	}
	for _, record := range updates {
		key := learningRecordMergeKey(record)
		if key == "" {
			out = append(out, record)
			continue
		}
		if idx, ok := indexByID[key]; ok {
			out[idx] = record
			continue
		}
		indexByID[key] = len(out)
		out = append(out, record)
	}
	return out
}

func learningRecordMergeKey(record LearningRecord) string {
	id := strings.TrimSpace(record.ID)
	if id == "" {
		return ""
	}
	return strings.TrimSpace(record.WorkspaceID) + "\x00" + id
}

func (s *Store) SaveDrafts(drafts []SkillDraft) error {
	unlock := lockStoreFile(s.paths.SkillDrafts)
	defer unlock()

	existing, err := s.LoadDrafts()
	if err != nil {
		return err
	}

	indexByKey := make(map[string]int, len(existing))
	for i, draft := range existing {
		indexByKey[draftKey(draft.WorkspaceID, draft.ID)] = i
	}

	for _, draft := range drafts {
		key := draftKey(draft.WorkspaceID, draft.ID)
		if idx, ok := indexByKey[key]; ok {
			existing[idx] = draft
			continue
		}
		indexByKey[key] = len(existing)
		existing = append(existing, draft)
	}

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(s.paths.SkillDrafts, data, 0o644)
}

func (s *Store) LoadDrafts() ([]SkillDraft, error) {
	data, err := os.ReadFile(s.paths.SkillDrafts)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}

	var drafts []SkillDraft
	if err := json.Unmarshal(data, &drafts); err != nil {
		return nil, err
	}
	return drafts, nil
}

func (s *Store) SaveProfile(profile SkillProfile) error {
	path, err := s.profilePath(profile.WorkspaceID, profile.SkillName)
	if err != nil {
		return err
	}
	unlock := lockStoreFile(path)
	defer unlock()

	if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr != nil {
		return mkdirErr
	}

	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(path, data, 0o644)
}

func (s *Store) LoadProfile(skillName string) (SkillProfile, error) {
	return s.loadProfileForWorkspace(strings.TrimSpace(s.paths.Workspace), skillName)
}

func (s *Store) UpdateProfile(
	workspaceID, skillName string,
	update func(profile *SkillProfile, exists bool) error,
) error {
	targetPath, err := s.profilePath(workspaceID, skillName)
	if err != nil {
		return err
	}

	unlock := lockStoreFile(targetPath)
	defer unlock()

	profile, err := s.loadProfileForWorkspace(workspaceID, skillName)
	exists := err == nil
	if errors.Is(err, os.ErrNotExist) {
		profile = SkillProfile{}
	} else if err != nil {
		return err
	}

	if updateErr := update(&profile, exists); updateErr != nil {
		return updateErr
	}
	if !exists && isZeroSkillProfile(profile) {
		return nil
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(targetPath), 0o755); mkdirErr != nil {
		return mkdirErr
	}

	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(targetPath, data, 0o644)
}

func (s *Store) loadProfileForWorkspace(workspaceID, skillName string) (SkillProfile, error) {
	paths, err := s.profileLookupPaths(workspaceID, skillName)
	if err != nil {
		return SkillProfile{}, err
	}
	for _, path := range paths {
		profile, loadErr := s.loadProfileFromPath(path)
		if errors.Is(loadErr, os.ErrNotExist) {
			continue
		}
		if loadErr != nil {
			return SkillProfile{}, loadErr
		}
		return profile, nil
	}
	return SkillProfile{}, os.ErrNotExist
}

func isZeroSkillProfile(profile SkillProfile) bool {
	return profile.SkillName == "" &&
		profile.WorkspaceID == "" &&
		profile.CurrentVersion == "" &&
		profile.Status == "" &&
		profile.Origin == "" &&
		profile.HumanSummary == "" &&
		profile.ChangeReason == "" &&
		len(profile.IntendedUseCases) == 0 &&
		len(profile.PreferredEntryPath) == 0 &&
		len(profile.AvoidPatterns) == 0 &&
		profile.LastUsedAt.IsZero() &&
		profile.UseCount == 0 &&
		profile.RetentionScore == 0 &&
		len(profile.VersionHistory) == 0
}

func (s *Store) LoadProfiles() ([]SkillProfile, error) {
	entries, err := os.ReadDir(s.paths.ProfilesDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	profiles := make([]SkillProfile, 0, len(entries))
	for _, entry := range entries {
		entryPath := filepath.Join(s.paths.ProfilesDir, entry.Name())
		if entry.IsDir() {
			nestedProfiles, loadErr := s.loadProfilesFromDir(entryPath)
			if loadErr != nil {
				return nil, loadErr
			}
			profiles = append(profiles, nestedProfiles...)
			continue
		}
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		profile, err := s.loadProfileFromPath(entryPath)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}

	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].SkillName != profiles[j].SkillName {
			return profiles[i].SkillName < profiles[j].SkillName
		}
		return profiles[i].WorkspaceID < profiles[j].WorkspaceID
	})
	return profiles, nil
}

// decodeJSONLLines decodes each non-empty line of path. Lines that aren't
// valid JSON records are skipped and returned, so one damaged line (from a
// crash mid-write) doesn't make the whole store unreadable.
func decodeJSONLLines(path string, decode func(line []byte) error) ([][]byte, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lines [][]byte
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		lines = append(lines, append([]byte(nil), line...))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	var corrupt [][]byte
	for _, line := range lines {
		if err := decode(line); err != nil {
			if isInvalidJSON(err) {
				corrupt = append(corrupt, line)
				continue
			}
			return nil, err
		}
	}
	return corrupt, nil
}

func draftKey(workspaceID, id string) string {
	return workspaceID + "\x00" + id
}

func isInvalidJSON(err error) bool {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &syntaxErr) || errors.As(err, &typeErr) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

func lockStoreFile(path string) func() {
	for {
		actual, _ := storeFileLocks.LoadOrStore(path, &sync.Mutex{})
		mu, ok := actual.(*sync.Mutex)
		if !ok || mu == nil {
			// Corrupted entry (wrong type or nil *sync.Mutex).
			// Atomically swap in a fresh mutex via CompareAndSwap.
			// If CAS fails, another goroutine already replaced it —
			// just retry the loop to pick up the valid entry.
			storeFileLocks.CompareAndSwap(path, actual, &sync.Mutex{})
			continue
		}
		mu.Lock()
		return mu.Unlock
	}
}

func (s *Store) profilePath(workspaceID, skillName string) (string, error) {
	if err := skills.ValidateSkillName(skillName); err != nil {
		return "", err
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return filepath.Join(s.paths.ProfilesDir, skillName+".json"), nil
	}
	return filepath.Join(s.paths.ProfilesDir, workspaceScopeDir(workspaceID), skillName+".json"), nil
}

func (s *Store) loadProfilesFromDir(dir string) ([]SkillProfile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	profiles := make([]SkillProfile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		profile, err := s.loadProfileFromPath(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

func (s *Store) loadProfileFromPath(path string) (SkillProfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SkillProfile{}, err
	}

	var profile SkillProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return SkillProfile{}, err
	}
	return profile, nil
}

func (s *Store) profileLookupPaths(workspaceID, skillName string) ([]string, error) {
	if err := skills.ValidateSkillName(skillName); err != nil {
		return nil, err
	}

	paths := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	appendPath := func(path string) {
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		paths = append(paths, path)
		seen[path] = struct{}{}
	}

	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID != "" {
		path, err := s.profilePath(workspaceID, skillName)
		if err != nil {
			return nil, err
		}
		appendPath(path)
		if !usesDefaultWorkspaceState(s.paths, workspaceID) {
			return paths, nil
		}
	}

	unscopedPath, err := s.profilePath("", skillName)
	if err != nil {
		return nil, err
	}
	appendPath(unscopedPath)
	return paths, nil
}

func workspaceScopeDir(workspaceID string) string {
	sum := sha1.Sum([]byte(workspaceID))
	base := filepath.Base(filepath.Clean(workspaceID))
	base = sanitizeWorkspaceComponent(base)
	if base == "" || base == "." {
		base = "workspace"
	}
	return base + "-" + hex.EncodeToString(sum[:6])
}

func sanitizeWorkspaceComponent(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
