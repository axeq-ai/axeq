package args

import (
	"fmt"
	"os"
	"strings"
)

// ExtractArgs splits a raw argument vector (such as os.Args) into the command,
// the remaining positional arguments, and the flags.
//
// The first element of argv is the invocation name (the binary path in os.Args)
// and is ignored. The second element — the first raw token after it — is returned
// verbatim as command, before any flag/arg parsing (so a flag-looking first token
// becomes the command, which the caller can then reject). It panics when there is
// no token after the invocation name (i.e. no command at all). Everything after
// the command is parsed into args and flags.
//
// Supported flag forms:
//
//	--name=value   long flag, value attached with '='
//	--name value   long flag, value is the following token
//	--name         long flag, no value (next token is a flag, or absent)
//	-a value       short (single-letter) flag, value is the following token
//	-av            short flag with the value attached, no '='
//
// A token is treated as a flag when it begins with '-'. The token following a
// bare long flag, or a bare short flag, is only consumed as that flag's value
// when it is not itself a flag. Flags may appear before, after, or between
// positional args.
//
// singleLetterFlagMappings aliases single-letter flag names to their long
// form, e.g. {"f": "ff"} (or {"-v": "--version"} — leading dashes on either
// side are ignored). After parsing, any flag whose name matches a mapping key
// is rewritten to the mapped long name.
//
// Flags are returned as a map from resolved name to the ordered list of every
// value seen for that name — no last-wins, all occurrences preserved in
// invocation order. A flag that appears with no value contributes its key with
// an empty (non-nil) slice.
func ExtractArgs(argv []string, singleLetterFlagMappings map[string]string) (command string, args []string, flags map[string][]string) {
	args = []string{}
	flags = map[string][]string{}

	if len(argv) <= 1 {
		fmt.Fprintln(os.Stderr, "no command provided")
		os.Exit(1)
	}
	// The command is the first raw token after the invocation name, taken
	// verbatim before any flag/arg parsing. Everything after it is parsed.
	command = argv[1]
	if len(argv) <= 2 {
		return command, args, flags
	}
	tokens := argv[2:]

	// Normalise the alias table so lookups work whether the caller wrote names
	// with or without leading dashes.
	aliases := make(map[string]string, len(singleLetterFlagMappings))
	for k, v := range singleLetterFlagMappings {
		aliases[strings.TrimLeft(k, "-")] = strings.TrimLeft(v, "-")
	}

	// rawFlags holds each parsed flag as {name} (no value) or {name, value},
	// in invocation order, before alias resolution.
	var rawFlags [][]string

	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]

		var name, value string
		var hasValue, isFlag bool

		switch {
		case strings.HasPrefix(tok, "--"):
			body := strings.TrimLeft(tok, "-")
			if body == "" {
				break // bare "--": fall through to a positional arg
			}
			isFlag = true
			if eq := strings.IndexByte(body, '='); eq >= 0 {
				name, value, hasValue = body[:eq], body[eq+1:], true
			} else {
				name = body
				if i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1], "-") {
					value, hasValue = tokens[i+1], true
					i++
				}
			}
		case strings.HasPrefix(tok, "-") && len(tok) > 1:
			isFlag = true
			name = tok[1:2] // single letter
			if len(tok) > 2 {
				// Attached value, e.g. -av (tolerate a stray '=' too).
				value, hasValue = strings.TrimPrefix(tok[2:], "="), true
			} else if i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1], "-") {
				value, hasValue = tokens[i+1], true
				i++
			}
		}

		if !isFlag {
			args = append(args, tok)
			continue
		}

		if hasValue {
			rawFlags = append(rawFlags, []string{name, value})
		} else {
			rawFlags = append(rawFlags, []string{name})
		}
	}

	// Resolve single-letter aliases, then collapse into the values map while
	// preserving order. A value-less flag still registers its (empty) key.
	for _, f := range rawFlags {
		name := f[0]
		if long, ok := aliases[name]; ok {
			name = long
		}
		if _, seen := flags[name]; !seen {
			flags[name] = []string{}
		}
		if len(f) > 1 {
			flags[name] = append(flags[name], f[1])
		}
	}

	return command, args, flags
}
