// Package github fetches what the pull_request command needs from the GitHub
// API: the pull request's metadata and its unified diff.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
)

// PullRequest is the subset of the API's pull request object the audit uses.
type PullRequest struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	HtmlUrl string `json:"html_url"`
	State   string `json:"state"`
	Head    struct {
		Ref string `json:"ref"`
		Sha string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		Sha string `json:"sha"`
	} `json:"base"`
	ChangedFiles int `json:"changed_files"`
	// Diff is the unified diff, fetched separately and not part of the API's
	// JSON object.
	Diff string `json:"-"`
}

// ErrUnauthorized is returned when the token is missing or rejected.
var ErrUnauthorized = errors.New("github: unauthorized")

// FetchPullRequest gets the pull request's metadata and diff. owner/repo name
// the repository, token is an installation or personal token with read access
// to it.
func FetchPullRequest(owner, repo string, number int, token string) (PullRequest, error) {
	if token == "" {
		return PullRequest{}, fmt.Errorf("%w: no token given", ErrUnauthorized)
	}
	base := "https://api.github.com/repos/" + path.Join(owner, repo, "pulls", fmt.Sprint(number))
	client := &http.Client{Timeout: 60 * time.Second}

	var pr PullRequest
	body, err := get(client, base, "application/vnd.github+json", token)
	if err != nil {
		return PullRequest{}, fmt.Errorf("fetching pull request #%d of %s/%s: %w", number, owner, repo, err)
	}
	if err := json.Unmarshal(body, &pr); err != nil {
		return PullRequest{}, fmt.Errorf("parsing pull request #%d: %w", number, err)
	}

	diff, err := get(client, base, "application/vnd.github.diff", token)
	if err != nil {
		return PullRequest{}, fmt.Errorf("fetching diff of pull request #%d: %w", number, err)
	}
	pr.Diff = string(diff)
	return pr, nil
}

func get(client *http.Client, url, accept, token string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w (HTTP %d): %s", ErrUnauthorized, resp.StatusCode, firstLine(body))
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, firstLine(body))
	}
	return body, nil
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// MaxDiffBytes is how much of a diff the agents get to read. Anything past it
// is cut, file by file, with a note of what was left out.
const MaxDiffBytes = 200 * 1024

// noisyDiffFiles are the paths whose diffs carry no information about the
// user interface: lockfiles, minified or generated bundles, source maps,
// snapshots, and vendored dependencies.
var noisyDiffFiles = []func(path string) bool{
	hasBase("package-lock.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb", "go.sum", "Cargo.lock", "poetry.lock", "Pipfile.lock", "composer.lock", "Gemfile.lock"),
	hasSuffix(".min.js", ".min.css", ".map", ".snap", ".lock", ".svg", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".woff", ".woff2", ".ttf"),
	hasDir("node_modules", "vendor", "dist", "build", ".next", "out", "coverage"),
}

// TrimDiff drops the noisy files from a unified diff and caps its size, so
// the agents see the changes that can affect the interface and nothing else.
// The returned notes describe what was removed, for the report.
func TrimDiff(diff string) (trimmed string, notes []string) {
	files := splitDiff(diff)
	var kept []string
	var dropped []string
	size := 0
	for _, f := range files {
		p := diffPath(f)
		if isNoisy(p) {
			dropped = append(dropped, p)
			continue
		}
		if size+len(f) > MaxDiffBytes {
			dropped = append(dropped, p+" (over the size limit)")
			continue
		}
		kept = append(kept, f)
		size += len(f)
	}
	if len(dropped) > 0 {
		notes = append(notes, fmt.Sprintf("%d file(s) left out of the diff shown to the agents: %s", len(dropped), strings.Join(dropped, ", ")))
	}
	return strings.Join(kept, ""), notes
}

// ChangedFiles lists the paths a unified diff touches, in order.
func ChangedFiles(diff string) []string {
	var out []string
	for _, f := range splitDiff(diff) {
		out = append(out, diffPath(f))
	}
	return out
}

// splitDiff cuts a unified diff into per-file chunks, each starting at its
// "diff --git" line.
func splitDiff(diff string) []string {
	var files []string
	for _, chunk := range strings.Split("\n"+diff, "\ndiff --git ") {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		files = append(files, "diff --git "+strings.TrimPrefix(chunk, "\n"))
	}
	return files
}

// diffPath is the new-side path of one file's chunk ("b/…" on the header
// line), or the old-side path for a deletion.
func diffPath(chunk string) string {
	header, _, _ := strings.Cut(chunk, "\n")
	fields := strings.Fields(header)
	if len(fields) >= 4 {
		return strings.TrimPrefix(fields[3], "b/")
	}
	return header
}

func isNoisy(p string) bool {
	for _, f := range noisyDiffFiles {
		if f(p) {
			return true
		}
	}
	return false
}

func hasBase(names ...string) func(string) bool {
	return func(p string) bool {
		b := path.Base(p)
		for _, n := range names {
			if b == n {
				return true
			}
		}
		return false
	}
}

func hasSuffix(suffixes ...string) func(string) bool {
	return func(p string) bool {
		for _, s := range suffixes {
			if strings.HasSuffix(p, s) {
				return true
			}
		}
		return false
	}
}

func hasDir(dirs ...string) func(string) bool {
	return func(p string) bool {
		for _, seg := range strings.Split(path.Dir(p), "/") {
			for _, d := range dirs {
				if seg == d {
					return true
				}
			}
		}
		return false
	}
}
