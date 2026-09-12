package interaction

var debugDir string

// SetDebugDir enables aclio-go's per-call debug dumps into dir (created
// lazily on first write): each call's settings, prompt, output, and error
// land as {stamp}-{name}-{kind} files, so ls lists them chronologically.
// Empty — the default — disables them.
func SetDebugDir(dir string) {
	debugDir = dir
}
