// Package token replaces delimited keys such as <::GROUP::> in byte input.
//
// A token is the open delimiter, a key matching [A-Za-z_][A-Za-z0-9_]*
// (at most MaxKeyLen bytes), and the close delimiter, with nothing in
// between. Input is scanned once; replacement values are copied verbatim and
// never scanned again, so a value may itself contain delimiters. Any open
// delimiter that does not begin a well-formed token is reported as malformed
// rather than passed through, because with a dedicated delimiter it is almost
// always a typo.
package token

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// MaxKeyLen bounds key length so diagnostics stay small whatever the input.
const MaxKeyLen = 128

// MaxProblems is the most problems reported for one input. Scanning stops
// once it is reached and Result.Truncated is set.
const MaxProblems = 100

// Syntax is the delimiter pair around a key.
type Syntax struct {
	Open  string
	Close string
}

// DefaultSyntax is <::KEY::>.
var DefaultSyntax = Syntax{Open: "<::", Close: "::>"}

// Validate reports whether the delimiters bound a key unambiguously. Keys are
// read greedily, so a close delimiter starting with a key character would be
// absorbed into the key, and an open delimiter ending with one would make the
// key's start ambiguous.
func (s Syntax) Validate() error {
	if s.Open == "" || s.Close == "" {
		return errors.New("delimiters must not be empty")
	}
	if strings.ContainsFunc(s.Open+s.Close, unicode.IsSpace) {
		return fmt.Errorf("delimiters %q and %q must not contain whitespace", s.Open, s.Close)
	}
	if isKeyByte(s.Open[len(s.Open)-1]) {
		return fmt.Errorf("open delimiter %q must not end with a letter, digit or underscore", s.Open)
	}
	if isKeyByte(s.Close[0]) {
		return fmt.Errorf("close delimiter %q must not start with a letter, digit or underscore", s.Close)
	}
	return nil
}

// ValidKey reports whether k can appear in a token.
func ValidKey(k string) bool {
	if len(k) == 0 || len(k) > MaxKeyLen || isDigit(k[0]) {
		return false
	}
	for i := 0; i < len(k); i++ {
		if !isKeyByte(k[i]) {
			return false
		}
	}
	return true
}

// Kind classifies a Problem.
type Kind int

const (
	// Missing is a well-formed token whose key has no value.
	Missing Kind = iota + 1
	// Malformed is an open delimiter not followed by a key and the close delimiter.
	Malformed
	// OutputTooLarge means the rendered output would exceed Config.MaxOutput.
	OutputTooLarge
)

// Problem is one reason an input could not be rendered. Line and Column are
// 1-based; Column counts bytes.
type Problem struct {
	Kind   Kind
	Key    string // set for Missing
	Line   int
	Column int
}

// Message describes the problem without its position.
func (p Problem) Message(s Syntax) string {
	switch p.Kind {
	case Missing:
		return fmt.Sprintf("no value for %s", p.Key)
	case Malformed:
		return fmt.Sprintf("malformed token: %q must be followed by a key ([A-Za-z_][A-Za-z0-9_]*, at most %d bytes) and %q", s.Open, MaxKeyLen, s.Close)
	case OutputTooLarge:
		return "rendered output exceeds the size limit"
	default:
		return "unknown problem"
	}
}

// Config configures an Engine.
type Config struct {
	Syntax Syntax
	Values map[string]string
	// MaxOutput is the largest rendered output in bytes; 0 means no limit.
	MaxOutput int
}

// Engine renders inputs against a fixed set of values. It is safe for
// concurrent use.
type Engine struct {
	syntax    Syntax
	open      []byte
	close     []byte
	values    map[string]string
	maxOutput int
}

// New validates cfg and returns an Engine holding a copy of cfg.Values.
func New(cfg Config) (*Engine, error) {
	if err := cfg.Syntax.Validate(); err != nil {
		return nil, err
	}
	if cfg.MaxOutput < 0 {
		return nil, fmt.Errorf("max output must not be negative, got %d", cfg.MaxOutput)
	}
	values := make(map[string]string, len(cfg.Values))
	for k, v := range cfg.Values {
		if !ValidKey(k) {
			return nil, fmt.Errorf("invalid key %q: keys must match [A-Za-z_][A-Za-z0-9_]* and be at most %d bytes", k, MaxKeyLen)
		}
		values[k] = v
	}
	return &Engine{
		syntax:    cfg.Syntax,
		open:      []byte(cfg.Syntax.Open),
		close:     []byte(cfg.Syntax.Close),
		values:    values,
		maxOutput: cfg.MaxOutput,
	}, nil
}

// Syntax returns the engine's delimiters.
func (e *Engine) Syntax() Syntax { return e.syntax }

// Result is the outcome of rendering one input.
type Result struct {
	// Output is the rendered input. It is nil when Problems is non-empty, and
	// it aliases the input when Replaced is zero.
	Output []byte
	// Replaced counts the tokens replaced.
	Replaced int
	// Problems lists problems in input order, at most MaxProblems of them.
	Problems []Problem
	// Truncated reports that scanning stopped at MaxProblems.
	Truncated bool
}

// Render replaces every token in src. It never modifies src.
func (e *Engine) Render(src []byte) Result {
	var (
		res  Result
		out  []byte
		pos  int // next byte to scan
		last int // start of the input not yet copied to out
		loc  locator
	)
	report := func(p Problem, at int) bool {
		p.Line, p.Column = loc.position(src, at)
		res.Problems = append(res.Problems, p)
		if len(res.Problems) == MaxProblems {
			res.Truncated = true
			return false
		}
		return true
	}

	for {
		i := bytes.Index(src[pos:], e.open)
		if i < 0 {
			break
		}
		start := pos + i
		keyStart := start + len(e.open)
		keyEnd := keyStart + keyLen(src[keyStart:])
		if keyEnd == keyStart || keyEnd-keyStart > MaxKeyLen || !bytes.HasPrefix(src[keyEnd:], e.close) {
			if !report(Problem{Kind: Malformed}, start) {
				return res
			}
			pos = keyStart
			continue
		}
		end := keyEnd + len(e.close)
		pos = end

		val, ok := e.values[string(src[keyStart:keyEnd])]
		if !ok {
			if !report(Problem{Kind: Missing, Key: string(src[keyStart:keyEnd])}, start) {
				return res
			}
			continue
		}
		if len(res.Problems) > 0 {
			continue // keep scanning for problems; output is discarded
		}
		if e.maxOutput > 0 && len(out)+(start-last)+len(val) > e.maxOutput {
			report(Problem{Kind: OutputTooLarge}, start)
			return res
		}
		if out == nil {
			out = make([]byte, 0, len(src)+len(val))
		}
		out = append(out, src[last:start]...)
		out = append(out, val...)
		last = end
		res.Replaced++
	}

	if len(res.Problems) > 0 {
		return res
	}
	if res.Replaced == 0 {
		res.Output = src
		return res
	}
	if e.maxOutput > 0 && len(out)+len(src)-last > e.maxOutput {
		report(Problem{Kind: OutputTooLarge}, len(src))
		return res
	}
	res.Output = append(out, src[last:]...)
	return res
}

// keyLen returns the length of the key at the start of b, or 0 if b does not
// start with one. It stops one byte past MaxKeyLen so overlong keys are
// detectable without scanning further.
func keyLen(b []byte) int {
	if len(b) == 0 || !isKeyByte(b[0]) || isDigit(b[0]) {
		return 0
	}
	n := 1
	for n < len(b) && n <= MaxKeyLen && isKeyByte(b[n]) {
		n++
	}
	return n
}

func isKeyByte(c byte) bool {
	return c == '_' || isDigit(c) || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// locator converts byte offsets to line and column. Offsets must be
// non-decreasing across calls, which keeps the total cost linear.
type locator struct {
	scanned   int // offset up to which newlines have been counted
	line      int // number of newlines before scanned
	lineStart int // offset of the first byte of the current line
}

func (l *locator) position(src []byte, at int) (line, col int) {
	seg := src[l.scanned:at]
	if n := bytes.Count(seg, []byte{'\n'}); n > 0 {
		l.line += n
		l.lineStart = l.scanned + bytes.LastIndexByte(seg, '\n') + 1
	}
	l.scanned = at
	return l.line + 1, at - l.lineStart + 1
}
