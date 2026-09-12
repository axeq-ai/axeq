package interaction

import (
	aclio "github.com/agenticcliorchestra/aclio-go"
	"github.com/agenticcliorchestra/aclio-go/claude"
)

// request assembles the claude call shared by Initial and Subsequent: the
// allow-listed tools, the optional system prompt file, and per-call debug
// files dumped into the debug dir.
func request(name, dir, promptText, schema, systemPromptFile, resumeID string) aclio.Request {
	return aclio.Request{
		Provider:   aclio.Claude,
		Dir:        dir,
		Prompt:     promptText,
		Model:      "sonnet",
		JSONSchema: schema,
		ResumeID:   resumeID,
		Name:       name,
		Stream:     true,
		ClaudeOpts: &claude.RunOpts{
			SystemPromptFile: systemPromptFile,
			AllowedTools:     []string{"Read", "Grep", "Bash(mkdir:*)", "Bash(ffmpeg:*)"},
			TempDir:          debugDir,
		},
	}
}
