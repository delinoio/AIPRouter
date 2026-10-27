// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeClaudeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeNativeGatewayTransaction(t *testing.T) {
	resolver := Resolver{Home: realTemp(t), OS: "linux", Env: map[string]string{}}
	path, err := resolver.Resolve("claude")
	if err != nil {
		t.Fatal(err)
	}
	original := "{\"env\":{\"USER_SETTING\":\"preserve\"},\"permissions\":{\"allow\":[]}}\n"
	cache := filepath.Join(filepath.Dir(path), "cache", "gateway-models.json")
	cacheOriginal := "{\"baseUrl\":\"https://old.example\",\"fetchedAt\":1,\"models\":[],\"foreign\":true}\n"
	writeClaudeFixture(t, path, original)
	writeClaudeFixture(t, cache, cacheOriginal)
	state := emptyState()
	models := []ClientModel{{ID: "claude-routed", Name: "Routed", Description: "Gateway route"}, {ID: "unusable-model", Name: "Hidden"}}
	changes, err := PlanClaudeNative(resolver, state, "claude", testProfile(), "PRIVATE_TEST_KEY", models)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || !changes[0].RemoteKey || changes[1].RemoteKey {
		t.Fatal("incorrect consent scope")
	}
	var preview bytes.Buffer
	Preview(&preview, changes)
	if strings.Contains(preview.String(), "PRIVATE_TEST_KEY") || strings.Contains(preview.String(), "preserve") {
		t.Fatal("preview leaked content")
	}
	current, _ := os.ReadFile(path)
	if string(current) != original {
		t.Fatal("planner wrote settings")
	}
	doc, err := parseDocument(changes[1].After, JSON)
	if err != nil {
		t.Fatal(err)
	}
	rows := doc["models"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["id"] != "claude-routed" || doc["foreign"] != true || rows[0].(map[string]any)["description"] != "Gateway route" {
		t.Fatal("incorrect native cache")
	}
	disk := testDisk(t)
	if err := disk.Apply(&state, "claude", "test", changes, answer(false)); err == nil {
		t.Fatal("missing consent accepted")
	}
	if err := disk.Apply(&state, "claude", "test", changes, answer(true)); err != nil {
		t.Fatal(err)
	}
	changes, err = PlanClaudeNative(resolver, state, "claude", testProfile(), "ROTATED_TEST_KEY", models)
	if err != nil {
		t.Fatal(err)
	}
	if err := disk.Apply(&state, "claude", "test", changes, answer(true)); err != nil {
		t.Fatal(err)
	}
	if err := disk.Restore(&state, "claude"); err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]string{path: original, cache: cacheOriginal} {
		got, err := os.ReadFile(target)
		if err != nil || string(got) != want {
			t.Fatalf("restoration failed for %s: %v", target, err)
		}
	}
}

func TestClaudeNativeDesktopRestoresSelection(t *testing.T) {
	for _, platform := range []string{"darwin", "windows"} {
		t.Run(platform, func(t *testing.T) {
			resolver := Resolver{Home: realTemp(t), OS: platform, Env: map[string]string{}}
			path, err := resolver.Resolve("claude-desktop")
			if err != nil {
				t.Fatal(err)
			}
			metadata := filepath.Join(filepath.Dir(path), "_meta.json")
			original := "{\"entries\":[{\"id\":\"standard\",\"name\":\"Standard\",\"custom\":true}],\"appliedId\":\"standard\",\"foreign\":7}\n"
			writeClaudeFixture(t, metadata, original)
			state := emptyState()
			changes, err := PlanClaudeNative(resolver, state, "claude-desktop", testProfile(), "TEST_KEY", nil)
			if err != nil {
				t.Fatal(err)
			}
			disk := testDisk(t)
			if err := disk.Apply(&state, "claude-desktop", "test", changes, answer(true)); err != nil {
				t.Fatal(err)
			}
			changes, err = PlanClaudeNative(resolver, state, "claude-desktop", testProfile(), "NEW_KEY", nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := disk.Apply(&state, "claude-desktop", "test", changes, answer(true)); err != nil {
				t.Fatal(err)
			}
			if err := disk.Restore(&state, "claude-desktop"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("owned profile not removed")
			}
			got, _ := os.ReadFile(metadata)
			if string(got) != original {
				t.Fatal("selection or foreign metadata lost")
			}
		})
	}
}

func TestClaudeNativeRefusesForeignAndDrift(t *testing.T) {
	resolver := Resolver{Home: realTemp(t), OS: "darwin", Env: map[string]string{}}
	path, _ := resolver.Resolve("claude-desktop")
	writeClaudeFixture(t, path, "{}")
	if _, err := PlanClaudeNative(resolver, emptyState(), "claude-desktop", testProfile(), "KEY", nil); err == nil {
		t.Fatal("foreign profile adopted")
	}
	resolver = Resolver{Home: realTemp(t), OS: "linux", Env: map[string]string{}}
	path, _ = resolver.Resolve("claude")
	writeClaudeFixture(t, path, "{\"env\":{\"ANTHROPIC_AUTH_TOKEN\":\"FOREIGN\"}}")
	if _, err := PlanClaudeNative(resolver, emptyState(), "claude", testProfile(), "KEY", nil); err == nil {
		t.Fatal("foreign auth overwritten")
	}
	writeClaudeFixture(t, path, "{}")
	changes, err := PlanClaudeNative(resolver, emptyState(), "claude", testProfile(), "KEY", nil)
	if err != nil {
		t.Fatal(err)
	}
	writeClaudeFixture(t, path, "{\"late\":true}")
	state := emptyState()
	if err := testDisk(t).Apply(&state, "claude", "test", changes, answer(true)); err == nil {
		t.Fatal("late edit overwritten")
	}
	resolver.Env["CLAUDE_CONFIG_DIR"] = realTemp(t)
	if err := changes[0].Check(); err == nil {
		t.Fatal("changed selector accepted")
	}
}

func TestClaudeNativeUnsafeInputs(t *testing.T) {
	for _, content := range []string{"{broken", "[]", "{\"env\":false}"} {
		t.Run(content, func(t *testing.T) {
			resolver := Resolver{Home: realTemp(t), OS: "linux", Env: map[string]string{}}
			path, _ := resolver.Resolve("claude")
			writeClaudeFixture(t, path, content)
			if _, err := PlanClaudeNative(resolver, emptyState(), "claude", testProfile(), "KEY", nil); err == nil {
				t.Fatal("malformed settings accepted")
			}
		})
	}
	resolver := Resolver{Home: realTemp(t), OS: "linux", Env: map[string]string{}}
	path, _ := resolver.Resolve("claude")
	cache := filepath.Join(filepath.Dir(path), "cache", "gateway-models.json")
	state := emptyState()
	state.Records["other"] = Record{Files: []Backup{{Path: cache}}}
	if _, err := PlanClaudeNative(resolver, state, "claude", testProfile(), "KEY", nil); err == nil {
		t.Fatal("another client ownership ignored")
	}
	target := filepath.Join(realTemp(t), "untouched.json")
	writeClaudeFixture(t, target, "{}")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, cache); err != nil {
		t.Skip("symlink creation unavailable")
	}
	if _, err := PlanClaudeNative(resolver, emptyState(), "claude", testProfile(), "KEY", nil); err == nil {
		t.Fatal("cache symlink accepted")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "{}" {
		t.Fatal("symlink destination changed")
	}
}

func TestClaudeNativeDesktopStaticSnapshot(t *testing.T) {
	yes, no := true, false
	resolver := Resolver{Home: realTemp(t), OS: "darwin", Env: map[string]string{}}
	models := []ClientModel{{ID: "different-route", Desktop: &ClaudeDesktopModel{Name: "claude-opus-4-8-abc", LabelOverride: "Routed model", AnthropicFamilyTier: "opus", IsFamilyDefault: &yes, Supports1M: &yes, Prefer1M: &no}}}
	changes, err := PlanClaudeNative(resolver, emptyState(), "claude-desktop", testProfile(), "KEY", models)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseDocument(changes[0].After, JSON)
	if err != nil {
		t.Fatal(err)
	}
	if doc["modelDiscoveryEnabled"] != false {
		t.Fatal("static catalog not selected")
	}
	rows := doc["inferenceModels"].([]any)
	row := rows[0].(map[string]any)
	if row["name"] != "claude-opus-4-8-abc" || row["labelOverride"] != "Routed model" || row["supports1m"] != true || row["prefer1m"] != false || row["isFamilyDefault"] != true {
		t.Fatal("remote snapshot altered")
	}
	disk := testDisk(t)
	state := emptyState()
	if err := disk.Apply(&state, "claude-desktop", "test", changes, answer(true)); err != nil {
		t.Fatal(err)
	}
	models[0].Desktop.Name = "claude-opus-4-8-def"
	changes, err = PlanClaudeNative(resolver, state, "claude-desktop", testProfile(), "KEY", models)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(changes[0].After, []byte("claude-opus-4-8-abc")) {
		t.Fatal("stale catalog retained")
	}
	if err := disk.Apply(&state, "claude-desktop", "test", changes, answer(true)); err != nil {
		t.Fatal(err)
	}
	if err := disk.Restore(&state, "claude-desktop"); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeNativeDesktopRejectsInvalidSnapshot(t *testing.T) {
	yes := true
	valid := ClaudeDesktopModel{Name: "claude-opus-4-8-abc", LabelOverride: "Routed", AnthropicFamilyTier: "opus"}
	for name, change := range map[string]func(*ClaudeDesktopModel){
		"alias":          func(m *ClaudeDesktopModel) { m.Name = "invent-an-alias" },
		"family":         func(m *ClaudeDesktopModel) { m.AnthropicFamilyTier = "unknown" },
		"label":          func(m *ClaudeDesktopModel) { m.LabelOverride = "Bad[1m]" },
		"long-label":     func(m *ClaudeDesktopModel) { m.LabelOverride = strings.Repeat("a", 81) },
		"unsupported-1m": func(m *ClaudeDesktopModel) { m.Prefer1M = &yes },
	} {
		t.Run(name, func(t *testing.T) {
			row := valid
			change(&row)
			if _, _, err := claudeDesktopCatalog([]ClientModel{{Desktop: &row}}); err == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
	if _, _, err := claudeDesktopCatalog([]ClientModel{{Desktop: &valid}, {Desktop: &valid}}); err == nil {
		t.Fatal("duplicate alias accepted")
	}
	if _, _, err := claudeDesktopCatalog([]ClientModel{{Desktop: &valid}, {ID: "no-metadata"}}); err == nil {
		t.Fatal("partial snapshot accepted")
	}
	resolver := Resolver{Home: realTemp(t), OS: "linux", Env: map[string]string{}}
	path, _ := resolver.Resolve("claude")
	writeClaudeFixture(t, path, "{\"env\":{\"ANTHROPIC_API_KEY\":\"VENDOR_SECRET\"}}")
	if _, err := PlanClaudeNative(resolver, emptyState(), "claude", testProfile(), "KEY", nil); err == nil || strings.Contains(err.Error(), "VENDOR_SECRET") {
		t.Fatal("vendor key not safely refused")
	}
}

func TestClaudeNativeEmptySnapshotAndUserDiscoverySetting(t *testing.T) {
	rows, static, err := claudeDesktopCatalog([]ClientModel{})
	if err != nil || !static || rows == nil || len(rows) != 0 {
		t.Fatal("empty authoritative snapshot lost")
	}
	if _, static, err := claudeDesktopCatalog(nil); err != nil || static {
		t.Fatal("discovery fallback changed")
	}
	resolver := Resolver{Home: realTemp(t), OS: "linux", Env: map[string]string{}}
	path, _ := resolver.Resolve("claude")
	writeClaudeFixture(t, path, "{\"env\":{\"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY\":\"0\",\"ANTHROPIC_DEFAULT_OPUS_MODEL\":\"my-model\",\"ANTHROPIC_DEFAULT_OPUS_MODEL_NAME\":\"My label\"}}")
	changes, err := PlanClaudeNative(resolver, emptyState(), "claude", testProfile(), "KEY", nil)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseDocument(changes[0].After, JSON)
	if err != nil {
		t.Fatal(err)
	}
	env := mapAt(doc, "env")
	if env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"] != "0" || env["ANTHROPIC_DEFAULT_OPUS_MODEL"] != "my-model" || env["ANTHROPIC_DEFAULT_OPUS_MODEL_NAME"] != "My label" {
		t.Fatal("user model preferences overwritten")
	}
}

type claudeConsentAnswers struct {
	answers []bool
	calls   int
}

func (a *claudeConsentAnswers) Confirm(string) bool {
	a.calls++
	return a.calls <= len(a.answers) && a.answers[a.calls-1]
}

func TestClaudeNativeRequiresBothConsents(t *testing.T) {
	for _, answers := range [][]bool{{false}, {true, false}} {
		resolver := Resolver{Home: realTemp(t), OS: "linux", Env: map[string]string{}}
		state := emptyState()
		changes, err := PlanClaudeNative(resolver, state, "claude", testProfile(), "PRIVATE_KEY", nil)
		if err != nil {
			t.Fatal(err)
		}
		prompt := &claudeConsentAnswers{answers: answers}
		if err := testDisk(t).Apply(&state, "claude", "test", changes, prompt); err == nil {
			t.Fatal("declined consent applied")
		}
		if prompt.calls != len(answers) {
			t.Fatal("consent prompts skipped")
		}
		for _, change := range changes {
			if _, err := os.Stat(change.Path); !os.IsNotExist(err) {
				t.Fatal("decline wrote file")
			}
		}
	}
}

func TestClaudeNativeOwnedEditRefusesRefreshAndRestore(t *testing.T) {
	resolver := Resolver{Home: realTemp(t), OS: "linux", Env: map[string]string{}}
	state := emptyState()
	changes, err := PlanClaudeNative(resolver, state, "claude", testProfile(), "ORIGINAL_KEY", nil)
	if err != nil {
		t.Fatal(err)
	}
	disk := testDisk(t)
	if err := disk.Apply(&state, "claude", "test", changes, answer(true)); err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(changes[0].After, []byte("ORIGINAL_KEY"), []byte("USER_KEY"), 1)
	writeClaudeFixture(t, changes[0].Path, string(edited))
	if _, err := PlanClaudeNative(resolver, state, "claude", testProfile(), "REFRESH_KEY", nil); err == nil {
		t.Fatal("owned drift overwritten on refresh")
	}
	if err := disk.Restore(&state, "claude"); err == nil {
		t.Fatal("owned drift overwritten on restore")
	}
	current, _ := os.ReadFile(changes[0].Path)
	if !bytes.Equal(current, edited) || len(state.Records["claude"].Files) != 2 {
		t.Fatal("conflict lost settings or backup")
	}
}
