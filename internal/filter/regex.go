package filter

import (
	"fmt"
	"regexp/syntax"
	"strings"
	"unicode"

	"github.com/xen0bit/kaleid/internal/apierr"
)

// Chroma validates $regex patterns with Rust's regex crate. Go's
// regexp/syntax is from the same RE2 family, so we parse with it and then
// re-emit an equivalent PostgreSQL ARE from the syntax tree. Emitting from the
// tree (rather than passing the pattern through) avoids dialect differences
// such as \b meaning backspace in PostgreSQL.

const parseFlags = syntax.Perl

// ValidateRegex returns an InvalidArgument error when the pattern is invalid
// or cannot be expressed in PostgreSQL.
func ValidateRegex(pattern string) error {
	_, err := TranslateRegex(pattern)
	return err
}

// TranslateRegex converts a Rust/RE2 pattern into a PostgreSQL ARE.
func TranslateRegex(pattern string) (string, error) {
	re, err := syntax.Parse(pattern, parseFlags)
	if err != nil {
		return "", apierr.InvalidArgument("Regex syntax errror: %s", err.Error())
	}
	var b strings.Builder
	if err := emit(&b, re); err != nil {
		return "", apierr.InvalidArgument("Regex syntax errror: %s", err.Error())
	}
	return b.String(), nil
}

func isARESpecial(r rune) bool {
	return r < 128 && !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != ' ' && r != '_'
}

func writeRuneEscaped(b *strings.Builder, r rune, inBracket bool) {
	switch {
	case r == '\n':
		b.WriteString(`\n`)
	case r == '\t':
		b.WriteString(`\t`)
	case r == '\r':
		b.WriteString(`\r`)
	case r < 0x20 || r == 0x7f:
		fmt.Fprintf(b, `\u%04x`, r)
	case r >= 0x80 && r <= 0xffff:
		fmt.Fprintf(b, `\u%04x`, r)
	case r > 0xffff:
		fmt.Fprintf(b, `\U%08x`, r)
	case inBracket && (r == '\\' || r == ']' || r == '[' || r == '^' || r == '-'):
		b.WriteByte('\\')
		b.WriteRune(r)
	case !inBracket && isARESpecial(r):
		b.WriteByte('\\')
		b.WriteRune(r)
	default:
		b.WriteRune(r)
	}
}

func writeFoldedLiteral(b *strings.Builder, r rune) {
	folds := []rune{r}
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		folds = append(folds, f)
	}
	if len(folds) == 1 {
		writeRuneEscaped(b, r, false)
		return
	}
	b.WriteByte('[')
	for _, f := range folds {
		writeRuneEscaped(b, f, true)
	}
	b.WriteByte(']')
}

// splitSurrogates removes the UTF-16 surrogate block, which PostgreSQL does
// not accept in escapes (and which can never occur in valid UTF-8 text).
func splitSurrogates(ranges []rune) []rune {
	out := make([]rune, 0, len(ranges))
	for i := 0; i+1 < len(ranges); i += 2 {
		lo, hi := ranges[i], ranges[i+1]
		if hi < 0xD800 || lo > 0xDFFF {
			out = append(out, lo, hi)
			continue
		}
		if lo < 0xD800 {
			out = append(out, lo, 0xD7FF)
		}
		if hi > 0xDFFF {
			out = append(out, 0xE000, hi)
		}
	}
	return out
}

func emitClass(b *strings.Builder, ranges []rune) {
	ranges = splitSurrogates(ranges)
	if len(ranges) == 0 {
		b.WriteString(`\Zx`) // can never match
		return
	}
	if len(ranges) == 4 && ranges[0] == 0 && ranges[1] == 0xD7FF && ranges[2] == 0xE000 && ranges[3] == unicode.MaxRune {
		b.WriteByte('.')
		return
	}
	b.WriteByte('[')
	for i := 0; i+1 < len(ranges); i += 2 {
		lo, hi := ranges[i], ranges[i+1]
		if lo == 0 {
			lo = 1 // NUL cannot appear in PostgreSQL text
			if hi == 0 {
				continue
			}
		}
		writeRuneEscaped(b, lo, true)
		if hi != lo {
			b.WriteByte('-')
			writeRuneEscaped(b, hi, true)
		}
	}
	b.WriteByte(']')
}

func emitGroup(b *strings.Builder, re *syntax.Regexp) error {
	b.WriteString("(?:")
	if err := emit(b, re); err != nil {
		return err
	}
	b.WriteByte(')')
	return nil
}

func emit(b *strings.Builder, re *syntax.Regexp) error {
	switch re.Op {
	case syntax.OpNoMatch:
		b.WriteString(`\Zx`)
	case syntax.OpEmptyMatch:
		b.WriteString("(?:)")
	case syntax.OpLiteral:
		for _, r := range re.Rune {
			if re.Flags&syntax.FoldCase != 0 {
				writeFoldedLiteral(b, r)
			} else {
				writeRuneEscaped(b, r, false)
			}
		}
	case syntax.OpCharClass:
		emitClass(b, re.Rune)
	case syntax.OpAnyCharNotNL:
		b.WriteString(`[^\n]`)
	case syntax.OpAnyChar:
		b.WriteByte('.')
	case syntax.OpBeginLine:
		b.WriteString(`(?:^|(?<=\n))`)
	case syntax.OpEndLine:
		b.WriteString(`(?:$|(?=\n))`)
	case syntax.OpBeginText:
		b.WriteByte('^')
	case syntax.OpEndText:
		b.WriteByte('$')
	case syntax.OpWordBoundary:
		b.WriteString(`\y`)
	case syntax.OpNoWordBoundary:
		b.WriteString(`\Y`)
	case syntax.OpCapture:
		return emitGroup(b, re.Sub[0])
	case syntax.OpStar, syntax.OpPlus, syntax.OpQuest:
		if err := emitGroup(b, re.Sub[0]); err != nil {
			return err
		}
		b.WriteString(map[syntax.Op]string{syntax.OpStar: "*", syntax.OpPlus: "+", syntax.OpQuest: "?"}[re.Op])
		if re.Flags&syntax.NonGreedy != 0 {
			b.WriteByte('?')
		}
	case syntax.OpRepeat:
		if re.Min > 255 || re.Max > 255 {
			return fmt.Errorf("repetition count %d exceeds the supported maximum of 255", max(re.Min, re.Max))
		}
		if err := emitGroup(b, re.Sub[0]); err != nil {
			return err
		}
		switch {
		case re.Max == -1:
			fmt.Fprintf(b, "{%d,}", re.Min)
		case re.Min == re.Max:
			fmt.Fprintf(b, "{%d}", re.Min)
		default:
			fmt.Fprintf(b, "{%d,%d}", re.Min, re.Max)
		}
		if re.Flags&syntax.NonGreedy != 0 {
			b.WriteByte('?')
		}
	case syntax.OpConcat:
		for _, s := range re.Sub {
			if err := emit(b, s); err != nil {
				return err
			}
		}
	case syntax.OpAlternate:
		b.WriteString("(?:")
		for i, s := range re.Sub {
			if i > 0 {
				b.WriteByte('|')
			}
			if err := emit(b, s); err != nil {
				return err
			}
		}
		b.WriteByte(')')
	default:
		return fmt.Errorf("unsupported regex construct")
	}
	return nil
}
