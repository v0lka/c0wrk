package tools

import "context"

// AskUserOption represents a single answer option for the ask_user tool.
type AskUserOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// AskUserQuestion represents a single question in a multi-question request.
type AskUserQuestion struct {
	ID          string          `json:"id"`
	Question    string          `json:"question"`
	Options     []AskUserOption `json:"options"`
	MultiSelect bool            `json:"multi_select,omitempty"`
	Recommended []string        `json:"recommended,omitempty"`
}

// AskUserRequest describes one or more questions to ask the user via the UI.
type AskUserRequest struct {
	Questions []AskUserQuestion `json:"questions"`
}

// AskUserAnswer is the user's response to a single question.
type AskUserAnswer struct {
	ID         string   `json:"id"`
	Selected   []string `json:"selected"`
	CustomText string   `json:"custom_text,omitempty"`
}

// AskUserResponse represents the user's answers to all questions.
type AskUserResponse struct {
	Answers []AskUserAnswer `json:"answers"`
}

// AskUserFunc is called when the ask_user tool needs to display questions to the user.
// If nil, ask_user is not available (CLI mode).
type AskUserFunc func(ctx context.Context, req AskUserRequest) (AskUserResponse, error)
