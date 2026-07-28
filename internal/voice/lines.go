package voice

// Marker prefixes every flavor line, so a reader can tell at a glance that the
// line is barracks talking rather than part of the report above it.
const Marker = "▸"

// pools is the whole of the voice: which commands speak, and what they say.
//
// A command with no entry here is silent - that is the entire rule, and it is
// why `list`, `deployed`, `inspect` and `targets` say nothing. Only commands
// that change something have a pool.
//
// Each entry is one pool per escalation step, ordered from freshly ordered to
// thoroughly put-upon. Repeating a command on the same loadout inside the
// escalation window walks down this list; a quiet period puts it back at the
// top. A step's lines are interchangeable, so which one is picked is only a
// matter of not repeating yesterday's phrasing.
//
// The house style, for anyone adding to these: two to five words, understated,
// spoken by the unit about itself. Never addresses the reader, never mentions
// files, flags or paths, never restates what the line above already said.
var pools = map[string][]step{
	"train": {
		{"Formed up.", "Fresh from the drills.", "A new banner raised."},
		{"Formed up already.", "Drilled this one before.", "The banner is up."},
		{"...drilling again.", "The parade ground wears thin.", "Marching in circles."},
		{"We are trained!", "No more drills.", "Enough of the yard."},
	},
	"equip": {
		{"Kit issued.", "Sharpening up.", "The quartermaster obliges."},
		{"Kit is already issued.", "Back at the armory.", "The same crate."},
		{"...the pack is heavy.", "Carrying plenty already.", "Little room left."},
		{"Hands are full!", "No more crates.", "The straps are creaking."},
	},
	"spawn": {
		{"Off to the front.", "Moving out.", "Taking position."},
		{"Already there.", "The position is held.", "Boots are on the ground."},
		{"...still here.", "Not moved since.", "Same ground, same watch."},
		{"We never left!", "This ground again.", "Nothing has changed here."},
	},
	"recall": {
		{"Falling back.", "Standing down.", "Marching home."},
		{"Already fell back.", "Nothing left to pull.", "The line is empty."},
		{"...nobody out there.", "Withdrawing from an empty field.", "The camp is packed."},
		{"There is no one left!", "Recalling the wind.", "An empty field again."},
	},
	"upgrade": {
		{"New orders taken.", "Fresh supply.", "Blades reground."},
		{"Orders unchanged.", "The supply already came.", "The blades are keen."},
		{"...the same orders.", "The runner brought nothing.", "Barely sharper than yesterday."},
		{"The orders will not change!", "Nothing new arrives.", "No word from command."},
	},
	"garrison": {
		{"Manning the walls.", "Colors planted.", "Settling into the fort."},
		{"The walls are manned.", "The colors already fly.", "Quarters are taken."},
		{"...the same wall.", "The stones have not moved.", "A long watch."},
		{"The wall is manned!", "Nothing approaches this gate.", "The same stones again."},
	},
	"run": {
		{"Marching alongside.", "Right behind.", "Falling in."},
		{"Falling in again.", "Same escort.", "Back in step."},
		{"...marching again.", "The boots wear thin.", "The same road."},
		{"This road once more!", "The boots are finished.", "Always the same march."},
	},
}

// step is the interchangeable set of lines for one level of annoyance.
type step []string

// steps is how far the annoyance can climb. Every pool is this long; the
// constant exists so state can be clamped without consulting a pool.
const steps = 4

// Speaks reports whether a command has a voice at all.
func Speaks(command string) bool {
	_, ok := pools[command]
	return ok
}
