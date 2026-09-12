package args

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Usage renders a usage message for a single command: a USAGE synopsis line plus
// ARGUMENTS and FLAGS sections describing which are required and, for flags, how
// many values each takes. It returns the message — the caller decides how to
// surface it.
func Usage(command string, argsRequested []ArgRequested, flagsRequested []FlagRequested) string {
	prog := filepath.Base(os.Args[0])

	var b strings.Builder

	// USAGE synopsis: required args as <name>, optional as [<name>], then [flags].
	b.WriteString("USAGE\n")
	b.WriteString("  " + prog + " " + command)
	for _, a := range argsRequested {
		if a.Required {
			b.WriteString(" <" + a.Name + ">")
		} else {
			b.WriteString(" [<" + a.Name + ">]")
		}
	}
	if len(flagsRequested) > 0 {
		b.WriteString(" [flags]")
	}
	b.WriteString("\n")

	type row struct{ label, desc string }
	var argRows, flagRows []row

	for _, a := range argsRequested {
		argRows = append(argRows, row{"<" + a.Name + ">", required(a.Required)})
	}
	for _, f := range flagsRequested {
		desc := required(f.Required) + ", " + valueArity(f)
		flagRows = append(flagRows, row{"--" + f.Name, desc})
	}

	// Pad labels to a common width so descriptions line up across both sections.
	width := 0
	for _, r := range append(append([]row{}, argRows...), flagRows...) {
		if len(r.label) > width {
			width = len(r.label)
		}
	}

	writeSection := func(title string, rows []row) {
		if len(rows) == 0 {
			return
		}
		b.WriteString("\n" + title + "\n")
		for _, r := range rows {
			b.WriteString(fmt.Sprintf("  %-*s   %s\n", width, r.label, r.desc))
		}
	}

	writeSection("ARGUMENTS", argRows)
	writeSection("FLAGS", flagRows)

	return b.String()
}

func required(b bool) string {
	if b {
		return "required"
	}
	return "optional"
}

// valueArity describes how many values a flag accepts.
func valueArity(f FlagRequested) string {
	switch {
	case f.MaximumCount == 0:
		return "boolean"
	case f.MinimumCount == 1 && f.MaximumCount == 1:
		return "1 value"
	default:
		return fmt.Sprintf("%d-%d values", f.MinimumCount, f.MaximumCount)
	}
}
