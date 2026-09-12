package main

import (
	"axeq/cmd/pull_request"
	"axeq/cmd/pull_request_comment"
	"axeq/cmd/schedule"
	"axeq/internal/args"
	"axeq/internal/auth"
	"axeq/internal/deps"
	"fmt"
	"os"
)

// commands routes the leading positional arg directly to its handler. Each
// command maps straight to its cmd/<name> package's Run function, which
// receives the remaining positional args and the parsed flags.
var commands = map[string]func([]string, map[string][]string){
	"schedule":             schedule.Run,
	"pull_request":         pull_request.Run,
	"pull_request_comment": pull_request_comment.Run,
}

// flagMappings aliases single-letter flags to their long form. Add entries
// here as commands grow (e.g. "v": "version").
var flagMappings = map[string]string{
	"-h": "--help",
}

func main() {
	if err := deps.CheckDeps(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := auth.CheckAuth(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	command, parsed, flags := args.ExtractArgs(os.Args, flagMappings)

	fn, ok := commands[command]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", command)
		os.Exit(1)
	}

	fn(parsed, flags)
}
