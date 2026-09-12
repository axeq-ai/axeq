// Package app brings up the application under test as described by the
// repository's config: it runs the setup commands, starts the server in its
// own process group, and waits until the ready URL answers.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"axeq/internal/config"
)

// Process is a running application server.
type Process struct {
	cmd  *exec.Cmd
	done chan error
	log  *os.File
}

// Start runs the app's setup commands to completion, launches its start
// command, and blocks until the ready URL responds (status below 500) or the
// ready timeout passes. Command output is appended to logPath so it doesn't
// interleave with the audit's own output. dir is the repository root; a
// relative App.Cwd is resolved against it.
func Start(a config.App, dir, readyUrl, logPath string) (Process, error) {
	cwd := dir
	if a.Cwd != "" {
		cwd = a.Cwd
		if !filepath.IsAbs(cwd) {
			cwd = filepath.Join(dir, cwd)
		}
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return Process{}, fmt.Errorf("opening app log: %w", err)
	}

	env := os.Environ()
	for k, v := range a.Env {
		env = append(env, k+"="+v)
	}

	for _, setup := range a.Setup {
		fmt.Printf("[app] setup: %s\n", setup)
		fmt.Fprintf(logFile, "\n$ %s\n", setup)
		cmd := command(setup, cwd, env, logFile)
		if err := cmd.Run(); err != nil {
			logFile.Close()
			return Process{}, fmt.Errorf("setup command %q failed: %w (see %s)", setup, err, logPath)
		}
	}

	fmt.Printf("[app] start: %s\n", a.Start)
	fmt.Fprintf(logFile, "\n$ %s\n", a.Start)
	cmd := command(a.Start, cwd, env, logFile)
	// Own process group, so Stop can take down the whole tree — `npm run dev`
	// spawns the actual server as a child.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return Process{}, fmt.Errorf("starting app: %w", err)
	}

	p := Process{cmd: cmd, done: make(chan error, 1), log: logFile}
	go func() { p.done <- cmd.Wait() }()

	timeout := time.Duration(config.ReadyTimeoutSeconds(a)) * time.Second
	fmt.Printf("[app] waiting up to %s for %s\n", timeout, readyUrl)
	if err := waitReady(p, readyUrl, timeout); err != nil {
		Stop(p)
		return Process{}, fmt.Errorf("%w (see %s)", err, logPath)
	}
	fmt.Println("[app] ready")
	return p, nil
}

// Stop terminates the app's process group (SIGTERM, then SIGKILL after a
// grace period) and closes its log. Safe on a zero Process.
func Stop(p Process) {
	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	defer p.log.Close()

	pgid := -p.cmd.Process.Pid
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	select {
	case <-p.done:
		return
	case <-time.After(5 * time.Second):
	}
	_ = syscall.Kill(pgid, syscall.SIGKILL)
	<-p.done
}

// waitReady polls readyUrl until it answers with a status below 500, the
// process exits, or the timeout passes.
func waitReady(p Process, readyUrl string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for {
		select {
		case err := <-p.done:
			// Put it back so Stop's wait still returns.
			p.done <- err
			return fmt.Errorf("app exited before becoming ready: %v", exitError(err))
		default:
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, readyUrl, nil)
		resp, err := client.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		cancel()
		if err == nil && resp.StatusCode < 500 {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("app did not answer at %s within %s", readyUrl, timeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// command builds a `sh -c` invocation with both output streams on w.
func command(script, cwd string, env []string, w io.Writer) *exec.Cmd {
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = cwd
	cmd.Env = env
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd
}

func exitError(err error) string {
	if err == nil {
		return "exit status 0"
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.String()
	}
	return err.Error()
}
