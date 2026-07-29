package tui

// picker is the list of choices a card puts in front of the user.
//
// Two orders need one and they need different things of it: a deploy installs
// into as many agents as it likes, and a run starts exactly one program. Both
// are the same list with the same keys, so they are the same widget with a flag
// rather than two lists that would drift apart.
type picker struct {
	options []choice
	on      []bool
	cursor  int
	// multi is whether more than one option may be on at once.
	multi bool
	// touched is whether the user has actually chosen anything.
	//
	// It is what keeps the picker from changing the meaning of a deploy nobody
	// touched. An untouched picker says "wherever barracks would have sent it",
	// which is a different statement from naming those same agents by hand: the
	// first leaves the loadout's own declaration and the repository's evidence
	// in charge, and the second overrides both. A picker that opened on the
	// detected set and then reported it as an explicit choice would silently
	// pin a loadout to today's detection.
	touched bool
}

// choice is one row of a picker.
type choice struct {
	// Key is what the choice is reported as - a target ID, a program name.
	Key string
	// Label is what the row says.
	Label string
	// Note is the dim half of the row, empty when there is nothing to add.
	Note string
}

// newPicker builds a picker over options, with on set for every key in chosen.
func newPicker(options []choice, chosen []string, multi bool) picker {
	p := picker{options: options, on: make([]bool, len(options)), multi: multi}
	want := map[string]bool{}
	for _, k := range chosen {
		want[k] = true
	}
	for i, o := range options {
		p.on[i] = want[o.Key]
	}
	// The cursor opens on something that is already chosen, so the first thing
	// the space bar does is un-choose a target rather than silently add one the
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

// toggle turns the option under the cursor on or off. In a single-choice picker
// it moves the choice there instead, because a run with no program to start is
// not a state the card can offer.
func (p *picker) toggle() {
	if len(p.options) == 0 {
		return
	}
	p.touched = true
	if !p.multi {
		for i := range p.on {
			p.on[i] = false
		}
		p.on[p.cursor] = true
		return
	}
	p.on[p.cursor] = !p.on[p.cursor]
}

// keys are the chosen options, in the order the picker offers them.
func (p picker) keys() []string {
	var out []string
	for i, on := range p.on {
		if on {
			out = append(out, p.options[i].Key)
		}
	}
	return out
}

// chosen is what the order should be given: the keys when the user picked, and
// nil when they left the picker exactly as it opened.
func (p picker) chosen() []string {
	if !p.touched {
		return nil
	}
	return p.keys()
}

// empty reports whether nothing at all is selected, which is the one state an
// order cannot be started from.
func (p picker) empty() bool { return len(p.keys()) == 0 }

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
