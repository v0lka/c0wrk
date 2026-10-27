package backend

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/workspace"
	"gopkg.in/yaml.v3"
)

// commandFileExt is the only file extension the command discovery recognizes.
const commandFileExt = ".md"

// commandFrontmatter is the YAML frontmatter schema of a command template
// file; unknown keys are ignored, every field is optional.
// See specs/domains/commands.md (ADR-096).
type commandFrontmatter struct {
	Description string `yaml:"description"`
	Agent       string `yaml:"agent"`
	Model       string `yaml:"model"`
}

// ListCommands returns the custom command descriptors for /-autocomplete and
// send-time template expansion (ADR-096): project-local .agents/commands
// first, then the configured global dirs; first name wins. Cache-served;
// invalidated on project switches and by the global-dir watchers. Discovery
// is backend-owned (core never consumes commands). See specs/domains/commands.md.
func (f *FrontendAPI) ListCommands() []CommandDescriptorDTO {
	f.activeProjectMu.RLock()
	wsPath := f.activeProjectPath
	f.activeProjectMu.RUnlock()

	projectCommandDir := ""
	if wsPath != "" {
		projectCommandDir = config.ProjectCommandsPath(wsPath)
	}

	gen := atomic.LoadUint64(&f.commandCacheGen)
	f.commandCacheMu.Lock()
	if f.commandCache != nil && f.commandCacheGenSnapshot == gen && f.commandCacheProjectDir == projectCommandDir {
		cached := f.commandCache
		f.commandCacheMu.Unlock()
		return cached
	}
	f.commandCacheMu.Unlock()

	result := f.scanCommands(projectCommandDir, f.commandBaseDirs())

	f.commandCacheMu.Lock()
	f.commandCache = result
	f.commandCacheGenSnapshot = gen
	f.commandCacheProjectDir = projectCommandDir
	f.commandCacheMu.Unlock()

	return result
}

// setCommandBaseDirs stores the resolved global command directories, once at
// startup with the same resolved list the watchers get: fixed until restart
// (the accepted skills/agents trade-off).
func (f *FrontendAPI) setCommandBaseDirs(dirs []string) {
	f.commandWatchersMu.Lock()
	defer f.commandWatchersMu.Unlock()
	f.commandDirs = append([]string(nil), dirs...)
}

func (f *FrontendAPI) commandBaseDirs() []string {
	f.commandWatchersMu.Lock()
	defer f.commandWatchersMu.Unlock()
	return f.commandDirs
}

func (f *FrontendAPI) invalidateCommandCache() {
	atomic.AddUint64(&f.commandCacheGen, 1)
}

// startCommandsWatchers watches each existing global command directory
// (debounced): cache invalidation plus a commands:changed event. Missing
// directories are skipped until an app restart — the accepted trade-off.
// Commands are flat .md files, so unlike skills no subdirectory watches.
func (f *FrontendAPI) startCommandsWatchers(dirs []string) {
	f.seedAcquire()
	f.commandWatchersMu.Lock()
	defer f.commandWatchersMu.Unlock()

	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		watcher, err := workspace.NewWatcher(dir, func(_ []string) {
			f.invalidateCommandCache()
			f.emitEvent(EventCommandsChanged, nil)
		}, f.log())
		if err != nil {
			f.log().Warn("failed to watch command directory", "dir", dir, "error", err)
			continue
		}
		f.commandWatchers = append(f.commandWatchers, watcher)
	}
}

func (f *FrontendAPI) closeCommandsWatchers() {
	f.commandWatchersMu.Lock()
	defer f.commandWatchersMu.Unlock()
	for _, w := range f.commandWatchers {
		_ = w.Close()
	}
	f.commandWatchers = nil
}

// scanCommands walks the precedence-ordered dir list (project-local first,
// then global dirs in config order); the first file claiming a name wins. The
// result is always non-nil so the cache can tell "scanned, empty" from
// "never scanned".
func (f *FrontendAPI) scanCommands(projectDir string, baseDirs []string) []CommandDescriptorDTO {
	dirs := make([]string, 0, len(baseDirs)+1)
	if projectDir != "" {
		dirs = append(dirs, projectDir)
	}
	dirs = append(dirs, baseDirs...)

	result := make([]CommandDescriptorDTO, 0)
	seen := make(map[string]struct{})
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			// A missing directory is normal (nothing created yet); anything
			// else is worth a warning but never fatal.
			if !os.IsNotExist(err) {
				f.log().Warn("command directory unreadable", "dir", dir, "error", err)
			}
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != commandFileExt {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), commandFileExt)
			if !validCommandName(name) {
				f.log().Debug("skipping command file with uninvocable name", "path", filepath.Join(dir, entry.Name()))
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			desc, ok := parseCommandFile(filepath.Join(dir, entry.Name()), name, f.log())
			if !ok {
				continue
			}
			seen[name] = struct{}{}
			result = append(result, desc)
		}
	}
	return result
}

// parseCommandFile reads one command template file: optional frontmatter
// supplies description/agent/model, the rest is the template. A read failure
// drops the file; an unparsable frontmatter is fail-soft (warning, empty
// metadata, body still ships). See specs/domains/commands.md.
func parseCommandFile(path, name string, log *slog.Logger) (CommandDescriptorDTO, bool) {
	content, err := os.ReadFile(path)
	if err != nil {
		return CommandDescriptorDTO{}, false
	}
	var meta commandFrontmatter
	text := string(content)
	body := text
	if fm, rest, has := splitFrontmatter(text); has {
		if err := yaml.Unmarshal([]byte(fm), &meta); err != nil {
			if log != nil {
				log.Warn("command frontmatter unparsable; metadata ignored", "path", path, "error", err)
			}
			meta = commandFrontmatter{}
		}
		body = rest
	}
	// Trim the leading newlines the frontmatter delimiter leaves (LF and
	// CRLF) and the trailing whitespace; the first line's indentation
	// survives.
	body = strings.TrimLeft(body, "\r\n")
	body = strings.TrimRight(body, " \t\n\r")
	return CommandDescriptorDTO{
		Name:        name,
		Description: meta.Description,
		Agent:       meta.Agent,
		Model:       meta.Model,
		Template:    body,
	}, true
}

// splitFrontmatter splits a leading YAML frontmatter block off content (the
// first line exactly "---", closed by a second exactly-"---" line) and
// returns the inner YAML plus the remainder. Missing or unterminated →
// has=false, full content as the body.
func splitFrontmatter(content string) (fm, body string, has bool) {
	first, rest, found := cutLine(content)
	if !found || strings.TrimRight(first, "\r") != "---" {
		return "", content, false
	}
	var lines []string
	for {
		line, restAfter, found := cutLine(rest)
		if !found {
			return "", content, false
		}
		if strings.TrimRight(line, "\r") == "---" {
			return strings.Join(lines, "\n"), restAfter, true
		}
		lines = append(lines, strings.TrimRight(line, "\r"))
		rest = restAfter
	}
}

// cutLine splits off the first line of s including its terminating newline
// (when present). found is false only for an empty remainder.
func cutLine(s string) (line, rest string, found bool) {
	if s == "" {
		return "", "", false
	}
	idx := strings.IndexByte(s, '\n')
	if idx < 0 {
		return s, "", true
	}
	return s[:idx], s[idx+1:], true
}

// validCommandName reports whether name is invocable through the chat input's
// /-grammar: the mention scanner's character class (ASCII letters, digits,
// underscore, hyphen). Anything else cannot be typed as a ref and is skipped
// at discovery. See specs/domains/commands.md.
func validCommandName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}
