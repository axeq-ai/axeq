package audit

import (
	aclio "github.com/agenticcliorchestra/aclio-go"
	"github.com/agenticcliorchestra/aclio-go/claude"
)

var debugDir string

// SetDebugDir enables aclio-go's per-call debug dumps into dir (created
// lazily on first write): each call's settings, prompt, output, and error
// land as {stamp}-{name}-{kind} files, so ls lists them chronologically.
// Empty — the default — disables them.
func SetDebugDir(dir string) {
	debugDir = dir
}

// explorerTools lets the explorer grep and read the DOM file the orchestrator
// captures for it each turn. The auditor gets no tools at all: it has no
// files, and a screen-reader user has nothing to grep.
var explorerTools = []string{"Read", "Grep"}

// request assembles the claude call shared by both agents' turns: the
// allow-listed tools, the optional system prompt file, and per-call debug
// files dumped into the debug dir.
func request(name, dir, promptText, schema, systemPromptFile, resumeID string, allowedTools []string) aclio.Request {
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
			AllowedTools:     allowedTools,
			TempDir:          debugDir,
		},
	}
}
