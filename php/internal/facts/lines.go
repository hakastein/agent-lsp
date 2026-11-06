package facts

import (
	"sort"

	"github.com/VKCOM/php-parser/pkg/position"
)

// Position is a place in a file: a 1-based line and a 1-based column in bytes.
type Position struct {
	Line   int
	Column int
}

// Range is a range in a file; End is exclusive.
type Range struct {
	Start Position
	End   Position
}

// lines turns byte offsets of a file into positions.
type lines struct {
	src    []byte
	starts []int // byte offset of each line start
}

func newLines(src []byte) lines {
	starts := []int{0}
	for i, b := range src {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return lines{src: src, starts: starts}
}

func (l lines) span(p *position.Position) Range {
	return Range{Start: l.at(p.StartPos), End: l.at(p.EndPos)}
}

func (l lines) at(offset int) Position {
	offset = min(max(offset, 0), len(l.src))
	line := sort.Search(len(l.starts), func(i int) bool { return l.starts[i] > offset }) - 1
	return Position{Line: line + 1, Column: offset - l.starts[line] + 1}
}
