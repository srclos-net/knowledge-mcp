package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ── MCP protocol types ───────────────────────────────────────────────────────

type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

type InputSchema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties"`
	Required   []string            `json:"required,omitempty"`
}

type Property struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Enum        []string `json:"enum,omitempty"`
	Default     any      `json:"default,omitempty"`
}

type ToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func textResult(text string) ToolResult {
	return ToolResult{Content: []ContentBlock{{Type: "text", Text: text}}}
}

func errorResult(text string) ToolResult {
	return ToolResult{Content: []ContentBlock{{Type: "text", Text: text}}, IsError: true}
}

var validCategories = []string{
	"preferences",      // communication style, format preferences
	"personal_context", // relevant personal facts that help responses
	"technical",        // technical preferences, stack details
	"personal_growth",  // therapeutic work, growth patterns
	"mistakes",         // things that went wrong, to avoid
	"general",          // catch-all
}

// ── Tool definitions ─────────────────────────────────────────────────────────

func GetTools() []Tool {
	return []Tool{
		{
			Name: "lookup_context",
			Description: `CALL THIS FIRST at the start of any conversation.
Retrieves relevant learnings and context that should inform how to best interact with this user.
Returns stored preferences, past mistakes to avoid, and relevant personal context.
Use the results to calibrate your tone, approach, and content before responding.`,
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"query": {
						Type:        "string",
						Description: "Keywords describing the topic or type of help needed (e.g. 'emotional support', 'kubernetes debugging', 'writing')",
					},
					"category": {
						Type:        "string",
						Description: "Optional: filter by category",
						Enum:        append([]string{""}, validCategories...),
					},
					"limit": {
						Type:        "integer",
						Description: "Max results to return (default 10)",
						Default:     10,
					},
				},
				Required: []string{"query"},
			},
		},
		{
			Name: "store_learning",
			Description: `Store a new learning, observation, or improvement note.
Use this to record: user preferences discovered during conversation, mistakes made and how to avoid them,
useful context about the user, communication patterns that work well or poorly.
Be specific and actionable. Write as if briefing a future version of yourself.`,
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"category": {
						Type:        "string",
						Description: "Category for this learning",
						Enum:        validCategories,
					},
					"content": {
						Type:        "string",
						Description: "The learning itself. Be specific and actionable.",
					},
					"tags": {
						Type:        "string",
						Description: "Comma-separated tags (e.g. 'formatting,tone,communication')",
					},
					"confidence": {
						Type:        "number",
						Description: "Confidence in this learning, 0.0-1.0 (default 0.8)",
						Default:     0.8,
					},
				},
				Required: []string{"category", "content"},
			},
		},
		{
			Name:        "list_learnings",
			Description: "List stored learnings, optionally filtered by category.",
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"category": {
						Type:        "string",
						Description: "Optional: filter by category",
						Enum:        append([]string{""}, validCategories...),
					},
					"limit": {
						Type:        "integer",
						Description: "Max results (default 50)",
						Default:     50,
					},
				},
			},
		},
		{
			Name:        "update_learning",
			Description: "Update an existing learning by ID. Use to refine or correct a stored learning.",
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"id": {
						Type:        "string",
						Description: "ID of the learning to update",
					},
					"content": {
						Type:        "string",
						Description: "Updated content",
					},
					"tags": {
						Type:        "string",
						Description: "Updated tags (comma-separated)",
					},
					"confidence": {
						Type:        "number",
						Description: "Updated confidence score 0.0-1.0",
					},
				},
				Required: []string{"id", "content"},
			},
		},
		{
			Name:        "delete_learning",
			Description: "Delete a learning by ID. Use when a learning is outdated, wrong, or no longer relevant.",
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"id": {
						Type:        "string",
						Description: "ID of the learning to delete",
					},
				},
				Required: []string{"id"},
			},
		},
		{
			Name:        "get_stats",
			Description: "Get a summary of stored learnings by category.",
			InputSchema: InputSchema{
				Type:       "object",
				Properties: map[string]Property{},
			},
		},
	}
}

// ── Dispatch ─────────────────────────────────────────────────────────────────

func HandleTool(backend Backend, name string, args json.RawMessage) ToolResult {
	switch name {
	case "lookup_context":
		return handleLookup(backend, args)
	case "store_learning":
		return handleStore(backend, args)
	case "list_learnings":
		return handleList(backend, args)
	case "update_learning":
		return handleUpdate(backend, args)
	case "delete_learning":
		return handleDelete(backend, args)
	case "get_stats":
		return handleStats(backend)
	default:
		return errorResult(fmt.Sprintf("unknown tool: %s", name))
	}
}

// ── Handlers ─────────────────────────────────────────────────────────────────

func handleLookup(backend Backend, args json.RawMessage) ToolResult {
	var p struct {
		Query    string `json:"query"`
		Category string `json:"category"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}
	if p.Limit <= 0 {
		p.Limit = 10
	}

	learnings, err := backend.Search(p.Query, p.Category, p.Limit)
	if err != nil {
		return errorResult("search failed: " + err.Error())
	}
	if len(learnings) == 0 {
		return textResult("No relevant learnings found. This may be a new topic or a fresh start.")
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d relevant learnings:\n\n", len(learnings)))
	for _, l := range learnings {
		sb.WriteString(fmt.Sprintf("--- [ID:%s | %s | confidence:%.1f]\n", l.ID, l.Category, l.Confidence))
		sb.WriteString(l.Content + "\n")
		if l.Tags != "" {
			sb.WriteString(fmt.Sprintf("tags: %s\n", l.Tags))
		}
		sb.WriteString("\n")
		backend.IncrementUseCount(l.ID)
	}
	return textResult(sb.String())
}

func handleStore(backend Backend, args json.RawMessage) ToolResult {
	var p struct {
		Category   string  `json:"category"`
		Content    string  `json:"content"`
		Tags       string  `json:"tags"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}
	if p.Confidence == 0 {
		p.Confidence = 0.8
	}
	if p.Category == "" {
		p.Category = "general"
	}

	l, err := backend.Add(p.Category, p.Content, p.Tags, p.Confidence)
	if err != nil {
		return errorResult("failed to store: " + err.Error())
	}
	return textResult(fmt.Sprintf("Learning stored successfully with ID:%s in category '%s'.", l.ID, l.Category))
}

func handleList(backend Backend, args json.RawMessage) ToolResult {
	var p struct {
		Category string `json:"category"`
		Limit    int    `json:"limit"`
	}
	json.Unmarshal(args, &p)
	if p.Limit <= 0 {
		p.Limit = 50
	}

	learnings, err := backend.List(p.Category, p.Limit)
	if err != nil {
		return errorResult("list failed: " + err.Error())
	}
	if len(learnings) == 0 {
		return textResult("No learnings stored yet.")
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Stored learnings (%d):\n\n", len(learnings)))
	for _, l := range learnings {
		sb.WriteString(fmt.Sprintf("[ID:%s | %s | confidence:%.1f | used:%d times]\n", l.ID, l.Category, l.Confidence, l.UseCount))
		sb.WriteString(l.Content + "\n")
		if l.Tags != "" {
			sb.WriteString(fmt.Sprintf("tags: %s\n", l.Tags))
		}
		sb.WriteString(fmt.Sprintf("updated: %s\n\n", l.UpdatedAt.Format("2006-01-02")))
	}
	return textResult(sb.String())
}

func handleUpdate(backend Backend, args json.RawMessage) ToolResult {
	var p struct {
		ID         string  `json:"id"`
		Content    string  `json:"content"`
		Tags       string  `json:"tags"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}
	if p.Confidence == 0 {
		p.Confidence = 0.8
	}
	if err := backend.Update(p.ID, p.Content, p.Tags, p.Confidence); err != nil {
		return errorResult("update failed: " + err.Error())
	}
	return textResult(fmt.Sprintf("Learning ID:%s updated successfully.", p.ID))
}

func handleDelete(backend Backend, args json.RawMessage) ToolResult {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}
	if err := backend.Delete(p.ID); err != nil {
		return errorResult("delete failed: " + err.Error())
	}
	return textResult(fmt.Sprintf("Learning ID:%s deleted.", p.ID))
}

func handleStats(backend Backend) ToolResult {
	stats, err := backend.Stats()
	if err != nil {
		return errorResult("stats failed: " + err.Error())
	}

	var sb strings.Builder
	total := 0
	sb.WriteString("Learnings by category:\n")
	for cat, count := range stats {
		sb.WriteString(fmt.Sprintf("  %-20s %d\n", cat, count))
		total += count
	}
	sb.WriteString(fmt.Sprintf("\nTotal: %d learnings\n", total))
	return textResult(sb.String())
}
