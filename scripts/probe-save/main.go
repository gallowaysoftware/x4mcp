// Command probe-save answers structural questions about a real savegame.
//
// The parser in internal/x4save reads the sections it already knows about. Before
// it can read a new one, someone has to find out what that section actually looks
// like in a save the game wrote — which elements, nested how, carrying which
// attributes, and (the question that bites hardest) which attributes are OMITTED
// when the value is the default. An attribute that vanishes at 100% hull is the
// same class of bug as a blueprint list that reads as "you own none": the parser
// must decode absence as "full", not as "unknown", and it can only know which by
// looking.
//
// So this tool is deliberately dumb and lossless: it streams the save once and
// reports what is there, never what it means. Nothing here interprets a value.
//
// Modes (exactly one):
//
//	-paths            element-path histogram, deepest-first
//	-attrs PATH       attribute-name union at PATH, with presence counts and samples
//	-dump PATH        complete subtrees at PATH, re-emitted as XML
//	-values PATH@ATTR value histogram for one attribute at PATH
//
// Filters:
//
//	-ancestor k=v     only within an element (self or any ancestor) whose attribute k matches v
//	-where k=v        only elements at PATH whose own attribute k matches v
//	-max N            stop after N matches (0 = no limit)
//	-depth N          truncate paths to N segments in -paths mode
//
// PATH is a slash-separated element-name suffix: "component/build" matches any
// <build> whose parent is <component>, at any depth. A leading "/" anchors at the
// document root. "*" matches one segment. Attribute matching is exact unless the
// value contains "*", which then matches as a substring wildcard.
//
// Usage:
//
//	go run ./scripts/probe-save -in save.xml.gz -paths -depth 6
//	go run ./scripts/probe-save -in save.xml.gz -attrs component -ancestor owner=player
//	go run ./scripts/probe-save -in save.xml.gz -dump construction -ancestor owner=player -max 3
//	go run ./scripts/probe-save -in save.xml.gz -values component@class -ancestor owner=player
//
// It is read-only. It opens the save O_RDONLY and writes only to stdout, because
// the one thing this project must never do to a savegame is change it.
package main

import (
	"bufio"
	"compress/gzip"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

func main() {
	var (
		in       = flag.String("in", "", "path to a savegame (*.xml.gz or *.xml, or - for stdin)")
		paths    = flag.Bool("paths", false, "print an element-path histogram")
		attrs    = flag.String("attrs", "", "print the attribute-name union at PATH")
		dump     = flag.String("dump", "", "print complete subtrees at PATH")
		values   = flag.String("values", "", "print a value histogram for PATH@ATTR")
		ancestor = flag.String("ancestor", "", "restrict to elements inside an ancestor (or self) with attribute k=v")
		where    = flag.String("where", "", "restrict to matched elements whose own attribute is k=v")
		max      = flag.Int("max", 0, "stop after N matches (0 = no limit)")
		depth    = flag.Int("depth", 8, "truncate paths to N segments in -paths mode")
		samples  = flag.Int("samples", 3, "distinct sample values to show per attribute in -attrs mode")
	)
	flag.Parse()

	modes := 0
	for _, on := range []bool{*paths, *attrs != "", *dump != "", *values != ""} {
		if on {
			modes++
		}
	}
	if *in == "" || modes != 1 {
		flag.Usage()
		os.Exit(2)
	}

	anc, err := parsePred(*ancestor)
	if err != nil {
		fatal(fmt.Errorf("-ancestor: %w", err))
	}
	wh, err := parsePred(*where)
	if err != nil {
		fatal(fmt.Errorf("-where: %w", err))
	}

	r, closeAll, err := open(*in)
	if err != nil {
		fatal(err)
	}
	defer closeAll()

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	switch {
	case *paths:
		err = runPaths(r, out, *depth, anc)
	case *attrs != "":
		err = runAttrs(r, out, mustPath(*attrs), anc, wh, *samples)
	case *dump != "":
		err = runDump(r, out, mustPath(*dump), anc, wh, *max)
	case *values != "":
		p, attr, ok := strings.Cut(*values, "@")
		if !ok {
			fatal(fmt.Errorf("-values wants PATH@ATTR, got %q", *values))
		}
		err = runValues(r, out, mustPath(p), attr, anc, wh)
	}
	if err != nil {
		fatal(err)
	}
}

// open returns a reader over the save's XML, transparently gunzipping.
func open(path string) (io.Reader, func(), error) {
	var f *os.File
	var err error
	if path == "-" {
		f = os.Stdin
	} else if f, err = os.Open(path); err != nil {
		return nil, nil, err
	}
	closers := []func(){func() { _ = f.Close() }}
	closeAll := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	// A big buffer matters here: the walk is one long sequential read of ~800 MB.
	br := bufio.NewReaderSize(f, 1<<20)
	if strings.HasSuffix(path, ".gz") || path == "-" {
		peek, _ := br.Peek(2)
		if len(peek) == 2 && peek[0] == 0x1f && peek[1] == 0x8b {
			gz, err := gzip.NewReader(br)
			if err != nil {
				closeAll()
				return nil, nil, fmt.Errorf("gzip: %w", err)
			}
			closers = append(closers, func() { _ = gz.Close() })
			return bufio.NewReaderSize(gz, 1<<20), closeAll, nil
		}
	}
	return br, closeAll, nil
}

// ---- path and predicate matching -------------------------------------------

type pathPat struct {
	segs     []string
	anchored bool
}

func mustPath(s string) pathPat {
	p := pathPat{anchored: strings.HasPrefix(s, "/")}
	for _, seg := range strings.Split(strings.Trim(s, "/"), "/") {
		if seg != "" {
			p.segs = append(p.segs, seg)
		}
	}
	if len(p.segs) == 0 {
		fatal(fmt.Errorf("empty path"))
	}
	return p
}

// match reports whether the element stack ends with this pattern.
func (p pathPat) match(stack []frame) bool {
	if len(stack) < len(p.segs) {
		return false
	}
	if p.anchored && len(stack) != len(p.segs) {
		return false
	}
	off := len(stack) - len(p.segs)
	for i, seg := range p.segs {
		if seg != "*" && seg != stack[off+i].name {
			return false
		}
	}
	return true
}

type pred struct {
	key, val string
	wild     bool
	set      bool
}

func parsePred(s string) (pred, error) {
	if s == "" {
		return pred{}, nil
	}
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return pred{}, fmt.Errorf("want k=v, got %q", s)
	}
	return pred{key: k, val: strings.ReplaceAll(v, "*", ""), wild: strings.Contains(v, "*"), set: true}, nil
}

func (p pred) matchAttrs(as []xml.Attr) bool {
	if !p.set {
		return true
	}
	for _, a := range as {
		if a.Name.Local != p.key {
			continue
		}
		if p.wild {
			if strings.Contains(a.Value, p.val) {
				return true
			}
		} else if a.Value == p.val {
			return true
		}
	}
	return false
}

type frame struct {
	name  string
	attrs []xml.Attr
	// inAnc is true if this element or any ancestor satisfied -ancestor, so the
	// check is O(1) per element rather than a walk up the stack every time.
	inAnc bool
}

// walk streams the document, calling visit for every start element with the
// current stack (visit must not retain it).
//
// visit reports whether it CONSUMED the element — read the decoder all the way
// past its EndElement, as -dump does. That matters: a consumed element's
// EndElement never reaches this loop, so the frame has to be popped here or
// every subsequent path is reported one level too deep for the rest of the file.
func walk(r io.Reader, anc pred, visit func(dec *xml.Decoder, stack []frame) (bool, error)) error {
	dec := xml.NewDecoder(r)
	var stack []frame
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			parentAnc := false
			if len(stack) > 0 {
				parentAnc = stack[len(stack)-1].inAnc
			}
			stack = append(stack, frame{
				name:  t.Name.Local,
				attrs: t.Attr,
				inAnc: parentAnc || (anc.set && anc.matchAttrs(t.Attr)),
			})
			consumed, err := visit(dec, stack)
			if err != nil {
				return err
			}
			if consumed {
				stack = stack[:len(stack)-1]
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
}

func inScope(stack []frame, anc pred) bool {
	if !anc.set {
		return true
	}
	return stack[len(stack)-1].inAnc
}

func pathOf(stack []frame) string {
	var b strings.Builder
	for _, f := range stack {
		b.WriteByte('/')
		b.WriteString(f.name)
	}
	return b.String()
}

// ---- modes -----------------------------------------------------------------

func runPaths(r io.Reader, out io.Writer, depth int, anc pred) error {
	counts := map[string]int{}
	err := walk(r, anc, func(_ *xml.Decoder, stack []frame) (bool, error) {
		if !inScope(stack, anc) {
			return false, nil
		}
		s := stack
		if depth > 0 && len(s) > depth {
			s = s[:depth]
		}
		counts[pathOf(s)]++
		return false, nil
	})
	if err != nil {
		return err
	}
	return printCounts(out, counts, "path")
}

func runValues(r io.Reader, out io.Writer, p pathPat, attr string, anc, wh pred) error {
	counts := map[string]int{}
	err := walk(r, anc, func(_ *xml.Decoder, stack []frame) (bool, error) {
		if !p.match(stack) || !inScope(stack, anc) {
			return false, nil
		}
		self := stack[len(stack)-1]
		if !wh.matchAttrs(self.attrs) {
			return false, nil
		}
		v := "∅ (attribute absent)"
		for _, a := range self.attrs {
			if a.Name.Local == attr {
				v = a.Value
				break
			}
		}
		counts[v]++
		return false, nil
	})
	if err != nil {
		return err
	}
	return printCounts(out, counts, attr)
}

type attrStat struct {
	name    string
	present int
	seen    map[string]bool
	order   []string
}

func runAttrs(r io.Reader, out io.Writer, p pathPat, anc, wh pred, nsamples int) error {
	stats := map[string]*attrStat{}
	total := 0
	err := walk(r, anc, func(_ *xml.Decoder, stack []frame) (bool, error) {
		if !p.match(stack) || !inScope(stack, anc) {
			return false, nil
		}
		self := stack[len(stack)-1]
		if !wh.matchAttrs(self.attrs) {
			return false, nil
		}
		total++
		for _, a := range self.attrs {
			s := stats[a.Name.Local]
			if s == nil {
				s = &attrStat{name: a.Name.Local, seen: map[string]bool{}}
				stats[a.Name.Local] = s
			}
			s.present++
			if len(s.order) < nsamples && !s.seen[a.Value] {
				s.seen[a.Value] = true
				s.order = append(s.order, a.Value)
			}
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	list := make([]*attrStat, 0, len(stats))
	for _, s := range stats {
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].present != list[j].present {
			return list[i].present > list[j].present
		}
		return list[i].name < list[j].name
	})
	fmt.Fprintf(out, "%d elements matched\n\n", total)
	fmt.Fprintf(out, "%-24s %9s %7s  %s\n", "attribute", "present", "of all", "samples")
	for _, s := range list {
		pct := 0.0
		if total > 0 {
			pct = 100 * float64(s.present) / float64(total)
		}
		// The interesting column is "of all": anything under 100% is an attribute
		// the game omits sometimes, and every one of those is a question about
		// what absence means.
		fmt.Fprintf(out, "%-24s %9d %6.1f%%  %s\n", s.name, s.present, pct, strings.Join(s.order, " | "))
	}
	return nil
}

func runDump(r io.Reader, out io.Writer, p pathPat, anc, wh pred, max int) error {
	n := 0
	errStop := fmt.Errorf("stop")
	err := walk(r, anc, func(dec *xml.Decoder, stack []frame) (bool, error) {
		if !p.match(stack) || !inScope(stack, anc) {
			return false, nil
		}
		self := stack[len(stack)-1]
		if !wh.matchAttrs(self.attrs) {
			return false, nil
		}
		n++
		fmt.Fprintf(out, "<!-- match %d at %s -->\n", n, pathOf(stack))
		start := xml.StartElement{Name: xml.Name{Local: self.name}, Attr: stripNS(self.attrs)}
		enc := xml.NewEncoder(out)
		enc.Indent("", "  ")
		if err := encodeSubtree(dec, enc, start); err != nil {
			return true, err
		}
		if err := enc.Flush(); err != nil {
			return true, err
		}
		fmt.Fprintln(out)
		if max > 0 && n >= max {
			return true, errStop
		}
		return true, nil
	})
	if err != nil && err != errStop {
		return err
	}
	if n == 0 {
		fmt.Fprintln(out, "no matches")
	}
	return nil
}

func encodeSubtree(dec *xml.Decoder, enc *xml.Encoder, start xml.StartElement) error {
	if err := enc.EncodeToken(start); err != nil {
		return err
	}
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if err := enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: t.Name.Local}, Attr: stripNS(t.Attr)}); err != nil {
				return err
			}
		case xml.EndElement:
			if err := enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: t.Name.Local}}); err != nil {
				return err
			}
			if depth == 0 {
				return nil
			}
			depth--
		case xml.CharData:
			if err := enc.EncodeToken(xml.CharData(t)); err != nil {
				return err
			}
		}
	}
}

// stripNS drops namespace prefixes Go would otherwise re-emit as xmlns
// declarations the save never had.
func stripNS(as []xml.Attr) []xml.Attr {
	out := make([]xml.Attr, 0, len(as))
	for _, a := range as {
		if a.Name.Local == "xmlns" || a.Name.Space == "xmlns" {
			continue
		}
		out = append(out, xml.Attr{Name: xml.Name{Local: a.Name.Local}, Value: a.Value})
	}
	return out
}

func printCounts(out io.Writer, counts map[string]int, label string) error {
	type kv struct {
		k string
		n int
	}
	list := make([]kv, 0, len(counts))
	for k, n := range counts {
		list = append(list, kv{k, n})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].k < list[j].k
	})
	fmt.Fprintf(out, "%9s  %s\n", "count", label)
	for _, e := range list {
		fmt.Fprintf(out, "%9d  %s\n", e.n, e.k)
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "probe-save:", err)
	os.Exit(1)
}
