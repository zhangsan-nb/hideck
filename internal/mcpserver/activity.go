package mcpserver

import "time"

// Activity is process-local metadata; it never contains arguments, results or credentials.
type Activity struct {
	LastRequestAt time.Time     `json:"last_request_at"`
	LastTool      *ToolActivity `json:"last_tool,omitempty"`
}

type ToolActivity struct {
	Name       string    `json:"name"`
	At         time.Time `json:"at"`
	DurationMS int64     `json:"duration_ms"`
	Failed     bool      `json:"failed"`
}

func (s *Tokens) Activity(id string) *Activity {
	s.mu.Lock()
	defer s.mu.Unlock()
	activity, exists := s.activity[id]
	if !exists {
		return nil
	}
	if activity.LastTool != nil {
		tool := *activity.LastTool
		activity.LastTool = &tool
	}
	return &activity
}

func (s *Tokens) recordTool(id string, tool ToolActivity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	activity, exists := s.activity[id]
	if !exists {
		return
	} // Revocation/deletion must not be undone by a finishing call.
	activity.LastTool = &tool
	s.activity[id] = activity
}
