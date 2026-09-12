package pull_request

import (
	"axeq/internal/args"
	"axeq/internal/claude/interaction"
	"axeq/internal/extension"
	browser "axeq/internal/playwright"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	aclio "github.com/agenticcliorchestra/aclio-go"
)

// framesFPS is the capture and playback frame rate for the recording.
const framesFPS = 15

// backend is the browser-automation surface the task loop drives, filled with
// either the playwright package's functions (its own Chromium) or the
// extension package's (the user's normal Chrome via the bridge extension).
// Both operate on the same action and state types, so the agent conversation
// is identical either way.
type backend struct {
	openInitialTabs func(urls []string) error
	waitForLoad     func() error
	performActions  func(actions []browser.Action) []browser.PerformedAction
	snapshot        func(dir string) (screenshotPath, domPath string, err error)
	focused         func() (*browser.FocusedElement, error)
	tabs            func() []browser.Tab
}

func Run(argsReceived []string, flagsReceived map[string][]string) {
	// run's deferred cleanup (browser/extension teardown) must fire before the
	// process exits, and os.Exit skips defers — so the exit happens out here,
	// after run has returned.
	if code := run(argsReceived, flagsReceived); code != 0 {
		os.Exit(code)
	}
}

func run(argsReceived []string, flagsReceived map[string][]string) int {
	argsRequested := []args.ArgRequested{}
	flagsRequested := []args.FlagRequested{
		{Name: "url", MinimumCount: 0, MaximumCount: 100, Required: false},
		{Name: "prompt", MinimumCount: 1, MaximumCount: 1, Required: false, MutuallyExclusiveRequired: []string{"prompt-path"}},
		{Name: "prompt-path", MinimumCount: 1, MaximumCount: 1, Required: false, MutuallyExclusiveRequired: []string{"prompt"}},
		{Name: "preset", MinimumCount: 1, MaximumCount: 1},
		{Name: "driver", MinimumCount: 1, MaximumCount: 1},
		{Name: "extension-port", MinimumCount: 1, MaximumCount: 1},
		{Name: "system-prompt-file", MinimumCount: 1, MaximumCount: 1},
		{Name: "user-data-dir", MinimumCount: 1, MaximumCount: 1},
		{Name: "viewport", MinimumCount: 1, MaximumCount: 1},
		{Name: "attachment", MinimumCount: 1, MaximumCount: 100, MutuallyExclusive: []string{"attachments-dir"}},
		{Name: "attachments-dir", MinimumCount: 1, MaximumCount: 1, MutuallyExclusive: []string{"attachment"}},
		{Name: "max-attachments", MinimumCount: 1, MaximumCount: 1},
		{Name: "headed", MinimumCount: 0, MaximumCount: 0},
		{Name: "no-video", MinimumCount: 0, MaximumCount: 0},
	}

	_, cleanFlags := args.ValidateArgs("pull_request", argsReceived, flagsReceived, argsRequested, flagsRequested)

	// Optional, repeatable: each --url opens in its own tab, in order. None given
	// means a single about:blank tab. Each must be an absolute URL.
	urls := cleanFlags["url"]
	for _, raw := range urls {
		if u, err := url.Parse(raw); err != nil || u.Scheme == "" || u.Host == "" {
			fatalf("invalid --url %q (expected an absolute URL like https://example.com)", raw)
		}
	}

	// Preset.
	preset := flagValue(cleanFlags, "preset", "default")
	switch preset {
	case "default", "blind", "motor":
	default:
		fatalf("invalid --preset %q (expected: default, blind, motor)", preset)
	}

	// Browser driver: playwright launches and drives its own Chromium;
	// extension drives the user's normal Chrome through the bridge extension in
	// test-chrome-extension/ — for sensitive sites whose bot prevention blocks
	// automated browsers.
	driver := flagValue(cleanFlags, "driver", "playwright")
	switch driver {
	case "playwright", "extension":
	default:
		fatalf("invalid --driver %q (expected: playwright, extension)", driver)
	}
	extensionPort, err := strconv.Atoi(flagValue(cleanFlags, "extension-port", "8377"))
	if err != nil || extensionPort < 1 || extensionPort > 65535 {
		fatalf("invalid --extension-port %q (expected a port number)", flagValue(cleanFlags, "extension-port", ""))
	}

	// Optional system prompt file, passed through to the claude CLI's
	// --system-prompt-file. Resolved to an absolute path because the CLI runs
	// from the agent dir, not the CWD this program was launched from.
	systemPromptFile := flagValue(cleanFlags, "system-prompt-file", "")
	if systemPromptFile != "" {
		abs, err := filepath.Abs(systemPromptFile)
		if err != nil {
			panic(fmt.Errorf("resolving absolute path for system prompt file: %w", err))
		}
		if _, err := os.Stat(abs); err != nil {
			fatalf("system prompt file %q not accessible: %v", abs, err)
		}
		systemPromptFile = abs
	}

	// Viewport size (WIDTHxHEIGHT, e.g. 1440x900).
	viewport := flagValue(cleanFlags, "viewport", "1440x900")
	viewportWidth, viewportHeight := parseViewport(viewport)

	// Optional persistent browser profile dir.
	userDataDir := flagValue(cleanFlags, "user-data-dir", "")

	// Boolean flags: presence means true.
	_, headed := cleanFlags["headed"]
	_, noVideo := cleanFlags["no-video"]

	fmt.Println(urls)
	fmt.Println(preset)
	fmt.Println(viewport)
	fmt.Println(headed)

	// The user's prompt drives the agent. Exactly one of --prompt (literal text)
	// or --prompt-path (a file to read) is set — ValidateArgs enforces that.
	userPrompt := flagValue(cleanFlags, "prompt", "")
	if promptPath := flagValue(cleanFlags, "prompt-path", ""); promptPath != "" {
		abs, err := filepath.Abs(promptPath)
		if err != nil {
			panic(fmt.Errorf("resolving absolute path for prompt file: %w", err))
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			fatalf("prompt file %q not accessible: %v", abs, err)
		}
		userPrompt = string(b)
	}

	// Attachments for the agent, given either as repeated --attachment files or
	// as one --attachments-dir tree — never both (ValidateArgs enforces that).
	// Sources are validated up front and copied into {tmpDir}/agent/attachments
	// once that dir exists; the initial prompt lists each for the agent as
	// attachments/{name}.
	//
	// --attachment (up to 100): each value is a file, copied flat under its base
	// name, so base names must be unique.
	attachmentPaths := cleanFlags["attachment"]
	var attachmentNames []string
	seenAttachmentNames := map[string]bool{}
	for i, raw := range attachmentPaths {
		abs, err := filepath.Abs(expandTilde(raw))
		if err != nil {
			panic(fmt.Errorf("resolving absolute path for attachment: %w", err))
		}
		info, err := os.Stat(abs)
		if err != nil {
			fatalf("attachment %q not accessible: %v", raw, err)
		}
		if info.IsDir() {
			fatalf("attachment %q is a directory (expected a file)", raw)
		}
		name := filepath.Base(abs)
		if seenAttachmentNames[name] {
			fatalf("duplicate attachment file name %q (attachments are copied into one directory, so file names must be unique)", name)
		}
		seenAttachmentNames[name] = true
		attachmentPaths[i] = abs
		attachmentNames = append(attachmentNames, name)
	}

	// --max-attachments caps how many files --attachments-dir may pick up — a
	// guard against attaching a huge tree by accident. Default 25, hard max 100,
	// and meaningless without --attachments-dir (--attachment has only the hard
	// flag-validator max of 100).
	maxAttachments := 25
	if raw, ok := cleanFlags["max-attachments"]; ok {
		if _, ok := cleanFlags["attachments-dir"]; !ok {
			fatalf("--max-attachments is only valid together with --attachments-dir")
		}
		n, err := strconv.Atoi(raw[0])
		if err != nil || n < 1 || n > 100 {
			fatalf("invalid --max-attachments %q (expected a number between 1 and 100)", raw[0])
		}
		maxAttachments = n
	}

	// --attachments-dir: attach every supported file (image, video, or markdown)
	// under the dir, recursively, keeping the directory structure below it — a
	// file at {dir}/a/some-image.png becomes attachments/a/some-image.png.
	if dirRaw := flagValue(cleanFlags, "attachments-dir", ""); dirRaw != "" {
		root, err := filepath.Abs(expandTilde(dirRaw))
		if err != nil {
			panic(fmt.Errorf("resolving absolute path for attachments dir: %w", err))
		}
		info, err := os.Stat(root)
		if err != nil {
			fatalf("attachments dir %q not accessible: %v", dirRaw, err)
		}
		if !info.IsDir() {
			fatalf("attachments dir %q is not a directory (for single files, use --attachment)", dirRaw)
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !attachmentExtSupported(p) {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			attachmentPaths = append(attachmentPaths, p)
			attachmentNames = append(attachmentNames, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			panic(fmt.Errorf("scanning attachments dir: %w", err))
		}
		if len(attachmentPaths) == 0 {
			fatalf("attachments dir %q contains no supported files (images, videos, or markdown)", dirRaw)
		}
		if len(attachmentPaths) > maxAttachments {
			fatalf("attachments dir %q contains %d supported files, more than the limit of %d (raise it with --max-attachments, up to 100)", dirRaw, len(attachmentPaths), maxAttachments)
		}
	}

	// Per-run temp dir holds the video and screenshots.
	tmpDir, err := os.MkdirTemp("", "ai-browser-connector-*")
	if err != nil {
		panic(fmt.Errorf("creating temp dir: %w", err))
	}
	fmt.Printf("Temp dir: %s\n", tmpDir)

	// Everything the agent emits for the user lands under one CWD-relative,
	// per-run folder: outputs/{run}/files for the write-file action and
	// outputs/{run}/screenshots for the screenshot action. Siblings, so a
	// written markdown file can link a screenshot as screenshots/{name}.png.
	// Each subdir is created lazily on first use. Both backends resolve
	// screenshot names against their own copy of the setting.
	runOutDir := filepath.Join("outputs", filepath.Base(tmpDir))
	screenshotsOutDir := filepath.Join(runOutDir, "screenshots")
	browser.SetScreenshotOutputDir(screenshotsOutDir)
	extension.SetScreenshotOutputDir(screenshotsOutDir)
	fmt.Printf("Screenshots dir: %s\n", screenshotsOutDir)

	filesOutDir := filepath.Join(runOutDir, "files")
	browser.SetOutputsDir(filesOutDir)
	fmt.Printf("Files dir: %s\n", filesOutDir)

	// Every claude call dumps its settings, prompt, and output here for
	// debugging, timestamp-prefixed so ls lists them chronologically.
	debugDir := filepath.Join(tmpDir, "debug")
	interaction.SetDebugDir(debugDir)
	fmt.Printf("Debug dir: %s\n", debugDir)

	// Agent working dir: claude runs from here and screenshots are written here.
	agentDir := filepath.Join(tmpDir, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		panic(fmt.Errorf("creating agent dir: %w", err))
	}

	// Attachments live under the agent dir so the agent (which runs from there)
	// can Read them by the relative attachments/{name} paths the initial prompt
	// lists. Names from --attachments-dir keep their subdirectory structure, so
	// each destination's parent dir is created as needed.
	if len(attachmentPaths) > 0 {
		attachmentsDir := filepath.Join(agentDir, "attachments")
		for i, src := range attachmentPaths {
			dst := filepath.Join(attachmentsDir, filepath.FromSlash(attachmentNames[i]))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				panic(fmt.Errorf("creating attachments dir: %w", err))
			}
			if err := copyFile(src, dst); err != nil {
				panic(fmt.Errorf("copying attachment %q: %w", src, err))
			}
		}
		fmt.Printf("Attachments dir: %s\n", attachmentsDir)
	}

	// The chosen driver supplies the loop's browser operations. Both expose the
	// same free-function surface over the same action types, so the agent
	// drives either one identically.
	var be backend
	if driver == "extension" {
		// The extension drives the user's real Chrome: viewport, headedness, and
		// profile belong to that browser, and video recording (15fps screenshot
		// sampling) exceeds captureVisibleTab's rate limit.
		for _, f := range []string{"viewport", "headed", "user-data-dir"} {
			if _, ok := cleanFlags[f]; ok {
				fmt.Printf("note: --%s is ignored with --driver extension (the user's own Chrome is driven)\n", f)
			}
		}
		if !noVideo {
			fmt.Println("note: --driver extension cannot record video; continuing without recording")
			noVideo = true
		}

		if err := extension.Prepare(extensionPort); err != nil {
			panic(fmt.Errorf("preparing extension bridge: %w", err))
		}
		defer extension.Teardown()

		fmt.Printf("Extension bridge listening on http://127.0.0.1:%d\n", extensionPort)
		fmt.Println("Waiting for the extension: load test-chrome-extension/ at chrome://extensions, enable it in its popup, and adopt the tab to drive.")
		if err := extension.WaitForExtension(5 * time.Minute); err != nil {
			fatalf("%v", err)
		}
		fmt.Println("Extension connected.")

		be = backend{
			openInitialTabs: extension.OpenInitialTabs,
			waitForLoad:     extension.WaitForLoad,
			performActions:  extension.PerformActions,
			snapshot:        extension.Snapshot,
			focused:         extension.Focused,
			tabs:            extension.Tabs,
		}
	} else {
		// Persistent browser profile: default into the temp dir when not specified.
		if userDataDir == "" {
			userDataDir = filepath.Join(tmpDir, "user-data")
		}
		fmt.Println(userDataDir)

		if err := browser.Prepare(filepath.Join(tmpDir, "playwright"), userDataDir, viewportWidth, viewportHeight, !headed); err != nil {
			panic(fmt.Errorf("preparing browser: %w", err))
		}
		defer browser.Teardown()

		be = backend{
			openInitialTabs: browser.OpenInitialTabs,
			waitForLoad:     browser.WaitForLoad,
			performActions:  browser.PerformActions,
			snapshot:        browser.Snapshot,
			focused:         browser.Focused,
			tabs:            browser.Tabs,
		}
	}

	if err := be.openInitialTabs(urls); err != nil {
		panic(fmt.Errorf("opening initial tabs: %w", err))
	}
	if err := be.waitForLoad(); err != nil {
		panic(err)
	}

	// Constant-rate frame recording into {tmpDir}/frames, started once the page
	// has loaded and paused while waiting on the agent so those gaps don't appear
	// in the output. The native full video (in {tmpDir}/playwright) is untouched.
	// --no-video disables only this recording; rec stays nil and its methods
	// no-op.
	var rec *browser.Recorder
	if !noVideo {
		rec, err = browser.StartRecording(filepath.Join(tmpDir, "frames"), framesFPS)
		if err != nil {
			panic(fmt.Errorf("starting recorder: %w", err))
		}
	}

	pset := browser.Preset(preset)

	// First turn: capture the landing-page state, then kick off the conversation.
	// Blind mode is a screen-reader simulation: no screenshot, no DOM — just the
	// focused element. Other presets capture the screenshot + DOM.
	var shotPath, domPath string
	var focused *browser.FocusedElement
	if pset == browser.PresetBlind {
		focused, err = be.focused()
	} else {
		shotPath, domPath, err = be.snapshot(agentDir)
	}
	if err != nil {
		panic(err)
	}
	rec.Pause()
	convoId, resp, err := interaction.Initial(agentDir, userPrompt, systemPromptFile, pset, shotPath, domPath, focused, be.tabs(), attachmentNames)
	rec.Resume()
	if err != nil {
		if errors.Is(err, aclio.ErrInterrupted) {
			fmt.Fprintln(os.Stderr, "interrupted")
			return 130
		}
		panic(fmt.Errorf("agent interaction: %w", err))
	}

	for !resp.TaskComplete {
		performed := be.performActions(resp.PerformActions)

		// Blind mode is a screen-reader simulation: no screenshot, no DOM — just
		// the focused element. Other presets capture the screenshot + DOM.
		var shotPath, domPath string
		var focused *browser.FocusedElement
		if pset == browser.PresetBlind {
			focused, err = be.focused()
		} else {
			shotPath, domPath, err = be.snapshot(agentDir)
		}
		if err != nil {
			panic(err)
		}

		rec.Pause()
		resp, err = interaction.Subsequent(convoId, agentDir, performed, systemPromptFile, pset, shotPath, domPath, focused, be.tabs())
		rec.Resume()
		if err != nil {
			if errors.Is(err, aclio.ErrInterrupted) {
				fmt.Fprintln(os.Stderr, "interrupted")
				return 130
			}
			panic(fmt.Errorf("agent interaction: %w", err))
		}
	}

	if rec != nil {
		framesDir := rec.Stop()
		fmt.Printf("Frames: %s\n", framesDir)

		recordingPath := filepath.Join(tmpDir, "recording", "recording.mp4")
		if err := browser.EncodeVideo(framesDir, recordingPath, framesFPS); err != nil {
			panic(fmt.Errorf("encoding recording: %w", err))
		}
		fmt.Println(recordingPath)
	}

	// The agent's answer/output for the user's request, if any (set only on the
	// final, completed turn).
	if resp.UserRequestedInformation != nil {
		fmt.Println(*resp.UserRequestedInformation)
	}
	return 0
}

// Attachment types --attachments-dir picks up, by extension (matched
// case-insensitively): images, videos, and markdown.
var (
	attachmentImageExts    = []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff", ".heic"}
	attachmentVideoExts    = []string{".mp4", ".mov", ".mpeg", ".mpg", ".m4v", ".avi", ".webm", ".mkv"}
	attachmentMarkdownExts = []string{".md", ".markdown"}
)

// attachmentExtSupported reports whether the file is a supported attachment
// type (image, video, or markdown) going by its extension.
func attachmentExtSupported(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, group := range [][]string{attachmentImageExts, attachmentVideoExts, attachmentMarkdownExts} {
		for _, e := range group {
			if ext == e {
				return true
			}
		}
	}
	return false
}

// expandTilde resolves a leading "~" or "~/" to the user's home directory —
// the shell doesn't expand it when the path was quoted.
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			panic(fmt.Errorf("resolving home directory: %w", err))
		}
		return filepath.Join(home, p[1:])
	}
	return p
}

// copyFile copies src to dst (streamed, so large attachments like videos don't
// load into memory), creating or truncating dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// flagValue returns the single value of a validated flag, or def when the flag
// was not provided.
func flagValue(flags map[string][]string, name, def string) string {
	if v, ok := flags[name]; ok && len(v) > 0 {
		return v[0]
	}
	return def
}

// parseViewport parses a WIDTHxHEIGHT string (case-insensitive x), e.g. 1440x900.
func parseViewport(s string) (int, int) {
	parts := strings.Split(strings.ToLower(s), "x")
	if len(parts) != 2 {
		fatalf("invalid --viewport %q (expected WIDTHxHEIGHT, e.g. 1440x900)", s)
	}

	width, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || width <= 0 {
		fatalf("invalid --viewport width in %q (expected WIDTHxHEIGHT, e.g. 1440x900)", s)
	}
	height, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || height <= 0 {
		fatalf("invalid --viewport height in %q (expected WIDTHxHEIGHT, e.g. 1440x900)", s)
	}
	return width, height
}

// fatalf reports a user-caused error (bad input, unusable file, ...) to stderr
// and exits 1. Panics stay reserved for internal errors, where the stack trace
// is the debugging aid.
func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
