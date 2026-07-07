package builtins

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// FileReadParams controls a streaming line-range read from a file.
// All line numbers are 1-based.
type FileReadParams struct {
	Path           string // absolute or relative path to the file
	StartLine      int    // 1-based; <= 0 means 1
	EndLine        int    // 1-based; <= 0 means StartLine + DefaultLines - 1
	DefaultLines   int    // window size when EndLine is not specified
	MaxLineBytes   int    // per-line byte cap; 0 = no cap
	MaxWindowLines int    // hard cap on (EndLine - StartLine + 1); 0 = no cap
}

// FileReadResult holds the output of ReadFileRange.
type FileReadResult struct {
	Content      string // extracted window (lines joined with \n)
	TotalLines   int    // total lines in the file
	StartLine    int    // resolved start line (after defaults)
	EndLine      int    // resolved end line (after defaults + hard cap, before TotalLines clamping)
	WindowCapped bool   // true if MaxWindowLines truncated the requested range
	BytesRead    int    // byte length of Content
}

// ReadFileRange reads a line range from a file using O(1) memory — only the
// requested window is buffered. TotalLines is computed in the same pass by
// counting newlines; after the window, scanning switches to a fast byte-count
// mode that does not allocate per line.
//
// Line counting convention:
//   - "a\nb\nc\n" → 3 lines
//   - "a\nb\nc"   → 3 lines (last line without trailing \n is still counted)
//   - ""          → 0 lines
//   - "\n"        → 1 line (one empty line)
//
// The caller is responsible for clamping StartLine/EndLine to TotalLines for
// display purposes; ReadFileRange returns the resolved (but unclamped) values.
func ReadFileRange(params FileReadParams) (*FileReadResult, error) {
	startLine := params.StartLine
	if startLine <= 0 {
		startLine = 1
	}

	endLine := params.EndLine
	if endLine <= 0 {
		defaultLines := params.DefaultLines
		if defaultLines <= 0 {
			defaultLines = 1
		}
		endLine = startLine + defaultLines - 1
	}

	windowCapped := false
	if params.MaxWindowLines > 0 && endLine-startLine+1 > params.MaxWindowLines {
		endLine = startLine + params.MaxWindowLines - 1
		windowCapped = true
	}

	f, err := os.Open(params.Path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	reader := bufio.NewReaderSize(f, 64*1024)

	var builder strings.Builder
	lineNum := 0
	totalLines := 0
	pastWindow := false

	for {
		line, rerr := reader.ReadString('\n')

		if line != "" {
			lineNum++
			totalLines = lineNum

			if !pastWindow && lineNum >= startLine && lineNum <= endLine {
				writeLine(&builder, line, params.MaxLineBytes)
			}

			if lineNum > endLine {
				pastWindow = true
			}
		}

		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}

		if pastWindow {
			remaining, countErr := countRemainingNewlines(reader)
			if countErr != nil {
				return nil, countErr
			}
			totalLines += remaining
			break
		}
	}

	return &FileReadResult{
		Content:      builder.String(),
		TotalLines:   totalLines,
		StartLine:    startLine,
		EndLine:      endLine,
		WindowCapped: windowCapped,
		BytesRead:    builder.Len(),
	}, nil
}

// writeLine appends a line to the builder, applying an optional per-line byte
// cap. When the line exceeds MaxLineBytes, it is truncated and a marker is
// appended. The trailing newline (if present) is preserved after the marker.
func writeLine(builder *strings.Builder, line string, maxLineBytes int) {
	if maxLineBytes <= 0 || len(line) <= maxLineBytes {
		builder.WriteString(line)
		return
	}

	hasNewline := strings.HasSuffix(line, "\n")
	truncated := line[:maxLineBytes]
	truncated = strings.TrimSuffix(truncated, "\n")
	builder.WriteString(truncated)
	fmt.Fprintf(builder, "[...line truncated at %d bytes...]", maxLineBytes)
	if hasNewline {
		builder.WriteString("\n")
	}
}

// countRemainingNewlines counts lines in the remaining data from reader without
// allocating per line. Each \n counts as one line; if data remains after the
// last \n, it counts as one additional line (the trailing-no-newline case).
func countRemainingNewlines(r *bufio.Reader) (int, error) {
	count := 0
	sawNonNewline := false
	buf := make([]byte, 64*1024)
	for {
		n, err := r.Read(buf)
		for i := 0; i < n; i++ {
			if buf[i] == '\n' {
				count++
				sawNonNewline = false
			} else {
				sawNonNewline = true
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	if sawNonNewline {
		count++
	}
	return count, nil
}
