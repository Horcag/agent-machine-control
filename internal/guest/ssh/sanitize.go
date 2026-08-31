package ssh

import (
	"bytes"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

var (
	// Sensitive text patterns for conservative redaction.
	privateKeyPattern = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]+ PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]+ PRIVATE KEY-----`)
	bearerPattern     = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9\-\._~\+\/]+=*`)
	passwordPattern   = regexp.MustCompile(`(?i)(password\s*[:=]\s*)([^\s\r\n]+)`)
	tokenPattern      = regexp.MustCompile(`(?i)(token\s*[:=]\s*)([^\s\r\n]+)`)
)

const (
	maxSanitizerPendingBytes = 64 * 1024
	sanitizerOverflowMarker  = "[REDACTED TRUNCATED]"
)

type sanitizerDropMode uint8

const (
	dropNone sanitizerDropMode = iota
	dropUntilLine
	dropUntilFlush
)

// RedactionPattern is a configured bounded regex matcher. MaxMatchBytes is the
// explicit stream-boundary width contract for the expression.
type RedactionPattern struct {
	Pattern       *regexp.Regexp
	MaxMatchBytes int
}

// SanitizerConfig contains only server-owned redaction inputs.
type SanitizerConfig struct {
	ExactSecrets [][]byte
	Patterns     []RedactionPattern
}

// StreamSanitizer preserves redaction state across arbitrary transport chunks.
type StreamSanitizer struct {
	mu       sync.Mutex
	pending  []byte
	secrets  [][]byte
	patterns []RedactionPattern
	dropping sanitizerDropMode
	terminal terminalFilter
}

// NewStreamSanitizer creates an isolated stateful sanitizer. Secret bytes are copied.
func NewStreamSanitizer(cfg SanitizerConfig) *StreamSanitizer {
	s := &StreamSanitizer{patterns: append([]RedactionPattern(nil), cfg.Patterns...)}
	for _, secret := range cfg.ExactSecrets {
		if len(secret) == 0 {
			continue
		}
		s.secrets = append(s.secrets, append([]byte(nil), secret...))
	}
	return s
}

// Push sanitizes one transport chunk while retaining only undecidable suffix bytes.
func (s *StreamSanitizer) Push(raw []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(raw) == 0 {
		return ""
	}
	if s.dropping != dropNone {
		rest := s.consumeDropped(raw)
		if len(rest) == 0 {
			return ""
		}
		raw = rest
	}
	filtered := s.terminal.Push(raw)
	if filtered == "" {
		return ""
	}
	data := append(append([]byte(nil), s.pending...), filtered...)
	s.pending = nil
	text := string(data)
	cut := s.safeCut(text)
	s.pending = append(s.pending, []byte(text[cut:])...)
	output := s.redactComplete(text[:cut])
	if len(s.pending) > maxSanitizerPendingBytes {
		s.dropping = dropModeForTail(text[cut:])
		s.pending = nil
		output += sanitizerOverflowMarker
	}
	return output
}

// RetainedBytes reports the bounded undecidable state held across chunks.
func (s *StreamSanitizer) RetainedBytes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending) + s.terminal.RetainedBytes()
}

// Flush emits sanitized terminal suffix data and clears all secret-bearing state.
func (s *StreamSanitizer) Flush() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	text := string(s.pending) + s.terminal.Flush()
	s.pending = nil
	s.dropping = dropNone
	text = redactIncompleteStructures(text)
	result := s.redactComplete(text)
	s.zeroSecrets()
	return result
}

func dropModeForTail(tail string) sanitizerDropMode {
	lower := strings.ToLower(tail)
	if strings.HasPrefix(lower, "-----begin ") && strings.Contains(lower, "private key-----") {
		return dropUntilFlush
	}
	return dropUntilLine
}

func (s *StreamSanitizer) consumeDropped(raw []byte) []byte {
	switch s.dropping {
	case dropUntilLine:
		if i := bytes.IndexAny(raw, "\r\n"); i >= 0 {
			s.dropping = dropNone
			return raw[i+1:]
		}
	case dropUntilFlush:
		return nil
	}
	return nil
}

func (s *StreamSanitizer) zeroSecrets() {
	for _, secret := range s.secrets {
		for i := range secret {
			secret[i] = 0
		}
	}
	s.secrets = nil
}

func (s *StreamSanitizer) safeCut(text string) int {
	cut := len(text)
	lower := strings.ToLower(text)
	cut = minCut(cut, incompleteStructureStart(text, lower))
	cut = cutForLiteralPrefixes(text, cut)
	cut = s.cutForSecretPrefixes(text, cut)
	cut = s.cutForConfiguredTail(text, cut)
	cut = s.cutForExactSecretMatches(text, cut)
	cut = cutForPatternMatches(text, cut, []*regexp.Regexp{privateKeyPattern, bearerPattern, passwordPattern, tokenPattern})
	for _, configured := range s.patterns {
		cut = cutForPatternMatches(text, cut, []*regexp.Regexp{configured.Pattern})
	}
	return cut
}

func cutForLiteralPrefixes(text string, cut int) int {
	for _, prefix := range []string{"-----begin", "bearer ", "password=", "password:", "token=", "token:"} {
		for n := 1; n < len(prefix) && n <= len(text); n++ {
			if strings.EqualFold(text[len(text)-n:], prefix[:n]) {
				cut = minCut(cut, len(text)-n)
			}
		}
	}
	return cut
}

func (s *StreamSanitizer) cutForSecretPrefixes(text string, cut int) int {
	for _, secret := range s.secrets {
		maxPrefix := min(min(len(secret)-1, len(text)), maxSanitizerPendingBytes+1)
		for n := 1; n <= maxPrefix; n++ {
			if bytes.Equal([]byte(text[len(text)-n:]), secret[:n]) {
				cut = minCut(cut, len(text)-n)
			}
		}
	}
	return cut
}

func (s *StreamSanitizer) cutForConfiguredTail(text string, cut int) int {
	for _, configured := range s.patterns {
		if configured.Pattern == nil || configured.MaxMatchBytes <= 1 {
			continue
		}
		hold := min(min(configured.MaxMatchBytes-1, maxSanitizerPendingBytes+1), len(text))
		cut = minCut(cut, len(text)-hold)
	}
	return cut
}

func (s *StreamSanitizer) cutForExactSecretMatches(text string, cut int) int {
	for _, secret := range s.secrets {
		for start := 0; start < len(text); {
			index := strings.Index(text[start:], string(secret))
			if index < 0 {
				break
			}
			matchStart := start + index
			matchEnd := matchStart + len(secret)
			if matchStart < cut && matchEnd > cut {
				cut = matchStart
			}
			start = matchEnd
		}
	}
	return cut
}

func cutForPatternMatches(text string, cut int, patterns []*regexp.Regexp) int {
	for _, pattern := range patterns {
		if pattern == nil {
			continue
		}
		for _, match := range pattern.FindAllStringIndex(text, -1) {
			if match[0] < cut && match[1] > cut {
				cut = match[0]
			}
		}
	}
	return cut
}

func minCut(current, candidate int) int {
	if candidate >= 0 && candidate < current {
		return candidate
	}
	return current
}

func incompleteStructureStart(text, lower string) int {
	if start := strings.LastIndex(lower, "-----begin "); start >= 0 {
		tail := lower[start:]
		headerRemainder := tail[len("-----begin "):]
		if !strings.Contains(headerRemainder, "-----") {
			return start
		}
		if strings.Contains(tail, "private key-----") && privateKeyPattern.FindStringIndex(text[start:]) == nil {
			return start
		}
	}
	for _, word := range []string{"bearer", "password", "token"} {
		if start := strings.LastIndex(lower, word); start >= 0 {
			tail := text[start+len(word):]
			if !strings.ContainsAny(tail, "\r\n") && sensitiveAssignmentPrefix(word, tail) {
				return start
			}
		}
	}
	return -1
}

func sensitiveAssignmentPrefix(word, tail string) bool {
	trimmed := strings.TrimLeft(tail, " \t")
	if word == "bearer" {
		return len(tail) != len(trimmed) && !strings.ContainsAny(trimmed, " \t")
	}
	if trimmed == "" {
		return true
	}
	if trimmed[0] != ':' && trimmed[0] != '=' {
		return false
	}
	value := strings.TrimLeft(trimmed[1:], " \t")
	return !strings.ContainsAny(value, " \t")
}

func (s *StreamSanitizer) redactComplete(text string) string {
	text = RedactSensitiveText(text)
	for _, secret := range s.secrets {
		text = strings.ReplaceAll(text, string(secret), "[REDACTED]")
	}
	for _, configured := range s.patterns {
		if configured.Pattern != nil {
			text = configured.Pattern.ReplaceAllString(text, "[REDACTED]")
		}
	}
	return text
}

func redactIncompleteStructures(text string) string {
	lower := strings.ToLower(text)
	if start := strings.LastIndex(lower, "-----begin "); start >= 0 && strings.Contains(lower[start:], "private key-----") && !strings.Contains(lower[start:], "-----end ") {
		text = text[:start] + "[REDACTED PRIVATE KEY]"
	}
	return text
}

// SanitizeTerminalOutput filters dangerous escape sequences, resolves UTF-8 boundaries,
// and redacts known credential patterns.
func SanitizeTerminalOutput(raw []byte, pendingBuf *[]byte) string {
	if len(raw) == 0 && (pendingBuf == nil || len(*pendingBuf) == 0) {
		return ""
	}

	var data []byte
	if pendingBuf != nil && len(*pendingBuf) > 0 {
		data = append(*pendingBuf, raw...)
		*pendingBuf = nil
	} else {
		data = raw
	}

	// 1. Resolve trailing incomplete UTF-8 sequence
	data, incomplete := splitIncompleteUTF8(data)
	if pendingBuf != nil && len(incomplete) > 0 {
		*pendingBuf = incomplete
	}

	// 2. Parse raw bytes before UTF-8 normalization so 8-bit C1 controls cannot
	// be converted to printable replacement characters ahead of the parser.
	return RedactSensitiveText(filterTerminalOutput(data))
}

func splitIncompleteUTF8(data []byte) ([]byte, []byte) {
	if len(data) == 0 {
		return data, nil
	}

	// Check up to 3 trailing bytes for incomplete multibyte UTF-8 prefix
	n := len(data)
	for i := 1; i <= 3 && i <= n; i++ {
		b := data[n-i]
		if utf8.RuneStart(b) {
			if !utf8.FullRune(data[n-i:]) {
				return data[:n-i], data[n-i:]
			}
			break
		}
	}
	return data, nil
}

// RedactSensitiveText redacts known private key blocks, bearer tokens, and credentials.
func RedactSensitiveText(text string) string {
	if text == "" {
		return ""
	}

	// 1. Redact PEM Private Key Blocks
	text = privateKeyPattern.ReplaceAllString(text, "[REDACTED PRIVATE KEY]")

	// 2. Redact Bearer Tokens
	text = bearerPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := bearerPattern.FindStringSubmatch(match)
		if len(parts) >= 2 {
			return parts[1] + "[REDACTED]"
		}
		return match
	})

	// 3. Redact Password Assignments
	text = passwordPattern.ReplaceAllString(text, "${1}[REDACTED]")

	// 4. Redact Token Assignments
	text = tokenPattern.ReplaceAllString(text, "${1}[REDACTED]")

	return text
}

// StripANSI removes all ANSI escape sequences for pure plain-text extraction.
func StripANSI(text string) string {
	return filterTerminalOutput([]byte(text))
}
