package workspace

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/internal/sysproc"
)

// Git version probing ──────────────────────────────────────────────────────
//
// attr.tree — the blanket attribute-routing kill for include-bearing configs —
// exists only since git 2.45; older git silently ignores the pin and hidden
// drivers stay live, so the chokepoint fails closed unless git >= 2.45
// (specs/domains/workspace.md).

// attrTreeMinGitVersion is the oldest git that honors attr.tree.
var attrTreeMinGitVersion = gitVersion{major: 2, minor: 45}

// gitVersion is a parsed (major, minor) git version; the patch level is
// irrelevant to every gate consulted here and is deliberately dropped.
type gitVersion struct {
	major int
	minor int
}

// lessThan reports lexicographic (major, minor) ordering.
func (v gitVersion) lessThan(o gitVersion) bool {
	return v.major < o.major || (v.major == o.major && v.minor < o.minor)
}

// parseGitVersion extracts (major, minor) from `git --version` output —
// canonical or bare "2.45.0" spelling — refusing anything unparsable: fail
// closed, never guess.
func parseGitVersion(output string) (gitVersion, error) {
	s := strings.TrimSpace(output)
	if rest, ok := strings.CutPrefix(s, "git version "); ok {
		s = rest
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return gitVersion{}, fmt.Errorf("no version token in %q", output)
	}
	parts := strings.Split(fields[0], ".")
	if len(parts) < 2 {
		return gitVersion{}, fmt.Errorf("no major.minor pair in %q", output)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return gitVersion{}, fmt.Errorf("major in %q: %w", output, err)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return gitVersion{}, fmt.Errorf("minor in %q: %w", output, err)
	}
	return gitVersion{major: major, minor: minor}, nil
}

var (
	// gitVersionMu guards lazy resolution below — deliberately not a sync.Once:
	// a Once freezes the FIRST outcome, so a transient probe failure (git not
	// yet on PATH, an AV lock) would fail include-bearing repos for the process lifetime; the mutex retries, caching only completed resolutions.
	gitVersionMu sync.Mutex

	// resolvedGitVersion marks a parsed version: the verdict in
	// resolvedGitVersionErr is then final for the process lifetime; false on
	// probe/parse failure — retry.
	resolvedGitVersion bool

	resolvedGitVersionErr error
)

// gitVersionProbeTimeout bounds each `git --version` probe (normally
// milliseconds) so a hung binary fails the gate instead of blocking callers.
const gitVersionProbeTimeout = 5 * time.Second

// gitVersionOutputFn is the tests' seam for `git --version` output (mirroring
// scanGitConfigFn); production spawns through sysproc.GitCmd — no repo config
// is read, so no repo-scoped neutralization.
var gitVersionOutputFn = func(ctx context.Context) (string, error) {
	cmd, err := sysproc.GitCmd(ctx, "--version")
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("running git --version: %w", err)
	}
	return out.String(), nil
}

// requireAttrTreeCapableGit returns nil when git is known to honor attr.tree
// (>= 2.45) and fails closed otherwise, including an unresolvable version. A
// completed resolution is cached for the process lifetime; failures retry. Only invoked on the include-bearing path, where the pin is load-bearing.
func requireAttrTreeCapableGit() error {
	gitVersionMu.Lock()
	defer gitVersionMu.Unlock()

	if resolvedGitVersion {
		return resolvedGitVersionErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), gitVersionProbeTimeout)
	defer cancel()

	out, err := gitVersionOutputFn(ctx)
	if err != nil {
		// Not cached: probe failures are routinely transient.
		return fmt.Errorf(
			"cannot resolve the git version (fail closed: an include-bearing repository config needs the attr.tree neutralization, which requires git >= %d.%d): %w",
			attrTreeMinGitVersion.major, attrTreeMinGitVersion.minor, err)
	}
	v, parseErr := parseGitVersion(out)
	if parseErr != nil {
		// Not cached either: unparsable output is retried, not frozen.
		return fmt.Errorf(
			"cannot parse git version %q (fail closed: an include-bearing repository config needs the attr.tree neutralization, which requires git >= %d.%d): %w",
			strings.TrimSpace(out), attrTreeMinGitVersion.major, attrTreeMinGitVersion.minor, parseErr)
	}
	if v.lessThan(attrTreeMinGitVersion) {
		resolvedGitVersionErr = fmt.Errorf(
			"git %d.%d predates attr.tree support (fail closed: the repository config contains include directives whose hidden keys are only covered by the attr.tree neutralization, which requires git >= %d.%d); refusing to run git in this repository",
			v.major, v.minor, attrTreeMinGitVersion.major, attrTreeMinGitVersion.minor)
	}
	// Resolution succeeded: freeze the verdict (nil when capable) for the
	// process lifetime.
	resolvedGitVersion = true
	return resolvedGitVersionErr
}
