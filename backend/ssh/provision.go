package ssh

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

// Directive represents a parsed Dockerfile instruction.
type Directive struct {
	Command string
	Args    []string
}

const markerDir = "/var/lib/silo"
const markerFile = markerDir + "/provision.hash"

// stage groups directives that belong to a single FROM ... AS <name> block.
type stage struct {
	name       string
	from       string // base image or stage name
	directives []Directive
}

// TODO: use the moby buildkit dockerfile parser
// (https://github.com/moby/buildkit/blob/master/frontend/dockerfile/parser/parser.go)
// to parse the Dockerfile instead?

var fromRe = regexp.MustCompile(`(?i)^FROM\s+(\S+)(?:\s+AS\s+(\S+))?`)

// parseStages parses a Dockerfile string into ordered stages.
func parseStages(dockerfile string) []stage {
	var stages []stage
	var cur *stage
	for _, raw := range strings.Split(dockerfile, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if m := fromRe.FindStringSubmatch(line); m != nil {
			stages = append(stages, stage{
				name: strings.ToLower(m[2]),
				from: m[1],
			})
			cur = &stages[len(stages)-1]
			continue
		}

		if cur == nil {
			// Directives before any FROM (e.g. global ARGs) — attach to a
			// synthetic unnamed stage so they are not lost.
			stages = append([]stage{{name: ""}}, stages...)
			cur = &stages[0]
		}

		cmd, args := splitDirective(line)
		if cmd == "" {
			continue
		}
		cur.directives = append(cur.directives, Directive{Command: cmd, Args: args})
	}
	return stages
}

// splitDirective splits a Dockerfile line into the command and its arguments.
// It handles line continuations (trailing backslash) within a single line value
// but expects the caller to have already joined continuation lines if needed.
func splitDirective(line string) (string, []string) {
	// Handle continuation lines — strip trailing backslash.
	line = strings.TrimRight(line, " \t")
	line = strings.TrimSuffix(line, "\\")
	line = strings.TrimSpace(line)

	idx := strings.IndexAny(line, " \t")
	if idx == -1 {
		return strings.ToUpper(line), nil
	}
	cmd := strings.ToUpper(line[:idx])
	rest := strings.TrimSpace(line[idx+1:])
	return cmd, splitArgs(cmd, rest)
}

// splitArgs splits the argument portion of a directive. For RUN we keep
// the entire string as a single argument so the shell command is preserved.
func splitArgs(cmd, rest string) []string {
	switch cmd {
	case "RUN", "WORKDIR", "USER", "EXPOSE", "CMD", "ENTRYPOINT", "SHELL":
		return []string{rest}
	default:
		return tokenize(rest)
	}
}

// tokenize performs simple space-delimited tokenization respecting double
// quotes (no escape handling — sufficient for the directives silo supports).
func tokenize(s string) []string {
	var tokens []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t') && !inQuote:
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

// findStages returns a map of stage name to directives.
func findStages(dockerfile string) map[string][]Directive {
	stages := parseStages(dockerfile)
	m := make(map[string][]Directive, len(stages))
	for _, s := range stages {
		if s.name != "" {
			m[s.name] = s.directives
		}
	}
	return m
}

// DockerfileToShell converts a Dockerfile to a bash provisioning script.
// If target is non-empty, only the matching stage (and its FROM-referenced
// stages) are included. If target is empty, all stages are concatenated.
func DockerfileToShell(dockerfile string, target string) (string, error) {
	stages := parseStages(dockerfile)
	if len(stages) == 0 {
		return "", fmt.Errorf("no stages found in Dockerfile")
	}

	// Build a lookup by name.
	byName := make(map[string]*stage, len(stages))
	for i := range stages {
		if stages[i].name != "" {
			byName[stages[i].name] = &stages[i]
		}
	}

	// Determine which stages to emit.
	var ordered []*stage
	if target != "" {
		tgt, ok := byName[strings.ToLower(target)]
		if !ok {
			// If there is only one stage, use it regardless of name.
			if len(stages) == 1 {
				tgt = &stages[0]
			} else {
				return "", fmt.Errorf("stage not found: %s", target)
			}
		}
		// Walk the FROM chain to include prerequisite stages.
		ordered = resolveChain(tgt, byName)
	} else {
		for i := range stages {
			ordered = append(ordered, &stages[i])
		}
	}

	var script strings.Builder
	script.WriteString("#!/bin/bash\nset -euo pipefail\n\n")

	currentUser := ""
	currentDir := ""

	for _, s := range ordered {
		for _, d := range s.directives {
			line := directiveToShell(d, &currentUser, &currentDir)
			if line != "" {
				script.WriteString(line)
				script.WriteString("\n")
			}
		}
	}

	return script.String(), nil
}

// resolveChain walks the FROM references to produce an ordered list of stages
// to emit, starting from the earliest dependency.
func resolveChain(target *stage, byName map[string]*stage) []*stage {
	var chain []*stage
	seen := map[string]bool{}
	var walk func(s *stage)
	walk = func(s *stage) {
		if s == nil {
			return
		}
		key := s.name
		if key == "" {
			key = s.from
		}
		if seen[key] {
			return
		}
		seen[key] = true
		// If this stage's FROM references another local stage, include it first.
		if parent, ok := byName[strings.ToLower(s.from)]; ok {
			walk(parent)
		}
		chain = append(chain, s)
	}
	walk(target)
	return chain
}

// directiveToShell converts a single Directive to shell code.
func directiveToShell(d Directive, currentUser *string, currentDir *string) string {
	switch d.Command {
	case "RUN":
		if len(d.Args) == 0 {
			return ""
		}
		cmd := d.Args[0]
		if *currentDir != "" {
			cmd = fmt.Sprintf("cd %s && %s", shellQuote(*currentDir), cmd)
		}
		if *currentUser != "" && *currentUser != "root" {
			return fmt.Sprintf("su - %s -c %s", shellQuote(*currentUser), shellQuote(cmd))
		}
		return cmd

	case "ENV":
		return envToShell(d.Args)

	case "WORKDIR":
		if len(d.Args) == 0 {
			return ""
		}
		path := d.Args[0]
		*currentDir = path
		return fmt.Sprintf("mkdir -p %s", shellQuote(path))

	case "USER":
		if len(d.Args) == 0 {
			return ""
		}
		*currentUser = d.Args[0]
		return fmt.Sprintf("# USER %s", d.Args[0])

	case "ARG":
		return argToShell(d.Args)

	case "ADD":
		return addToShell(d.Args)

	case "COPY":
		// Files come from sync — skip.
		return "# COPY skipped (files come from sync)"

	case "EXPOSE":
		if len(d.Args) > 0 {
			return fmt.Sprintf("# EXPOSE %s", d.Args[0])
		}
		return ""

	case "LABEL", "STOPSIGNAL", "HEALTHCHECK", "ONBUILD",
		"CMD", "ENTRYPOINT", "SHELL", "VOLUME":
		// These don't have meaningful shell equivalents for provisioning.
		return ""

	default:
		return ""
	}
}

// envToShell converts ENV arguments to export statements.
// Handles both ENV KEY=VALUE and ENV KEY VALUE forms.
func envToShell(args []string) string {
	if len(args) == 0 {
		return ""
	}
	var lines []string
	for _, a := range args {
		if k, v, ok := strings.Cut(a, "="); ok {
			lines = append(lines, fmt.Sprintf("export %s=%s", k, shellQuote(v)))
		} else {
			// ENV KEY VALUE form: first arg is key, rest is value.
			if len(args) >= 2 {
				return fmt.Sprintf("export %s=%s", args[0], shellQuote(strings.Join(args[1:], " ")))
			}
			lines = append(lines, fmt.Sprintf("export %s=", a))
		}
	}
	return strings.Join(lines, "\n")
}

// argToShell converts ARG NAME=default to shell variable with default.
func argToShell(args []string) string {
	if len(args) == 0 {
		return ""
	}
	var lines []string
	for _, a := range args {
		if k, v, ok := strings.Cut(a, "="); ok {
			lines = append(lines, fmt.Sprintf("%s=${%s:-%s}", k, k, v))
		} else {
			lines = append(lines, fmt.Sprintf(": ${%s?\"build argument %s is required\"}", a, a))
		}
	}
	return strings.Join(lines, "\n")
}

// addToShell converts ADD to curl for URL sources, or a comment for local sources.
func addToShell(args []string) string {
	if len(args) < 2 {
		return ""
	}
	src := args[0]
	// Check for --chown or --chmod flags and skip them.
	i := 0
	for i < len(args)-1 && strings.HasPrefix(args[i], "--") {
		i++
	}
	remaining := args[i:]
	if len(remaining) < 2 {
		return ""
	}
	src = remaining[0]
	dst := remaining[len(remaining)-1]

	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		return fmt.Sprintf("curl -fsSL %s -o %s", shellQuote(src), shellQuote(dst))
	}
	return fmt.Sprintf("# ADD %s (local source skipped, files come from sync)", src)
}

// ProvisionScript wraps a provisioning script with idempotency checks.
// If the script has already been run (same hash), it exits early.
func ProvisionScript(script string) string {
	hash := sha256Hash(script)
	return fmt.Sprintf(`#!/bin/bash
set -euo pipefail

MARKER_DIR="%s"
MARKER_FILE="%s"
EXPECTED_HASH="%s"

mkdir -p "$MARKER_DIR"

# Check if already provisioned with this exact configuration
if [ -f "$MARKER_FILE" ] && [ "$(cat "$MARKER_FILE")" = "$EXPECTED_HASH" ]; then
    echo "silo: environment already provisioned (hash match)"
    exit 0
fi

echo "silo: provisioning environment..."

%s

# Record successful provisioning
echo "$EXPECTED_HASH" > "$MARKER_FILE"
echo "silo: provisioning complete"
`, markerDir, markerFile, hash, script)
}

// sha256Hash returns the hex-encoded SHA-256 hash of s.
func sha256Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}
