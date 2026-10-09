package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/modelservice"
)

// runModel runs the model command with args. It returns what the command
// wrote to its stdout and stderr, and what reached the process's stdout
// past them.
func runModel(t *testing.T, args ...string) (stdout, stderr, printed string, err error) {
	t.Helper()
	cmd := NewModelCommand()
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	printed = captureStdout(func() { err = cmd.Execute() })
	return out.String(), errOut.String(), printed, err
}

// decodeOne decodes the only JSON document in data.
func decodeOne(t *testing.T, data string, v any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(data))
	require.NoError(t, dec.Decode(v), "decode %q", data)
	_, err := dec.Token()
	require.ErrorIs(t, err, io.EOF, "%q holds more than one JSON document", data)
}

// twoInstances saves a config with the instances "local" and "cloud".
func twoInstances(t *testing.T) {
	t.Helper()
	path, cfg := testConfig(t)
	cloud := *cfg.ProviderInstances[0]
	cloud.ID = "cloud"
	cloud.Endpoint = "http://127.0.0.1:1/v1"
	cfg.ProviderInstances = append(cfg.ProviderInstances, &cloud)
	require.NoError(t, config.SaveConfig(path, cfg))
}

// stubPing answers each ping with reply, never reaching the network.
func stubPing(t *testing.T, reply func(*config.ProviderInstanceConfig) modelservice.PingResult) {
	t.Helper()
	previous := pingInstance
	pingInstance = func(_ context.Context, inst *config.ProviderInstanceConfig, _ string) modelservice.PingResult {
		return reply(inst)
	}
	t.Cleanup(func() { pingInstance = previous })
}

func cannedPing(inst *config.ProviderInstanceConfig) modelservice.PingResult {
	if inst.ID == "local" {
		return modelservice.PingResult{OK: true, InstanceID: "local", LatencyMS: 12, ModelCount: 3, Status: "reachable"}
	}
	return modelservice.PingResult{InstanceID: inst.ID, LatencyMS: 5, Status: "unreachable", Error: "connection refused"}
}

func stubAutoConnect(t *testing.T, res *modelservice.AutoConnectResult, err error) {
	t.Helper()
	previous := autoConnectFree
	autoConnectFree = func(*cobra.Command, string) (*modelservice.AutoConnectResult, error) { return res, err }
	t.Cleanup(func() { autoConnectFree = previous })
}

func TestModelCommandsInheritJSONFlag(t *testing.T) {
	cmd := NewModelCommand()
	require.NotNil(t, cmd.PersistentFlags().Lookup("json"))
	for _, name := range []string{"auto-free", "ping", "roster"} {
		sub, _, err := cmd.Find([]string{name})
		require.NoError(t, err)
		assert.NotNil(t, sub.InheritedFlags().Lookup("json"), name)
	}
}

func TestModelJSONShowsDefaultModelAndChoices(t *testing.T) {
	path, cfg := testConfig(t)
	cfg.Agents.Defaults.ModelName = "everyday"
	require.NoError(t, config.SaveConfig(path, cfg))

	stdout, _, printed, err := runModel(t, "--json")
	require.NoError(t, err)
	assert.Empty(t, printed)
	assert.JSONEq(t, `{
		"selection": "everyday",
		"active_models": ["local/llama3", "local/qwen3"],
		"routes": [{"name": "everyday", "targets": ["local/llama3", "local/qwen3"]}]
	}`, stdout)
}

func TestModelJSONListsAreNeverNull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(config.EnvConfig, path)
	require.NoError(t, config.SaveConfig(path, config.DefaultConfig()))

	stdout, _, _, err := runModel(t, "--json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"selection": "", "active_models": [], "routes": []}`, stdout)
}

func TestModelJSONSetsAndClearsDefaultModel(t *testing.T) {
	path, _ := testConfig(t)
	acceptSelections(t, "local/qwen3")

	stdout, _, printed, err := runModel(t, "local/qwen3", "--json")
	require.NoError(t, err)
	assert.Empty(t, printed)
	assert.JSONEq(t, `{"selection": "local/qwen3", "previous": ""}`, stdout)

	stdout, _, printed, err = runModel(t, "--clear", "--json")
	require.NoError(t, err)
	assert.Empty(t, printed)
	assert.JSONEq(t, `{"selection": "", "previous": "local/qwen3"}`, stdout)

	saved, err := config.LoadConfig(path)
	require.NoError(t, err)
	assert.Empty(t, saved.Agents.Defaults.ModelName)
}

// A failed command prints no document of its own: main prints the error one.
func TestModelJSONFailurePrintsNothing(t *testing.T) {
	testConfig(t)
	acceptSelections(t)

	stdout, _, printed, err := runModel(t, "local/missing", "--json")
	require.ErrorContains(t, err, `cannot use "local/missing"`)
	assert.Empty(t, stdout)
	assert.Empty(t, printed)
}

func TestPingJSONListsEachInstance(t *testing.T) {
	twoInstances(t)
	stubPing(t, cannedPing)

	stdout, _, printed, err := runModel(t, "ping", "--json")
	require.NoError(t, err)
	assert.Empty(t, printed)
	assert.JSONEq(t, `{"results": [
		{"ok": true, "instance_id": "local", "latency_ms": 12, "model_count": 3, "status": "reachable"},
		{"ok": false, "instance_id": "cloud", "latency_ms": 5, "status": "unreachable", "error": "connection refused"}
	], "total": 2}`, stdout)

	// One instance, named in any case, has the same shape.
	stdout, _, _, err = runModel(t, "ping", "CLOUD", "--json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"results": [
		{"ok": false, "instance_id": "cloud", "latency_ms": 5, "status": "unreachable", "error": "connection refused"}
	], "total": 1}`, stdout)

	stdout, _, _, err = runModel(t, "ping", "ghost", "--json")
	require.ErrorContains(t, err, `provider instance "ghost" not found`)
	assert.Empty(t, stdout)
}

func TestPingWithoutInstances(t *testing.T) {
	stubPing(t, cannedPing)
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(config.EnvConfig, path)
	require.NoError(t, config.SaveConfig(path, config.DefaultConfig()))

	stdout, _, _, err := runModel(t, "ping", "--json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"results": [], "total": 0}`, stdout)

	stdout, _, _, err = runModel(t, "ping")
	require.NoError(t, err)
	assert.Equal(t, "No provider instances configured.\n", stdout)
}

// Text shows each instance's ping before the next ping starts.
func TestPingTextShowsEachResultAsItArrives(t *testing.T) {
	twoInstances(t)
	cmd := NewModelCommand()
	cmd.SetArgs([]string{"ping"})
	var out bytes.Buffer
	cmd.SetOut(&out)

	localShownFirst := false
	stubPing(t, func(inst *config.ProviderInstanceConfig) modelservice.PingResult {
		if inst.ID == "cloud" {
			localShownFirst = strings.Contains(out.String(), "[OK] local")
		}
		return cannedPing(inst)
	})
	require.NoError(t, cmd.Execute())

	assert.True(t, localShownFirst, "local's result waited for cloud's ping")
	assert.Contains(t, out.String(), "12ms (3 models) [reachable]")
	assert.Contains(t, out.String(), "[FAIL] cloud")
	assert.Contains(t, out.String(), "connection refused")
}

func TestAutoFreeJSONPrintsTheLaunchersAnswer(t *testing.T) {
	testConfig(t)
	stubAutoConnect(t, &modelservice.AutoConnectResult{
		Total:             2,
		CatalogDiscovered: 1,
		Outcomes: []modelservice.AnonymousProviderOutcome{{
			RegistryID: "free-a", ProviderID: "free-a", Status: "catalog_only",
			Models: []string{"m1"}, ErrorClass: "rate_limited", Error: "429",
		}},
	}, nil)

	stdout, stderr, printed, err := runModel(t, "auto-free", "--json")
	require.NoError(t, err)
	assert.Empty(t, printed)
	assert.Contains(t, stderr, "Probing the free providers")
	// ok false still succeeds; lists are [] and default_model is always there.
	assert.JSONEq(t, `{
		"ok": false, "total": 2, "catalog_discovered": 1, "verified": 0,
		"instances": [],
		"outcomes": [{"registry_id": "free-a", "provider_id": "free-a", "status": "catalog_only",
			"models": ["m1"], "error_class": "rate_limited", "error": "429"}],
		"default_model": ""
	}`, stdout)
}

func TestAutoFreeText(t *testing.T) {
	testConfig(t)
	stubAutoConnect(t, &modelservice.AutoConnectResult{
		OK: true, Total: 1, CatalogDiscovered: 1, Verified: 1,
		Instances: []string{"free-a"},
		Outcomes:  []modelservice.AnonymousProviderOutcome{{ProviderID: "free-a", Status: "verified"}},
	}, nil)

	stdout, stderr, _, err := runModel(t, "auto-free")
	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.Equal(t, "Probing the free providers that need no key...\n"+
		"Catalogs discovered: 1 | Inference verified: 1\n"+
		"- free-a: verified\n"+
		"  + free-a\n", stdout)
}

func TestAutoFreeJSONFailurePrintsNothing(t *testing.T) {
	testConfig(t)
	stubAutoConnect(t, nil, errors.New("catalog unreachable"))

	stdout, _, _, err := runModel(t, "auto-free", "--json")
	require.ErrorContains(t, err, "auto-connect free failed: catalog unreachable")
	assert.Empty(t, stdout)
}

func TestRosterJSONListsTheRoster(t *testing.T) {
	path, _ := testConfig(t)

	stdout, _, printed, err := runModel(t, "roster", "--json")
	require.NoError(t, err)
	assert.Empty(t, printed)

	var got struct {
		Providers []map[string]any `json:"providers"`
		Total     int              `json:"total"`
	}
	decodeOne(t, stdout, &got)

	cfg, err := config.LoadConfig(path)
	require.NoError(t, err)
	roster := modelservice.ListRoster(cfg)
	require.NotEmpty(t, roster)
	require.Len(t, got.Providers, len(roster))
	assert.Equal(t, len(roster), got.Total)
	for i, item := range roster {
		assert.Equal(t, item.ID, got.Providers[i]["id"])
		assert.Equal(t, item.DisplayName, got.Providers[i]["display_name"])
	}
}
