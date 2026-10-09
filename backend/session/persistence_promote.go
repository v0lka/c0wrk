package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/v0lka/c0wrk/backend/project"
)

// PromoteSessionToProject atomically re-parents a CHAT (No Project) session to
// a newly created CODE project within a single SQL transaction:
//
//  1. sessions.project_id is updated to dstProjectID and archived is cleared
//     (the promoted session becomes the fresh project's single live session).
//  2. Every persisted message metadata blob that embeds absolute paths under
//     the session's old on-disk locations has those prefixes rewritten to the
//     new ones via pathRewrites (old → new absolute prefix pairs).
//
// pathRewrites carries the two location moves performed by the file-level
// half of the promotion (Manager.MoveSessionStorage): the per-session
// directory (<agentDir>/projects/__no_project__/<sid> →
// <agentDir>/projects/<dstProjectID>/<sid>) and the workspace directory
// (<old session dir>/workspace → <agentDir>/projects/<dstProjectID>/Workspace).
// Free message content and tool_call payloads are deliberately NOT rewritten —
// only the machine-resolved structured references (image attachment paths and
// similar metadata fields) move with the files; historical prose keeps its
// original paths.
//
// Longer old prefixes are applied first so the more specific rewrite (the
// workspace pair) wins over the generic session-directory pair wherever both
// would match.
//
// The rewrite matches both the raw prefix and its JSON-string-escaped form
// (backslash and quote escaping plus the default HTML escapes — the shape
// stored metadata carries on Windows and around &, <, > path characters),
// because the blobs are serialized JSON text rather than raw paths. An
// occurrence is rewritten only when a path boundary follows it (separator,
// enclosing quote, end of blob): a sibling name that merely extends the
// prefix keeps its stale path instead of following the files to a location
// that does not exist. Occurrences hidden behind any other \uXXXX escape
// (e.g. U+2028/U+2029 line separators) are out of scope: a missed rewrite
// degrades to a stale rendered path, never to corruption.
//
// The source session must belong to the No Project pseudo-project: promotion
// is a CHAT→CODE transform, and the rewrite semantics are specific to the No
// Project per-session directory layout. Any other owner is refused so a
// future caller cannot silently re-parent a CODE session with CHAT-shaped
// rewrite expectations.
func (s *SQLiteSessionStore) PromoteSessionToProject(ctx context.Context, sessionID, dstProjectID string, pathRewrites [][2]string) error {
	if sessionID == "" {
		return errors.New("session id is required")
	}
	if dstProjectID == "" {
		return errors.New("destination project id is required")
	}
	if dstProjectID == project.NoProjectID {
		return errors.New("destination project cannot be the No Project pseudo-project")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin promotion transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Ownership guard: refuse anything that is not a CHAT session. The
	// project_id = ? predicate doubles as the existence check.
	res, err := tx.ExecContext(ctx, `
		UPDATE sessions
		SET project_id = ?, archived = 0
		WHERE id = ? AND project_id = ?`,
		dstProjectID, sessionID, project.NoProjectID,
	)
	if err != nil {
		return fmt.Errorf("failed to re-parent session: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("failed to read promotion result: %w", err)
	} else if n == 0 {
		return fmt.Errorf("session %q is not a No Project session (or does not exist)", sessionID)
	}

	if len(pathRewrites) > 0 {
		updates, err := s.scanMetadataRewrites(ctx, tx, sessionID, pathRewrites)
		if err != nil {
			return err
		}
		for _, u := range updates {
			if _, err := tx.ExecContext(ctx, `
				UPDATE session_messages SET metadata = ? WHERE id = ?`,
				u.metadata, u.id,
			); err != nil {
				return fmt.Errorf("failed to rewrite message metadata for promotion: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit promotion: %w", err)
	}
	return nil
}

// metadataRewrite is one persisted metadata blob whose prefix rewrites
// changed its bytes.
type metadataRewrite struct {
	id       int64
	metadata string
}

// scanMetadataRewrites applies every old→new prefix pair to the stored
// metadata blobs of one session and returns only the changed rows. Rows are
// scanned per session (a session's message count is naturally bounded)
// instead of via LIKE filtering: path prefixes may contain LIKE wildcards and
// the escaped/raw variant split makes a Go-side replacement both simpler and
// exact. The rows cursor is closed before any write runs.
func (s *SQLiteSessionStore) scanMetadataRewrites(ctx context.Context, tx *sql.Tx, sessionID string, pathRewrites [][2]string) ([]metadataRewrite, error) {
	rewrites := make([][2]string, len(pathRewrites))
	copy(rewrites, pathRewrites)
	// Longest match first: the workspace pair is a suffix-extension of the
	// session-directory pair, so it must win wherever both apply.
	for i := 1; i < len(rewrites); i++ {
		for j := i; j > 0 && len(rewrites[j][0]) > len(rewrites[j-1][0]); j-- {
			rewrites[j], rewrites[j-1] = rewrites[j-1], rewrites[j]
		}
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, metadata FROM session_messages
		WHERE session_id = ? AND metadata IS NOT NULL AND metadata != ''`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan message metadata for promotion: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var updates []metadataRewrite
	for rows.Next() {
		var id int64
		var metadata string
		if err := rows.Scan(&id, &metadata); err != nil {
			return nil, fmt.Errorf("failed to scan message metadata row: %w", err)
		}
		if rewritten := rewritePathPrefixes(metadata, rewrites); rewritten != metadata {
			updates = append(updates, metadataRewrite{id: id, metadata: rewritten})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate message metadata rows: %w", err)
	}
	return updates, nil
}

// rewritePathPrefixes returns s with every rewrite applied in both the raw
// and the JSON-string-escaped form. Escaped variants are derived per pair
// (backslashes first, then quotes, then the HTML escapes encoding/json
// applies by default) because the blobs are serialized JSON text produced by
// encoding/json rather than raw paths.
//
// An occurrence is rewritten only when a path boundary follows it: a path
// separator, the enclosing JSON string's closing quote, or the end of the
// blob. A longer sibling name that merely extends the prefix — a
// …/__no_project__/<sid>-backup directory mentioned in the metadata — keeps
// its stale path instead of being pointed at a location the promotion never
// moved it to. A prefix that carries its own trailing separator is
// self-bounded and always matches.
func rewritePathPrefixes(s string, rewrites [][2]string) string {
	for _, pair := range rewrites {
		oldRaw, newRaw := pair[0], pair[1]
		if oldRaw == "" || oldRaw == newRaw {
			continue
		}
		// Raw form: plain-text contexts of the blob.
		s = rewriteSegmentScoped(s, oldRaw, newRaw, rawPathBoundaries)
		oldEsc := jsonEscapeString(oldRaw)
		if oldEsc != oldRaw {
			// Escaped JSON-string form: forward slashes are stored raw,
			// backslashes doubled, and the value ends at a closing quote.
			newEsc := jsonEscapeString(newRaw)
			s = rewriteSegmentScoped(s, oldEsc, newEsc, escapedPathBoundaries)
		}
	}
	return s
}

// rawPathBoundaries may follow a raw-form prefix occurrence: path separators
// or a quote around a plainly quoted path.
var rawPathBoundaries = []string{`/`, `\`, `"`}

// escapedPathBoundaries may follow an escaped-form occurrence inside a JSON
// string value: raw forward separators, doubled backslashes, or the closing
// quote that ends the value.
var escapedPathBoundaries = []string{`/`, `\\`, `"`}

// rewriteSegmentScoped replaces every occurrence of old whose remainder in s
// starts with one of the boundaries (or ends the string) with its
// replacement. A candidate that lacks a boundary advances the scan by a
// single byte so overlapping candidates are still examined.
func rewriteSegmentScoped(s, old, replacement string, boundaries []string) string {
	if old == "" {
		return s
	}
	selfBounded := false
	for _, b := range boundaries {
		if strings.HasSuffix(old, b) {
			selfBounded = true
			break
		}
	}
	var out strings.Builder
	for {
		i := strings.Index(s, old)
		if i < 0 {
			break
		}
		out.WriteString(s[:i])
		rest := s[i+len(old):]
		if selfBounded || rest == "" || hasPrefixAny(rest, boundaries) {
			out.WriteString(replacement)
			s = rest
			continue
		}
		out.WriteByte(s[i])
		s = s[i+1:]
	}
	out.WriteString(s)
	return out.String()
}

// hasPrefixAny reports whether s starts with one of the prefixes.
func hasPrefixAny(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// jsonEscapeString renders a path the way it appears inside a JSON string
// value produced by encoding/json with its default Marshal settings:
// backslashes and quotes are backslash-escaped, and the HTML-significant
// characters (&, <, >) become their \uXXXX forms — all legal in Unix path
// components, so the escaped variant must mirror them. Forward slashes are
// left alone by Go's encoder, and no other path character is ever escaped;
// exotic escapes such as the U+2028/U+2029 line separators stay out of scope
// (a missed rewrite degrades to a stale rendered path, never to corruption).
func jsonEscapeString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "&", `\u0026`)
	s = strings.ReplaceAll(s, "<", `\u003c`)
	s = strings.ReplaceAll(s, ">", `\u003e`)
	return s
}
