// SPDX-License-Identifier: Apache-2.0
package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type object = map[string]any

const providerID = "aiprouter"
const placeholder = "aiprouter-loopback"

func frag(path []string, value any) Fragment { return Fragment{path, value} }
func providerFragment(root string, value any) []Fragment {
	return []Fragment{frag([]string{root, providerID}, value)}
}
func selector(key, value string) (string, error) {
	if strings.ContainsAny(value, ",]\r\n") {
		return "", errors.New("model identity cannot be represented by the client's ownership selector")
	}
	return "[" + key + "=" + value + "]", nil
}
func modelRows(models []ClientModel) []any {
	rows := []any{}
	for _, m := range models {
		input := clientInput(m, false)
		row := object{"id": m.ID, "name": m.Name, "input": input}
		if m.Context > 0 {
			row["contextWindow"] = m.Context
		}
		if m.Output > 0 {
			row["maxTokens"] = m.Output
		}
		rows = append(rows, row)
	}
	return rows
}
func contributions(id, path, base, key string, models []ClientModel, doc object) ([]Fragment, error) {
	models = compatibleClientModels(id, models)
	if len(models) == 0 {
		return nil, errors.New("no routed models are available")
	}
	for _, m := range models {
		if m.ID == "" || strings.Contains(m.ID, "${") || strings.Contains(m.Name, "${") {
			return nil, errors.New("unsafe model catalog identity")
		}
	}
	pi := object{"baseUrl": base, "api": "openai-completions", "apiKey": key, "models": modelRows(models)}
	switch id {
	case "pi", "prime", "aside", "omo", "gentle-shell", "omp", "gajae":
		rows := pi["models"].([]any)
		for i, m := range models {
			row := rows[i].(object)
			if len(m.Efforts) > 0 {
				row["reasoning"] = true
				switch id {
				case "omp":
					var efforts []string
					for _, v := range m.Efforts {
						if v != "none" && v != "ultra" {
							efforts = append(efforts, v)
						}
					}
					row["thinking"] = object{"mode": "effort", "efforts": efforts}
				case "gajae":
					var efforts []string
					for _, v := range m.Efforts {
						if v != "none" && v != "ultra" {
							efforts = append(efforts, v)
						}
					}
					if len(efforts) > 0 {
						row["thinking"] = object{"mode": "effort", "minLevel": efforts[0], "maxLevel": efforts[len(efforts)-1], "levels": efforts}
						row["compat"] = object{"supportsReasoningEffort": true}
					}
				default:
					levels := object{}
					for _, v := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
						levels[v] = nil
						wire := v
						if v == "off" {
							wire = "none"
						}
						for _, offered := range m.Efforts {
							if offered == wire || (v == "max" && offered == "ultra") {
								levels[v] = offered
								break
							}
						}
					}
					row["thinkingLevelMap"] = levels
				}
			}
		}
		if id == "pi" || id == "omo" || id == "gentle-shell" {
			pi["compat"] = object{"sendSessionAffinityHeaders": true, "supportsDeveloperRole": false}
		}
		return providerFragment("providers", pi), nil
	case "qoder", "qoder-cn":
		rows := []any{}
		for _, m := range models {
			window := m.Context
			if window == 0 {
				window = 128000
			}
			rows = append(rows, object{"model": m.ID, "displayName": m.Name, "contextWindow": window})
		}
		return providerFragment("providers", object{"type": "openai-compatible", "protocol": "openai", "displayName": "AIPRouter", "baseUrl": base, "apiKey": key, "defaultModel": models[0].ID, "models": rows}), nil
	case "workbuddy", "workbuddy-ai":
		var f []Fragment
		for _, m := range models {
			s, e := selector("id", m.ID)
			if e != nil {
				return nil, e
			}
			row := object{"id": m.ID, "name": "AIPRouter: " + m.Name, "vendor": "OpenAI", "url": base + "/chat/completions", "apiKey": key, "supportsToolCall": m.Tools, "supportsImages": m.Vision, "supportsReasoning": len(m.Efforts) > 0}
			if m.Context > 0 {
				row["maxInputTokens"] = m.Context
			}
			if m.Output > 0 {
				row["maxOutputTokens"] = m.Output
			}
			target := []string{"models", s}
			if _, ok := doc["$sequence"]; ok {
				target = []string{s}
			}
			f = append(f, frag(target, row))
		}
		return f, nil
	case "opencode", "kilo":
		catalog := object{}
		v2 := object{}
		for _, m := range models {
			row := object{"name": m.Name, "tool_call": m.Tools, "attachment": m.Vision}
			if len(m.InputModalities) > 0 {
				input := clientInput(m, true)
				attachment := false
				for _, v := range input {
					attachment = attachment || v != "text"
				}
				if len(input) > 0 {
					row["attachment"] = attachment
					row["modalities"] = object{"input": input, "output": []string{"text"}}
				}
			}
			if m.Context > 0 && m.Output > 0 {
				row["limit"] = object{"context": m.Context, "output": m.Output}
			}
			variants := object{}
			var modern []any
			for _, effort := range m.Efforts {
				variants[effort] = object{"reasoningEffort": effort}
				modern = append(modern, object{"id": effort, "settings": object{"reasoningEffort": effort}})
			}
			row["variants"] = variants
			catalog[m.ID] = row
			caps := object{"tools": m.Tools}
			if len(m.InputModalities) > 0 && len(clientInput(m, true)) > 0 {
				caps["input"] = clientInput(m, true)
				caps["output"] = []string{"text"}
			}
			modernRow := object{"name": m.Name, "variants": modern, "capabilities": caps}
			if limit, ok := row["limit"]; ok {
				modernRow["limit"] = limit
			}
			v2[m.ID] = modernRow
		}
		connection := object{"baseURL": base, "apiKey": key}
		f := providerFragment("provider", object{"npm": "@ai-sdk/openai-compatible", "name": "AIPRouter", "options": connection, "models": catalog})
		if id == "opencode" {
			f = append(f, frag([]string{"providers", providerID}, object{"package": "@opencode/ai/providers/openai-compatible", "name": "AIPRouter", "settings": connection, "models": v2}))
		}
		return f, nil
	case "hermes":
		catalog := object{}
		for _, m := range models {
			catalog[m.ID] = object{"supports_vision": m.Vision}
		}
		return providerFragment("providers", object{"api": base, "api_key": key, "api_mode": "chat_completions", "session_affinity_header": "session-id", "discover_models": false, "models": catalog}), nil
	case "openclaw":
		return []Fragment{frag([]string{"models", "providers", providerID}, pi)}, nil
	case "kimi":
		f := providerFragment("providers", object{"type": "openai", "base_url": base, "api_key": key})
		for _, m := range models {
			if m.Context <= 0 {
				continue
			}
			row := object{"provider": providerID, "model": m.ID, "max_context_size": m.Context, "display_name": m.Name}
			if m.Vision {
				row["capabilities"] = []string{"image_in"}
			}
			f = append(f, frag([]string{"models", providerID + "/" + m.ID}, row))
		}
		if len(f) == 1 {
			return nil, errors.New("Kimi needs a model with a known context limit")
		}
		return f, nil
	case "dsh":
		return []Fragment{frag([]string{"llm-pi-ai", "providers", providerID}, object{"displayName": "AIPRouter", "api": "openai-responses", "baseURL": base, "headers": object{"Authorization": "Bearer " + key}, "models": modelRows(models)})}, nil
	case "mcode":
		catalog := object{}
		for _, m := range models {
			row := object{}
			if m.Context > 0 {
				row["limit"] = object{"context": m.Context}
			}
			if len(m.Efforts) > 0 {
				var efforts []string
				for _, e := range m.Efforts {
					if e != "none" {
						efforts = append(efforts, e)
					}
				}
				row["thinking"] = object{"effortOptions": efforts}
			}
			catalog[m.ID] = row
		}
		return providerFragment("custom_provider", object{"name": "AIPRouter", "kind": "custom", "enabled": true, "api": "anthropic-messages", "options": object{"apiKey": key, "baseURL": strings.TrimSuffix(base, "/v1"), "authMode": "api-key"}, "models": catalog}), nil
	case "zcode":
		if strings.HasSuffix(path, "provider_config.json") {
			if doc["schemaVersion"] != float64(1) {
				return nil, errors.New("ZCode provider store schema is not established")
			}
			ids := []string{}
			for _, m := range models {
				ids = append(ids, m.ID)
			}
			f := []Fragment{frag([]string{"config", "providerConfigRules", "providerRules", "[providerId=aiprouter]"}, object{"providerId": providerID, "enabled": true, "providerName": "AIPRouter", "config": object{"group": "standard-personal", "access": object{"type": "api-key", "apiKey": key}, "api": object{"type": "openai-responses", "baseUrl": base}, "personalModelIds": ids, "modelOrder": ids}})}
			for _, m := range models {
				s, e := selector("modelId", m.ID)
				if e != nil {
					return nil, e
				}
				s = "[providerId=aiprouter," + s[1:]
				props := object{}
				if m.Context > 0 {
					props["contextWindow"] = m.Context
				}
				if m.Vision {
					props["inputFormat"] = object{"supportsImage": true}
				}
				f = append(f, frag([]string{"config", "modelConfigRules", "providerModelRules", s}, object{"providerId": providerID, "modelId": m.ID, "config": object{"properties": props}}))
			}
			return f, nil
		}
		catalog := object{}
		for _, m := range models {
			row := object{"name": m.Name, "modalities": object{"input": clientInput(m, false), "output": []string{"text"}}}
			if m.Context > 0 {
				row["limit"] = object{"context": m.Context}
			}
			catalog[m.ID] = row
		}
		return providerFragment("provider", object{"name": "AIPRouter", "kind": "openai", "enabled": true, "source": "custom", "options": object{"apiKey": key, "baseURL": base, "apiKeyRequired": true}, "models": catalog}), nil
	case "commandcode":
		root := "provider"
		if _, ok := doc[root]; !ok {
			if _, ok = doc["providers"]; ok {
				root = "providers"
			}
		}
		catalog := object{}
		for _, m := range models {
			row := object{}
			if m.Context > 0 {
				row["contextWindow"] = m.Context
			}
			if len(m.Efforts) > 0 {
				row["reasoningEfforts"] = m.Efforts
			}
			catalog[m.ID] = row
		}
		return providerFragment(root, object{"name": "AIPRouter", "api": "openai-completions", "baseURL": base, "apiKey": false, "models": catalog}), nil
	case "droid":
		var f []Fragment
		for _, m := range models {
			s, e := selector("model", m.ID)
			if e != nil {
				return nil, e
			}
			label := "AIPRouter: " + m.Name
			if strings.ContainsAny(label, ",]") {
				return nil, errors.New("Droid display name cannot be addressed")
			}
			s = strings.TrimSuffix(s, "]") + ",displayName=" + label + "]"
			f = append(f, frag([]string{"customModels", s}, object{"model": m.ID, "displayName": label, "baseUrl": base, "provider": "generic-chat-completion-api", "noImageSupport": !m.Vision}))
		}
		return f, nil
	case "raycast":
		rows := []any{}
		for _, m := range models {
			row := object{"id": m.ID, "name": m.Name, "abilities": object{"temperature": object{"supported": len(m.Efforts) == 0}, "vision": object{"supported": m.Vision}, "system_message": object{"supported": true}, "tools": object{"supported": m.Tools}, "reasoning_effort": object{"supported": len(m.Efforts) > 0}}}
			if m.Context > 0 {
				row["context"] = m.Context
			}
			rows = append(rows, row)
		}
		return []Fragment{frag([]string{"providers", "[id=aiprouter]"}, object{"id": providerID, "name": "AIPRouter", "base_url": base, "models": rows})}, nil
	case "codex":
		return []Fragment{frag([]string{"model_providers", providerID}, object{"name": "AIPRouter", "base_url": base, "wire_api": "responses", "experimental_bearer_token": key}), frag([]string{"model_provider"}, providerID)}, nil
	case "claude":
		return []Fragment{frag([]string{"env", "ANTHROPIC_BASE_URL"}, strings.TrimSuffix(base, "/v1")), frag([]string{"env", "ANTHROPIC_AUTH_TOKEN"}, key)}, nil
	case "grok":
		f := []Fragment{frag([]string{"model_providers", providerID}, object{"base_url": base, "api_backend": "responses", "api_key": key})}
		taken := map[string]bool{}
		if existing, ok := doc["model"].(map[string]any); ok {
			for alias := range existing {
				taken[alias] = true
			}
		}
		for _, m := range models {
			stem := "aip-" + regexp.MustCompile(`[^A-Za-z0-9_-]`).ReplaceAllString(m.ID, "-")
			alias := stem
			for n := 2; taken[alias]; n++ {
				alias = fmt.Sprintf("%s-%d", stem, n)
			}
			taken[alias] = true
			row := object{"model_provider": providerID, "model": m.ID, "name": m.Name}
			if m.Context > 0 {
				row["context_window"] = m.Context
			}
			var options []any
			for _, effort := range m.Efforts {
				if effort == "none" || effort == "ultra" {
					continue
				}
				options = append(options, object{"id": effort, "value": effort, "label": effort, "description": effort, "default": len(options) == 0})
			}
			if len(options) > 0 {
				row["supports_reasoning_effort"] = true
				row["reasoning_effort"] = options[0].(map[string]any)["value"]
				row["reasoning_efforts"] = options
			}
			f = append(f, frag([]string{"model", alias}, row))
		}
		return f, nil
	case "cline":
		return nil, errors.New("Cline must use the paired-file builder")
	case "claude-desktop":
		return nil, errors.New("Claude Desktop profile activation and native secret store require dedicated ownership handling; configuration refused")
	}
	return nil, fmt.Errorf("no builder for client %s", id)
}
