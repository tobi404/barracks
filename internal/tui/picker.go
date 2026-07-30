package tui

// The groups a card's picker can offer. They are looked up by these ids and
// never by position, for the reason the launcher is looked up by key and never
// by row: an order that read the wrong band would deploy somewhere nobody chose
// or install skills nobody ticked, silently, and one filtering change is all it
// would take.
const (
	groupTargets = "targets"
	groupSkills  = "skills"
	groupAgent   = "agent"
)

// picker is the list of choices a card puts in front of the user.
//
// It is one widget with one cursor rather than several lists with a focus key,
// because every choice on a card answers the same question - what should this
// order do - and ↑/↓/space over a single list is the whole interaction. The
// bands are only how the list is headed and how its answers are reported back.
//
// Two orders need one and they need different things of it: a deploy chooses as
// many agents as it likes and as many of the loadout's skills as it likes, and a
// run starts exactly one program. Both are the same list with a flag per band
// rather than lists that would drift apart.
type picker struct {
	groups  []group
	options []choice
	on      []bool
	cursor  int
}

// group is one band of a picker: a heading, and what its answers mean.
type group struct {
	// ID is what an order asks for its answer by.
	ID string
	// Title heads the band on the card.
	Title string
	// Noun is what one row of this band is, for the refusal a card raises when
	// nothing in it is chosen.
	Noun string
	// Multi is whether more than one option in this band may be on at once.
	Multi bool
	// touched is whether the user has actually chosen in this band.
	//
	// It is per band, and that is load-bearing rather than tidy. An untouched
	// deploy picker says "wherever barracks would have sent it", which is a
	// different statement from naming those same agents by hand: the first leaves
	// the loadout's own declaration and the repository's evidence in charge, and
	// the second overrides both. Ticking a *skill* must not turn the targets into
	// an explicit choice, or choosing which skills go out would silently pin the
	// deployment to whatever was detected that day.
	touched bool
}

// band is one group and the options under it, as a card declares them.
type band struct {
	ID, Title, Noun string
	Multi           bool
	Options         []choice
	// Chosen are the keys in this band that open ticked.
	Chosen []string
}

// choice is one row of a picker.
type choice struct {
	// Key is what the choice is reported as - a target ID, a skill name, a
	// program name.
	Key string
	// Label is what the row says.
	Label string
	// Note is the dim half of the row, empty when there is nothing to add.
	Note string
	// Group is the band this row belongs to, as an index into picker.groups.
	Group int
}

// newPicker builds a picker over the bands, each opening on its own chosen set.
//
// The chosen sets are per band rather than one list of keys across all of them:
// a target ID and a skill name are strings from two different namespaces, and a
// loadout carrying a skill called "cursor" would otherwise open with an agent
// nobody chose already ticked.
func newPicker(bands ...band) picker {
	var p picker
	for _, b := range bands {
		if len(b.Options) == 0 {
			continue
		}
		g := len(p.groups)
		p.groups = append(p.groups, group{ID: b.ID, Title: b.Title, Noun: b.Noun, Multi: b.Multi})
		want := map[string]bool{}
		for _, k := range b.Chosen {
			want[k] = true
		}
		for _, o := range b.Options {
			o.Group = g
			p.options = append(p.options, o)
			p.on = append(p.on, want[o.Key])
		}
	}
	// The cursor opens on something that is already chosen, so the first thing
	// the space bar does is un-choose a row rather than silently add one the
	// user never looked at.
	for i, on := range p.on {
		if on {
			p.cursor = i
			break
		}
	}
	return p
}

func (p *picker) move(d int) {
	if len(p.options) == 0 {
		return
	}
	p.cursor = (p.cursor + d + len(p.options)) % len(p.options)
}

// toggle turns the option under the cursor on or off. In a single-choice band
// it moves the choice there instead, because a run with no program to start is
// not a state the card can offer.
func (p *picker) toggle() {
	if len(p.options) == 0 {
		return
	}
	g := p.options[p.cursor].Group
	p.groups[g].touched = true
	if !p.groups[g].Multi {
		for i, o := range p.options {
			if o.Group == g {
				p.on[i] = false
			}
		}
		p.on[p.cursor] = true
		return
	}
	p.on[p.cursor] = !p.on[p.cursor]
}

// keys are the chosen options of one band, in the order the picker offers them.
// An id no band carries answers with nothing rather than with everything.
func (p picker) keys(id string) []string {
	var out []string
	for i, on := range p.on {
		if on && p.groups[p.options[i].Group].ID == id {
			out = append(out, p.options[i].Key)
		}
	}
	return out
}

// chosen is what the order should be given for one band: the keys when the user
// picked in it, and nil when they left it exactly as it opened.
func (p picker) chosen(id string) []string {
	for _, g := range p.groups {
		if g.ID == id && g.touched {
			return p.keys(id)
		}
	}
	return nil
}

// emptyBand is the first band with nothing at all chosen in it, which is the one
// state an order cannot be started from - and which band it is decides what the
// card says.
func (p picker) emptyBand() (group, bool) {
	for _, g := range p.groups {
		if len(p.keys(g.ID)) == 0 {
			return g, true
		}
	}
	return group{}, false
}

// window is the slice of the options that fits in n rows, with the cursor in
// it. A picker is the one list that must never hide a row the user is standing
// on - a choice you cannot see is a choice you cannot make.
func (p picker) window(n int) (int, int) {
	if n < 1 {
		n = 1
	}
	if len(p.options) <= n {
		return 0, len(p.options)
	}
	first := p.cursor - n/2
	if first < 0 {
		first = 0
	}
	if first+n > len(p.options) {
		first = len(p.options) - n
	}
	return first, first + n
}
