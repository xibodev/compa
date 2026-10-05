package evolution_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/pkg/evolution"
	"github.com/xibodev/compa/pkg/providers"
)

type llmClusterTestProvider struct {
	content      string
	defaultModel string
	messages     []providers.Message
}

func (p *llmClusterTestProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.messages = append([]providers.Message(nil), messages...)
	return &providers.LLMResponse{Content: p.content}, nil
}

func (p *llmClusterTestProvider) GetDefaultModel() string {
	return p.defaultModel
}

func TestHeuristicPatternClusterer_GroupsChineseSummariesWithoutLLM(t *testing.T) {
	clusterer := evolution.NewHeuristicPatternClusterer(3, func() time.Time {
		return time.Unix(1700000000, 0).UTC()
	})
	success := true
	tasks := []evolution.LearningRecord{
		{
			ID:          "task-1",
			Kind:        evolution.RecordKindTask,
			WorkspaceID: "workspace",
			Summary:     "调用三一定理计算100",
			FinalOutput: "100 + 31 = 131; 131 + 42 = 173; 173 - 53 = 120",
			Status:      evolution.RecordStatus("new"),
			Success:     &success,
		},
		{
			ID:          "task-2",
			Kind:        evolution.RecordKindTask,
			WorkspaceID: "workspace",
			Summary:     "调用三一定理计算200",
			FinalOutput: "200 + 31 = 231; 231 + 42 = 273; 273 - 53 = 220",
			Status:      evolution.RecordStatus("new"),
			Success:     &success,
		},
		{
			ID:             "task-3",
			Kind:           evolution.RecordKindTask,
			WorkspaceID:    "workspace",
			Summary:        "调用三一定理计算300",
			FinalOutput:    "300 + 31 = 331; 331 + 42 = 373; 373 - 53 = 320",
			Status:         evolution.RecordStatus("new"),
			Success:        &success,
			UsedSkillNames: []string{"three-one-theorem", "four-two-theorem", "five-three-theorem"},
		},
	}

	patterns, clusteredIDs, err := clusterer.BuildPatterns(context.Background(), "workspace", tasks, nil)
	if err != nil {
		t.Fatalf("BuildPatterns: %v", err)
	}
	if len(patterns) != 1 {
		t.Fatalf("len(patterns) = %d, want 1: %#v", len(patterns), patterns)
	}
	if !strings.HasPrefix(patterns[0].Label, "task-") {
		t.Fatalf("Label = %q, want task-* fallback label", patterns[0].Label)
	}
	if patterns[0].Summary != "调用三一定理计算100" {
		t.Fatalf("Summary = %q, want representative Chinese summary", patterns[0].Summary)
	}
	if len(patterns[0].TaskRecordIDs) != 3 {
		t.Fatalf("TaskRecordIDs = %v, want 3 ids", patterns[0].TaskRecordIDs)
	}
	if len(clusteredIDs) != 3 {
		t.Fatalf("clusteredIDs = %v, want 3 ids", clusteredIDs)
	}
}

func TestLLMPatternClusterer_FallsBackWhenLLMReturnsNoUsableClusters(t *testing.T) {
	fallback := evolution.NewHeuristicPatternClusterer(2, func() time.Time {
		return time.Unix(1700000000, 0).UTC()
	})
	clusterer := evolution.NewLLMPatternClusterer(
		&llmClusterTestProvider{content: `{"clusters":[]}`, defaultModel: "test-model"},
		"test-model",
		fallback,
		2,
		func() time.Time { return time.Unix(1700000000, 0).UTC() },
	)
	success := true
	tasks := []evolution.LearningRecord{
		{
			ID:          "task-1",
			Kind:        evolution.RecordKindTask,
			WorkspaceID: "workspace",
			Summary:     "调用三一定理计算100",
			FinalOutput: "100 + 31 = 131",
			Status:      evolution.RecordStatus("new"),
			Success:     &success,
		},
		{
			ID:          "task-2",
			Kind:        evolution.RecordKindTask,
			WorkspaceID: "workspace",
			Summary:     "调用三一定理计算200",
			FinalOutput: "200 + 31 = 231",
			Status:      evolution.RecordStatus("new"),
			Success:     &success,
		},
	}

	patterns, clusteredIDs, err := clusterer.BuildPatterns(context.Background(), "workspace", tasks, nil)
	if err != nil {
		t.Fatalf("BuildPatterns: %v", err)
	}
	if len(patterns) != 1 {
		t.Fatalf("len(patterns) = %d, want fallback pattern: %#v", len(patterns), patterns)
	}
	if len(clusteredIDs) != 2 {
		t.Fatalf("clusteredIDs = %v, want 2 task IDs", clusteredIDs)
	}
}

func TestLLMPatternClusterer_PromptFiltersExistingPatternsByWorkspace(t *testing.T) {
	provider := &llmClusterTestProvider{
		content:      `{"clusters":[{"label":"current-weather-path","summary":"current summary","task_record_ids":["task-1"],"cluster_reason":"same goal"}]}`,
		defaultModel: "test-model",
	}
	clusterer := evolution.NewLLMPatternClusterer(
		provider,
		"test-model",
		evolution.NewHeuristicPatternClusterer(1, nil),
		1,
		func() time.Time { return time.Unix(1700000000, 0).UTC() },
	)
	success := true
	tasks := []evolution.LearningRecord{
		{
			ID:          "task-1",
			Kind:        evolution.RecordKindTask,
			WorkspaceID: "workspace-a",
			Summary:     "weather lookup",
			FinalOutput: "sunny",
			Status:      evolution.RecordStatus("new"),
			Success:     &success,
		},
	}
	existing := []evolution.LearningRecord{
		{
			ID:          "rule-a",
			Kind:        evolution.RecordKindPattern,
			WorkspaceID: "workspace-a",
			Label:       "current-weather-path",
			Summary:     "current workspace pattern",
		},
		{
			ID:          "rule-b",
			Kind:        evolution.RecordKindPattern,
			WorkspaceID: "workspace-b",
			Label:       "other-workspace-secret-path",
			Summary:     "other workspace pattern",
		},
	}

	if _, _, err := clusterer.BuildPatterns(context.Background(), "workspace-a", tasks, existing); err != nil {
		t.Fatalf("BuildPatterns: %v", err)
	}
	if len(provider.messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(provider.messages))
	}
	prompt := provider.messages[1].Content
	if !strings.Contains(prompt, "current-weather-path") {
		t.Fatalf("prompt = %q, want current workspace pattern", prompt)
	}
	if strings.Contains(prompt, "other-workspace-secret-path") || strings.Contains(prompt, "other workspace pattern") {
		t.Fatalf("prompt leaked other workspace pattern: %s", prompt)
	}
}

func TestLLMPatternClusterer_RejectsClusterBelowEvidenceSuccessRatio(t *testing.T) {
	provider := &llmClusterTestProvider{
		content:      `{"clusters":[{"label":"weather-lookup","summary":"lookup weather","task_record_ids":["task-success","task-failed"],"cluster_reason":"same weather lookup goal"}]}`,
		defaultModel: "test-model",
	}
	clusterer := evolution.NewLLMPatternClusterer(
		provider,
		"test-model",
		evolution.NewHeuristicPatternClusterer(1, nil),
		1,
		func() time.Time { return time.Unix(1700000000, 0).UTC() },
	)
	success := true
	failed := false
	successfulTasks := []evolution.LearningRecord{
		{
			ID:          "task-success",
			Kind:        evolution.RecordKindTask,
			WorkspaceID: "workspace-a",
			Summary:     "weather lookup shanghai",
			FinalOutput: "sunny",
			Status:      evolution.RecordStatus("new"),
			Success:     &success,
		},
	}
	evidenceTasks := []evolution.LearningRecord{
		successfulTasks[0],
		{
			ID:          "task-failed",
			Kind:        evolution.RecordKindTask,
			WorkspaceID: "workspace-a",
			Summary:     "forecast for shanghai",
			FinalOutput: "could not complete",
			Status:      evolution.RecordStatus("new"),
			Success:     &failed,
		},
	}

	patterns, clusteredIDs, err := clusterer.BuildPatternsWithEvidence(
		context.Background(),
		"workspace-a",
		successfulTasks,
		evidenceTasks,
		nil,
		0.8,
	)
	if err != nil {
		t.Fatalf("BuildPatternsWithEvidence: %v", err)
	}
	if len(patterns) != 0 {
		t.Fatalf("len(patterns) = %d, want 0: %#v", len(patterns), patterns)
	}
	if len(clusteredIDs) != 0 {
		t.Fatalf("clusteredIDs = %v, want none", clusteredIDs)
	}
	prompt := provider.messages[1].Content
	if !strings.Contains(prompt, `"success": true`) || !strings.Contains(prompt, `"success": false`) {
		t.Fatalf("prompt should include success and failure evidence:\n%s", prompt)
	}
}

// The model's valid clusters are kept when it leaves records out, and the
// records it left out are clustered the simple way instead of every record
// being dropped and sent again on the next run (EV-07).
func TestLLMPatternClusterer_ClustersUnassignedRecordsWithTheFallback(t *testing.T) {
	provider := &llmClusterTestProvider{
		content:      `{"clusters":[{"label":"weather-lookup","summary":"lookup weather","task_record_ids":["task-weather"],"cluster_reason":"same weather lookup goal"}]}`,
		defaultModel: "test-model",
	}
	clusterer := evolution.NewLLMPatternClusterer(
		provider,
		"test-model",
		evolution.NewHeuristicPatternClusterer(2, nil),
		1,
		func() time.Time { return time.Unix(1700000000, 0).UTC() },
	)
	success := true
	task := func(id, summary string) evolution.LearningRecord {
		return evolution.LearningRecord{
			ID:          id,
			Kind:        evolution.RecordKindTask,
			WorkspaceID: "workspace-a",
			Summary:     summary,
			FinalOutput: "done",
			Status:      evolution.RecordStatus("new"),
			Success:     &success,
		}
	}
	tasks := []evolution.LearningRecord{
		task("task-weather", "weather lookup shanghai"),
		task("task-report-1", "convert report 1"),
		task("task-report-2", "convert report 2"),
	}

	patterns, clusteredIDs, err := clusterer.BuildPatternsWithEvidence(
		context.Background(),
		"workspace-a",
		tasks,
		tasks,
		nil,
		0.5,
	)
	if err != nil {
		t.Fatalf("BuildPatternsWithEvidence: %v", err)
	}
	byLabel := make(map[string]evolution.LearningRecord, len(patterns))
	for _, pattern := range patterns {
		byLabel[pattern.Label] = pattern
	}
	if got := strings.Join(byLabel["weather-lookup"].TaskRecordIDs, ","); got != "task-weather" {
		t.Fatalf("model pattern TaskRecordIDs = %q, want the model's cluster kept; patterns = %#v", got, patterns)
	}
	if got := strings.Join(byLabel["convert-report"].TaskRecordIDs, ","); got != "task-report-1,task-report-2" {
		t.Fatalf("fallback pattern TaskRecordIDs = %q, want the unassigned records; patterns = %#v", got, patterns)
	}
	if len(patterns) != 2 {
		t.Fatalf("len(patterns) = %d, want 2: %#v", len(patterns), patterns)
	}
	if got := strings.Join(clusteredIDs, ","); got != "task-weather,task-report-1,task-report-2" {
		t.Fatalf("clusteredIDs = %v, want every record", clusteredIDs)
	}

	// The same holds without evidence.
	patterns, clusteredIDs, err = clusterer.BuildPatterns(context.Background(), "workspace-a", tasks, nil)
	if err != nil {
		t.Fatalf("BuildPatterns: %v", err)
	}
	if len(patterns) != 2 || len(clusteredIDs) != 3 {
		t.Fatalf("BuildPatterns = %d patterns, clustered %v; want both clusters covering every record", len(patterns), clusteredIDs)
	}
}

// A fallback cluster with the label of one of the model's clusters adds its
// records to that pattern instead of replacing it.
func TestLLMPatternClusterer_FallbackJoinsTheModelsClusterOfTheSameLabel(t *testing.T) {
	provider := &llmClusterTestProvider{
		content:      `{"clusters":[{"label":"convert-report","summary":"convert reports","task_record_ids":["task-report-1"],"cluster_reason":"same goal"}]}`,
		defaultModel: "test-model",
	}
	clusterer := evolution.NewLLMPatternClusterer(
		provider,
		"test-model",
		evolution.NewHeuristicPatternClusterer(2, nil),
		1,
		func() time.Time { return time.Unix(1700000000, 0).UTC() },
	)
	success := true
	tasks := []evolution.LearningRecord{
		{ID: "task-report-1", Kind: evolution.RecordKindTask, WorkspaceID: "w", Summary: "convert report 1", Success: &success},
		{ID: "task-report-2", Kind: evolution.RecordKindTask, WorkspaceID: "w", Summary: "convert report 2", Success: &success},
	}

	patterns, clusteredIDs, err := clusterer.BuildPatternsWithEvidence(context.Background(), "w", tasks, tasks, nil, 0)
	if err != nil {
		t.Fatalf("BuildPatternsWithEvidence: %v", err)
	}
	if len(patterns) != 1 {
		t.Fatalf("len(patterns) = %d, want one pattern: %#v", len(patterns), patterns)
	}
	if got := strings.Join(patterns[0].TaskRecordIDs, ","); got != "task-report-1,task-report-2" {
		t.Fatalf("TaskRecordIDs = %q, want both records in the model's pattern", got)
	}
	if len(clusteredIDs) != 2 {
		t.Fatalf("clusteredIDs = %v, want both records", clusteredIDs)
	}
}

func TestLLMPatternClusterer_MarksAllAcceptedEvidenceClusteredButStoresSuccessfulTaskIDs(t *testing.T) {
	provider := &llmClusterTestProvider{
		content:      `{"clusters":[{"label":"weather-lookup","summary":"lookup weather","task_record_ids":["task-success","task-failed"],"cluster_reason":"same weather lookup goal"}]}`,
		defaultModel: "test-model",
	}
	assertClustererMarksAllAcceptedEvidenceClustered(
		t,
		provider,
		"weather lookup shanghai",
		"forecast for shanghai",
		"could not complete",
		"1",
	)
}

func TestLLMPatternClusterer_FallbackMarksAllAcceptedEvidenceClustered(t *testing.T) {
	provider := &llmClusterTestProvider{
		content:      `not-json`,
		defaultModel: "test-model",
	}
	assertClustererMarksAllAcceptedEvidenceClustered(
		t,
		provider,
		"weather lookup 100",
		"weather lookup 200",
		"partial result",
		"fallback pattern",
	)
}

func assertClustererMarksAllAcceptedEvidenceClustered(
	t *testing.T,
	provider *llmClusterTestProvider,
	successSummary string,
	failedSummary string,
	failedOutput string,
	wantPatternDescription string,
) {
	t.Helper()
	clusterer := evolution.NewLLMPatternClusterer(
		provider,
		"test-model",
		evolution.NewHeuristicPatternClusterer(1, nil),
		1,
		func() time.Time { return time.Unix(1700000000, 0).UTC() },
	)
	success := true
	failed := false
	successfulTasks := []evolution.LearningRecord{
		{
			ID:          "task-success",
			Kind:        evolution.RecordKindTask,
			WorkspaceID: "workspace-a",
			Summary:     successSummary,
			FinalOutput: "sunny",
			Status:      evolution.RecordStatus("new"),
			Success:     &success,
		},
	}
	evidenceTasks := []evolution.LearningRecord{
		successfulTasks[0],
		{
			ID:          "task-failed",
			Kind:        evolution.RecordKindTask,
			WorkspaceID: "workspace-a",
			Summary:     failedSummary,
			FinalOutput: failedOutput,
			Status:      evolution.RecordStatus("new"),
			Success:     &failed,
		},
	}

	patterns, clusteredIDs, err := clusterer.BuildPatternsWithEvidence(
		context.Background(),
		"workspace-a",
		successfulTasks,
		evidenceTasks,
		nil,
		0.5,
	)
	if err != nil {
		t.Fatalf("BuildPatternsWithEvidence: %v", err)
	}
	if len(patterns) != 1 {
		t.Fatalf("len(patterns) = %d, want %s: %#v", len(patterns), wantPatternDescription, patterns)
	}
	if got := strings.Join(patterns[0].TaskRecordIDs, ","); got != "task-success" {
		t.Fatalf("pattern TaskRecordIDs = %v, want only successful task", patterns[0].TaskRecordIDs)
	}
	if got := strings.Join(clusteredIDs, ","); got != "task-success,task-failed" {
		t.Fatalf("clusteredIDs = %v, want all accepted evidence IDs", clusteredIDs)
	}
}
