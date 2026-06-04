package teams

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	// Matches Markdown tables: lines containing "|"
	tableLineRegex = regexp.MustCompile(`(?m)^\|.*\|[ \t]*$`)
	// Matches code blocks
	codeBlockRegex = regexp.MustCompile("(?s)```(.*?)```")
)

// FormatForTeams translates standard Markdown to Teams-compatible Markdown.
// Teams Bot Connector API supports a subset of Markdown.
func FormatForTeams(text string) string {
	if text == "" {
		return ""
	}

	// Clean up code blocks. Teams sometimes handles pre/code tags better than raw backticks.
	processed := codeBlockRegex.ReplaceAllStringFunc(text, func(m string) string {
		content := codeBlockRegex.FindStringSubmatch(m)[1]
		// Strip language indicator if any (e.g. "go" from "```go")
		if idx := strings.Index(content, "\n"); idx >= 0 {
			lang := strings.TrimSpace(content[:idx])
			if len(lang) < 10 && !strings.Contains(lang, " ") { // likely a language tag
				content = content[idx+1:]
			}
		}
		// Wrap in html pre/code tags to enforce monospace display in all Teams clients
		return "<pre><code>" + htmlEscape(strings.TrimSpace(content)) + "</code></pre>"
	})

	// Basic table replacement: convert markdown tables to pre-formatted monospace text block
	if strings.Contains(processed, "|") {
		processed = formatTablesAsPre(processed)
	}

	return processed
}

// formatTablesAsPre searches for Markdown tables and wraps them in <pre> tags
func formatTablesAsPre(text string) string {
	lines := strings.Split(text, "\n")
	var result []string
	inTable := false
	var tableLines []string

	for _, line := range lines {
		isTableLine := tableLineRegex.MatchString(line)
		if isTableLine {
			if !inTable {
				inTable = true
			}
			tableLines = append(tableLines, line)
		} else {
			if inTable {
				// Flush accumulated table wrapped in <pre>
				result = append(result, "<pre>"+strings.Join(tableLines, "\n")+"</pre>")
				tableLines = nil
				inTable = false
			}
			result = append(result, line)
		}
	}

	if inTable && len(tableLines) > 0 {
		result = append(result, "<pre>"+strings.Join(tableLines, "\n")+"</pre>")
	}

	return strings.Join(result, "\n")
}

func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		`'`, "&#39;",
	)
	return r.Replace(s)
}

// PollOption represents a single option inside a Poll Adaptive Card.
type PollOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// BuildSimpleCard constructs an Adaptive Card JSON for a simple rich text display.
func BuildSimpleCard(title, body string) []byte {
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.5",
		"body": []any{
			map[string]any{
				"type":   "TextBlock",
				"text":   title,
				"weight": "Bolder",
				"size":   "Medium",
				"wrap":   true,
			},
			map[string]any{
				"type": "TextBlock",
				"text": body,
				"wrap": true,
			},
		},
	}
	raw, _ := json.Marshal(card)
	return raw
}

// BuildPollCard constructs a radio-button ChoiceSet Adaptive Card for custom interactive polls.
func BuildPollCard(question string, options []PollOption) []byte {
	choices := make([]map[string]any, len(options))
	for i, opt := range options {
		choices[i] = map[string]any{
			"title": opt.Label,
			"value": opt.Value,
		}
	}

	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.5",
		"body": []any{
			map[string]any{
				"type":   "TextBlock",
				"text":   question,
				"weight": "Bolder",
				"size":   "Medium",
				"wrap":   true,
			},
			map[string]any{
				"type":    "Input.ChoiceSet",
				"id":      "pollChoice",
				"style":   "expanded", // expanded = radio button
				"choices": choices,
			},
		},
		"actions": []any{
			map[string]any{
				"type":  "Action.Submit",
				"title": "Submit",
				"data": map[string]any{
					"actionType": "pollVote",
				},
			},
		},
	}
	raw, _ := json.Marshal(card)
	return raw
}
