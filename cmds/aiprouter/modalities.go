// SPDX-License-Identifier: Apache-2.0
package main

func clientInput(m ClientModel, wide bool) []string {
	declared := m.InputModalities
	if len(declared) == 0 {
		if m.Vision {
			return []string{"text", "image"}
		}
		return []string{"text"}
	}
	accepted := map[string]bool{"text": true, "image": true}
	if wide {
		accepted["audio"] = true
		accepted["video"] = true
		accepted["pdf"] = true
	}
	var result []string
	seen := map[string]bool{}
	for _, v := range declared {
		if accepted[v] && !seen[v] {
			result = append(result, v)
			seen[v] = true
		}
	}
	return result
}
func compatibleClientModels(id string, models []ClientModel) []ClientModel {
	switch id {
	case "pi", "prime", "aside", "omo", "gentle-shell", "omp", "gajae", "dsh", "cline", "zcode", "openclaw":
		result := make([]ClientModel, 0, len(models))
		for _, m := range models {
			if len(clientInput(m, false)) > 0 {
				result = append(result, m)
			}
		}
		return result
	default:
		return models
	}
}
