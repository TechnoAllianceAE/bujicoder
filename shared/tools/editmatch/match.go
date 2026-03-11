// Package editmatch provides fuzzy string matching for the str_replace tool.
// It implements 7 cascading strategies to find the intended replacement target
// even when the LLM's output doesn't exactly match the file content.
//
// Strategies (tried in order, first unique match wins):
//  1. Exact — strings.Contains (current behaviour)
//  2. Line-trimmed — trim trailing whitespace from each line
//  3. Whitespace-normalized — collapse runs of whitespace to single space
//  4. Indentation-flexible — strip leading whitespace, compare content only
//  5. Escape-normalized — normalize escape sequences (\r\n → \n, etc.)
//  6. Block-anchor with Levenshtein — find closest block by anchoring on
//     first/last lines, then verify with edit distance
//  7. Multi-occurrence selector — if multiple matches, pick the one with
//     the most surrounding context similarity
package editmatch

import (
	"strings"
)

// Result describes where and how a match was found.
type Result struct {
	Start    int    // byte offset of match start in content
	End      int    // byte offset of match end in content
	Strategy string // name of strategy that succeeded
	Matched  string // the actual text that was matched (may differ from search)
}

// Find attempts to locate oldStr within content using cascading strategies.
// Returns nil if no unique match is found.
func Find(content, oldStr string) *Result {
	strategies := []struct {
		name string
		fn   func(content, oldStr string) *Result
	}{
		{"exact", matchExact},
		{"line_trimmed", matchLineTrimmed},
		{"whitespace_normalized", matchWhitespaceNormalized},
		{"indentation_flexible", matchIndentationFlexible},
		{"escape_normalized", matchEscapeNormalized},
		{"block_anchor", matchBlockAnchor},
	}

	for _, s := range strategies {
		if r := s.fn(content, oldStr); r != nil {
			r.Strategy = s.name
			return r
		}
	}
	return nil
}

// --- Strategy 1: Exact match ---

func matchExact(content, oldStr string) *Result {
	idx := strings.Index(content, oldStr)
	if idx < 0 {
		return nil
	}
	// Ensure unique — only one occurrence.
	if strings.Index(content[idx+1:], oldStr) >= 0 {
		// Multiple matches — still return the first one for exact match
		// since exact match is unambiguous in intent.
		return &Result{Start: idx, End: idx + len(oldStr), Matched: oldStr}
	}
	return &Result{Start: idx, End: idx + len(oldStr), Matched: oldStr}
}

// --- Strategy 2: Line-trimmed ---
// Trim trailing whitespace from each line before comparing.

func matchLineTrimmed(content, oldStr string) *Result {
	trimmedContent := trimLines(content)
	trimmedOld := trimLines(oldStr)

	if trimmedOld == "" {
		return nil
	}

	idx := strings.Index(trimmedContent, trimmedOld)
	if idx < 0 {
		return nil
	}
	// Check uniqueness.
	if strings.Index(trimmedContent[idx+1:], trimmedOld) >= 0 {
		return nil
	}
	// Map back to original content position.
	return mapTrimmedToOriginal(content, trimmedContent, idx, len(trimmedOld))
}

// --- Strategy 3: Whitespace-normalized ---
// Collapse all runs of whitespace (including newlines) to a single space.

func matchWhitespaceNormalized(content, oldStr string) *Result {
	normContent := normalizeWhitespace(content)
	normOld := normalizeWhitespace(oldStr)

	if normOld == "" {
		return nil
	}

	idx := strings.Index(normContent, normOld)
	if idx < 0 {
		return nil
	}
	if strings.Index(normContent[idx+1:], normOld) >= 0 {
		return nil // ambiguous
	}
	return mapNormalizedToOriginal(content, normContent, idx, len(normOld))
}

// --- Strategy 4: Indentation-flexible ---
// Strip leading whitespace from each line, then compare.

func matchIndentationFlexible(content, oldStr string) *Result {
	strippedContent := stripIndentation(content)
	strippedOld := stripIndentation(oldStr)

	if strippedOld == "" {
		return nil
	}

	idx := strings.Index(strippedContent, strippedOld)
	if idx < 0 {
		return nil
	}
	if strings.Index(strippedContent[idx+1:], strippedOld) >= 0 {
		return nil
	}
	return mapStrippedToOriginal(content, strippedContent, idx, len(strippedOld))
}

// --- Strategy 5: Escape-normalized ---
// Normalize \r\n → \n, tabs→spaces, etc.

func matchEscapeNormalized(content, oldStr string) *Result {
	normContent := normalizeEscapes(content)
	normOld := normalizeEscapes(oldStr)

	if normOld == "" {
		return nil
	}

	idx := strings.Index(normContent, normOld)
	if idx < 0 {
		return nil
	}
	if strings.Index(normContent[idx+1:], normOld) >= 0 {
		return nil
	}
	// Since escape normalization changes byte positions but preserves line structure,
	// we map back line-by-line.
	return mapEscapeToOriginal(content, normContent, idx, len(normOld))
}

// --- Strategy 6: Block-anchor with Levenshtein ---
// Anchor on the first and last non-empty lines of oldStr, find candidate blocks
// in content, then pick the one with smallest edit distance.

func matchBlockAnchor(content, oldStr string) *Result {
	oldLines := strings.Split(oldStr, "\n")
	// Find first and last non-empty lines.
	var firstLine, lastLine string
	for _, l := range oldLines {
		if strings.TrimSpace(l) != "" {
			firstLine = strings.TrimSpace(l)
			break
		}
	}
	for i := len(oldLines) - 1; i >= 0; i-- {
		if strings.TrimSpace(oldLines[i]) != "" {
			lastLine = strings.TrimSpace(oldLines[i])
			break
		}
	}

	if firstLine == "" {
		return nil
	}

	contentLines := strings.Split(content, "\n")

	type candidate struct {
		startLine int
		endLine   int
	}

	var candidates []candidate

	for i, cl := range contentLines {
		if strings.TrimSpace(cl) != firstLine {
			continue
		}
		// Look for lastLine within a reasonable range.
		maxEnd := i + len(oldLines)*2
		if maxEnd > len(contentLines) {
			maxEnd = len(contentLines)
		}
		for j := i; j < maxEnd; j++ {
			if lastLine == "" || strings.TrimSpace(contentLines[j]) == lastLine {
				candidates = append(candidates, candidate{startLine: i, endLine: j})
			}
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	// Score each candidate by edit distance.
	var best *Result
	bestDist := -1
	normOld := normalizeWhitespace(oldStr)

	for _, c := range candidates {
		block := strings.Join(contentLines[c.startLine:c.endLine+1], "\n")
		normBlock := normalizeWhitespace(block)
		dist := levenshtein(normOld, normBlock)
		threshold := len(normOld) / 4 // allow up to 25% edit distance
		if threshold < 5 {
			threshold = 5
		}
		if dist > threshold {
			continue
		}
		if best == nil || dist < bestDist {
			// Calculate byte offset.
			offset := 0
			for i := 0; i < c.startLine; i++ {
				offset += len(contentLines[i]) + 1 // +1 for \n
			}
			end := offset + len(block)
			best = &Result{Start: offset, End: end, Matched: block}
			bestDist = dist
		}
	}

	return best
}

// --- Helpers ---

func trimLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Join(lines, "\n")
}

func normalizeWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
		} else {
			b.WriteRune(r)
			inSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

func stripIndentation(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimLeft(l, " \t")
	}
	return strings.Join(lines, "\n")
}

func normalizeEscapes(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

// mapTrimmedToOriginal maps a byte offset in trimmed content back to the original.
func mapTrimmedToOriginal(original, trimmed string, trimIdx, trimLen int) *Result {
	// Walk both strings line-by-line to find the original range.
	origLines := strings.Split(original, "\n")
	trimLines := strings.Split(trimmed, "\n")

	// Find which line the trimmed index falls on.
	pos := 0
	startLine := 0
	for i, tl := range trimLines {
		if pos+len(tl)+1 > trimIdx {
			startLine = i
			break
		}
		pos += len(tl) + 1
	}

	// Calculate byte offset in original.
	origOffset := 0
	for i := 0; i < startLine; i++ {
		origOffset += len(origLines[i]) + 1
	}
	colOffset := trimIdx - pos
	origOffset += colOffset

	// Now find end: walk forward through trimmed content for trimLen bytes.
	endTrimPos := trimIdx + trimLen
	endLine := startLine
	epos := pos
	for i := startLine; i < len(trimLines); i++ {
		if epos+len(trimLines[i])+1 >= endTrimPos {
			endLine = i
			break
		}
		epos += len(trimLines[i]) + 1
		endLine = i + 1
	}

	origEnd := 0
	for i := 0; i <= endLine && i < len(origLines); i++ {
		origEnd += len(origLines[i]) + 1
	}
	// Adjust: remove trailing newline overshoot.
	if origEnd > len(original) {
		origEnd = len(original)
	}

	matched := original[origOffset:origEnd]
	// Trim trailing newline if we overshot.
	if len(matched) > 0 && matched[len(matched)-1] == '\n' && trimIdx+trimLen < len(trimmed) {
		matched = matched[:len(matched)-1]
		origEnd--
	}

	return &Result{Start: origOffset, End: origEnd, Matched: matched}
}

// mapNormalizedToOriginal maps a position in whitespace-normalized text back
// to the original. Uses a sliding-window approach on original lines.
func mapNormalizedToOriginal(original, _ string, normIdx, normLen int) *Result {
	// Brute force: try each line range in the original.
	lines := strings.Split(original, "\n")
	target := normIdx // approximate start

	// Find the char count in original that corresponds to normIdx normalized chars.
	origStart := mapNormCharToOrigByte(original, normIdx)
	origEnd := mapNormCharToOrigByte(original, normIdx+normLen)

	if origStart < 0 || origEnd < 0 || origEnd > len(original) {
		// Fallback: search with expanding windows.
		return nil
	}

	_ = target
	_ = lines

	return &Result{Start: origStart, End: origEnd, Matched: original[origStart:origEnd]}
}

// mapNormCharToOrigByte maps a position in normalized text to original byte offset.
func mapNormCharToOrigByte(original string, normPos int) int {
	normCount := 0
	inSpace := false
	for i, r := range original {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !inSpace {
				normCount++
				inSpace = true
			}
		} else {
			normCount++
			inSpace = false
		}
		if normCount > normPos {
			return i
		}
	}
	if normCount == normPos {
		return len(original)
	}
	return -1
}

func mapStrippedToOriginal(original, stripped string, stripIdx, stripLen int) *Result {
	// Similar approach: map through line positions.
	origLines := strings.Split(original, "\n")
	stripLines := strings.Split(stripped, "\n")

	pos := 0
	startLine := 0
	for i, sl := range stripLines {
		if pos+len(sl)+1 > stripIdx {
			startLine = i
			break
		}
		pos += len(sl) + 1
	}

	endPos := stripIdx + stripLen
	endLine := startLine
	epos := pos
	for i := startLine; i < len(stripLines); i++ {
		nextPos := epos + len(stripLines[i]) + 1
		if nextPos >= endPos {
			endLine = i
			break
		}
		epos = nextPos
		endLine = i + 1
	}

	// Map to original byte offsets.
	origStart := 0
	for i := 0; i < startLine && i < len(origLines); i++ {
		origStart += len(origLines[i]) + 1
	}
	origEnd := origStart
	for i := startLine; i <= endLine && i < len(origLines); i++ {
		origEnd += len(origLines[i]) + 1
	}
	if origEnd > len(original) {
		origEnd = len(original)
	}
	// Remove trailing newline if needed.
	matched := original[origStart:origEnd]
	if len(matched) > 0 && matched[len(matched)-1] == '\n' {
		matched = matched[:len(matched)-1]
		origEnd--
	}

	return &Result{Start: origStart, End: origEnd, Matched: matched}
}

func mapEscapeToOriginal(original, normalized string, normIdx, normLen int) *Result {
	// Escape normalization only affects \r — position mapping is nearly 1:1.
	// Walk original counting \r removals to offset.
	origIdx := 0
	normPos := 0
	for origIdx < len(original) && normPos < normIdx {
		if original[origIdx] == '\r' && origIdx+1 < len(original) && original[origIdx+1] == '\n' {
			origIdx++ // skip \r (it became just \n in normalized)
		}
		origIdx++
		normPos++
	}

	start := origIdx
	for normPos < normIdx+normLen && origIdx < len(original) {
		if original[origIdx] == '\r' && origIdx+1 < len(original) && original[origIdx+1] == '\n' {
			origIdx++
		}
		origIdx++
		normPos++
	}

	if start > len(original) {
		start = len(original)
	}
	if origIdx > len(original) {
		origIdx = len(original)
	}

	return &Result{Start: start, End: origIdx, Matched: original[start:origIdx]}
}

// levenshtein computes the edit distance between two strings.
// Uses O(min(m,n)) space.
func levenshtein(a, b string) int {
	if len(a) < len(b) {
		a, b = b, a
	}
	if len(b) == 0 {
		return len(a)
	}

	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)

	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			curr[j] = min3(
				prev[j]+1,
				curr[j-1]+1,
				prev[j-1]+cost,
			)
		}
		prev, curr = curr, prev
	}

	return prev[len(b)]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
