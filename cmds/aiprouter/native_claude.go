// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

// PlanClaudeNative plans gateway configuration without writing files or starting
// a client. The caller must obtain both settings and remote-key consent through
// Disk.Apply. First-party TLS interception requires a separate trust lifecycle;
// gateway configuration must never install a proxy or enroll a certificate.
func PlanClaudeNative(resolver Resolver, state State, client string, profile Profile, key string, models []ClientModel) ([]Change, error) {
	if client != "claude" && client != "claude-desktop" {
		return nil, errors.New("unsupported native Claude client")
	}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("Claude gateway requires a profile key")
	}
	path, err := resolver.Resolve(client)
	if err != nil {
		return nil, err
	}
	paths := []string{path}
	if client == "claude" {
		paths = append(paths, filepath.Join(filepath.Dir(path), "cache", "gateway-models.json"))
	} else {
		paths = append(paths, filepath.Join(filepath.Dir(path), "_meta.json"))
	}
	if record, ok := state.Records[client]; ok {
		for _, backup := range record.Files {
			found := false
			for _, target := range paths {
				if backup.Path == target {
					found = true
				}
			}
			if !found {
				return nil, errors.New("Claude configuration path changed; restore the recorded files first")
			}
		}
	}
	check := func() error {
		current, e := resolver.Resolve(client)
		if e != nil {
			return e
		}
		if current != path {
			return ErrConflict
		}
		return nil
	}
	var changes []Change
	base := strings.TrimRight(profile.Server, "/")
	for index, target := range paths {
		before, exists, e := readConfig(target)
		if e != nil {
			return nil, e
		}
		source := before
		owned := false
		for owner, record := range state.Records {
			for _, backup := range record.Files {
				if backup.Path != target {
					continue
				}
				if owner != client {
					return nil, errors.New("another client owns this Claude destination")
				}
				source, _, e = restoreOwned(client, backup, before)
				if e != nil {
					return nil, e
				}
				owned = true
			}
		}
		doc := object{}
		if len(bytes.TrimSpace(source)) > 0 {
			doc, e = parseDocument(source, JSON)
			if e != nil {
				return nil, e
			}
		}
		var fragments []Fragment
		replace := false
		remote := index == 0
		if client == "claude" && index == 0 {
			// A pre-existing vendor key must not follow the new gateway origin.
			// Leave it untouched and require the user to resolve its provenance.
			if env := mapAt(doc, "env"); env != nil {
				if value, present := env["ANTHROPIC_API_KEY"]; present && value != "" && value != nil {
					return nil, errors.New("Claude has an existing API key; resolve its gateway authorization before configuring")
				}
			}
			fragments = []Fragment{frag([]string{"env", "ANTHROPIC_BASE_URL"}, base), frag([]string{"env", "ANTHROPIC_AUTH_TOKEN"}, key)}
			if _, present := mapAt(doc, "env")["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"]; !present {
				fragments = append(fragments, frag([]string{"env", "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"}, "1"))
			}
		} else if client == "claude" {
			// This is a disposable native cache, but its prior bytes still belong to
			// the user. Capture and restore them with the same transaction as settings.
			// The schema and usable-ID rule follow the pinned upstream gateway-cache.ts.
			rows := []any{}
			seen := map[string]bool{}
			for _, model := range models {
				id := strings.ToLower(model.ID)
				if !strings.Contains(id, "claude") && !strings.Contains(id, "anthropic") {
					continue
				}
				if seen[model.ID] {
					return nil, errors.New("duplicate Claude gateway model ID")
				}
				seen[model.ID] = true
				row := object{"id": model.ID}
				if model.Name != "" {
					row["display_name"] = model.Name
				}
				if model.Description != "" {
					row["description"] = model.Description
				}
				rows = append(rows, row)
			}
			fragments = []Fragment{frag([]string{"baseUrl"}, base), frag([]string{"fetchedAt"}, time.Now().UnixMilli()), frag([]string{"models"}, rows)}
			replace = true
		} else if index == 0 {
			if !owned && exists {
				return nil, errors.New("unowned AIPRouter Desktop profile already exists")
			}
			if len(bytes.TrimSpace(source)) > 0 {
				return nil, errors.New("AIPRouter Desktop profile has a foreign restoration baseline")
			}
			fragments = []Fragment{frag([]string{"inferenceProvider"}, "gateway"), frag([]string{"inferenceCredentialKind"}, "static"), frag([]string{"inferenceGatewayBaseUrl"}, base), frag([]string{"inferenceGatewayApiKey"}, key), frag([]string{"modelDiscoveryEnabled"}, true)}
			catalog, static, err := claudeDesktopCatalog(models)
			if err != nil {
				return nil, err
			}
			if static {
				fragments[len(fragments)-1] = frag([]string{"modelDiscoveryEnabled"}, false)
				fragments = append(fragments, frag([]string{"inferenceModels"}, catalog))
			}
		} else {
			if len(bytes.TrimSpace(source)) == 0 {
				source = []byte("{\"entries\":[]}\n")
				doc = object{"entries": []any{}}
			}
			entries, ok := doc["entries"].([]any)
			if !ok {
				return nil, errors.New("Claude Desktop metadata has no entries array")
			}
			seen := map[string]bool{}
			for _, entry := range entries {
				row, ok := entry.(map[string]any)
				if !ok {
					return nil, errors.New("invalid Claude Desktop metadata entry")
				}
				id, ok := row["id"].(string)
				name, named := row["name"].(string)
				if !ok || id == "" || !named || name == "" || seen[id] {
					return nil, errors.New("invalid or duplicate Claude Desktop profile identity")
				}
				seen[id] = true
				if id == providerID {
					return nil, errors.New("unowned AIPRouter Desktop metadata entry already exists")
				}
			}
			fragments = []Fragment{frag([]string{"entries", "[id=aiprouter]"}, object{"id": providerID, "name": "AIPRouter"})}
			source, e = Patch(source, JSON, []Fragment{frag([]string{"appliedId"}, providerID)}, true)
			if e != nil {
				return nil, e
			}
		}
		after, e := Patch(source, JSON, fragments, replace)
		if e != nil {
			return nil, e
		}
		if len(after) > MaxConfigBytes {
			return nil, errors.New("Claude configuration exceeds the file size limit")
		}
		fields := []string{}
		for _, fragment := range fragments {
			fields = append(fields, strings.Join(fragment.Path, "."))
		}
		if client == "claude-desktop" && index == 1 {
			fields = append(fields, "appliedId")
		}
		changes = append(changes, Change{Path: target, Before: before, After: after, Existed: exists, Fields: fields, RemoteKey: remote, Check: check})
	}
	return changes, nil
}

var claudeDesktopID = regexp.MustCompile(`^claude-[a-z0-9-]+$`)

// The remote snapshot owns aliases and capabilities. Never derive an alias from
// a display label or infer family/1M support from an unrelated provider model.
func claudeDesktopCatalog(models []ClientModel) ([]any, bool, error) {
	// A non-nil empty snapshot is authoritative. A nil slice selects discovery
	// for callers that did not request a static snapshot.
	static := models != nil && len(models) == 0
	for _, model := range models {
		static = static || model.Desktop != nil
	}
	if !static {
		return nil, false, nil
	}
	rows := make([]any, 0, len(models))
	seen := map[string]bool{}
	defaults := map[string]bool{}
	for _, model := range models {
		row := model.Desktop
		if row == nil || !claudeDesktopID.MatchString(row.Name) || seen[row.Name] {
			return nil, false, errors.New("invalid, duplicate or incomplete Claude Desktop snapshot")
		}
		seen[row.Name] = true
		switch row.AnthropicFamilyTier {
		case "opus", "sonnet", "haiku", "fable":
		default:
			return nil, false, errors.New("invalid Claude Desktop model family")
		}
		if strings.ContainsAny(row.LabelOverride, "[]") || len(utf16.Encode([]rune(row.LabelOverride))) > 80 {
			return nil, false, errors.New("invalid Claude Desktop model label")
		}
		if row.Prefer1M != nil && *row.Prefer1M && (row.Supports1M == nil || !*row.Supports1M) {
			return nil, false, errors.New("Claude Desktop 1M preference lacks capability")
		}
		if row.IsFamilyDefault != nil && *row.IsFamilyDefault {
			if defaults[row.AnthropicFamilyTier] {
				return nil, false, errors.New("duplicate Claude Desktop family default")
			}
			defaults[row.AnthropicFamilyTier] = true
		}
		value := object{"name": row.Name, "labelOverride": row.LabelOverride, "anthropicFamilyTier": row.AnthropicFamilyTier}
		if row.IsFamilyDefault != nil {
			value["isFamilyDefault"] = *row.IsFamilyDefault
		}
		if row.Supports1M != nil {
			value["supports1m"] = *row.Supports1M
		}
		if row.Prefer1M != nil {
			value["prefer1m"] = *row.Prefer1M
		}
		rows = append(rows, value)
	}
	return rows, true, nil
}
