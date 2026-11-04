package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/v0lka/c0wrk/backend/project"
)

// Atomically re-parents a CHAT session to a CODE project in one transaction:
// project_id → dstProjectID (archived cleared) plus pathRewrites applied to
// persisted message metadata; free message content is NOT rewritten. See
// specs/domains/session-lifecycle.md (§ Promotion).
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

	// Ownership guard; the project_id = ? predicate doubles as the
	// existence check.
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

// One persisted metadata blob whose prefix rewrites changed its bytes.
type metadataRewrite struct {
	id       int64
	metadata string
}

// Applies every old→new pair to one session's stored metadata blobs,
// returning only changed rows — scanned per session instead of via LIKE
// (prefixes may contain LIKE wildcards). The cursor closes before writes.
func (s *SQLiteSessionStore) scanMetadataRewrites(ctx context.Context, tx *sql.Tx, sessionID string, pathRewrites [][2]string) ([]metadataRewrite, error) {
	rewrites := make([][2]string, len(pathRewrites))
	copy(rewrites, pathRewrites)
	// Longest match first: the workspace pair is a suffix-extension of the
	// session-directory pair and must win where both apply.
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

// Returns s with every rewrite applied raw and JSON-escaped (the blobs are
// serialized JSON text); an occurrence is rewritten only when a path
// boundary follows, so a sibling name extending the prefix keeps its stale
// path.
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

// rawPathBoundaries may follow a raw-form occurrence.
var rawPathBoundaries = []string{`/`, `\`, `"`}

// escapedPathBoundaries may follow an escaped-form occurrence inside a JSON
// string value (doubled backslashes, closing quote).
var escapedPathBoundaries = []string{`/`, `\\`, `"`}

// Replaces every occurrence of old whose remainder starts with a boundary
// (or ends the string). A candidate lacking a boundary advances the scan by
// one byte so overlapping candidates are still examined.
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

func hasPrefixAny(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// Renders a path as encoding/json's default Marshal emits it in a string:
// backslashes/quotes escaped, &/</> as \uXXXX (legal Unix path characters);
// forward slashes stay raw. Exotic \uXXXX escapes stay out of scope — a
// missed rewrite degrades to a stale rendered path, never to corruption.
func jsonEscapeString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "&", `\u0026`)
	s = strings.ReplaceAll(s, "<", `\u003c`)
	s = strings.ReplaceAll(s, ">", `\u003e`)
	return s
}
