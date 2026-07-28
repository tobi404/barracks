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
//
// And the rule that governs every line past the first step: an escalated line
// says the unit is tired of being sent, never that the deployment is already in
// place. "Already there." is a claim about state, and barracks cannot stand
// behind it - a spawn, a recall and a second spawn escalate too, and the unit
// very much did leave in between. Weariness at the repetition is always true
// whenever the line can fire; a state claim is only sometimes true, and a voice
// line that states a falsehood is worse than no voice line, because the reader
// cannot tell flavor from fact.
var pools = map[string][]step{
	"train": {
		{"Formed up.", "Fresh from the drills.", "A new banner raised."},
		{"Again to the yard.", "Drilled this one before.", "More drilling, then."},
		{"...drilling again.", "The parade ground wears thin.", "Marching in circles."},
		{"Drilled and drilled again!", "No more drills.", "Enough of the yard."},
	},
	"equip": {
		{"Kit issued.", "Sharpening up.", "The quartermaster obliges."},
		{"Back for more kit.", "Back at the armory.", "The quartermaster sighs."},
		{"...the pack is heavy.", "Loaded up once more.", "Little room left."},
		{"Hands are full!", "No more crates.", "The straps are creaking."},
	},
	"spawn": {
		{"Off to the front.", "Moving out.", "Taking position."},
		{"The same front again.", "Marching out once more.", "Boots are on the ground."},
		{"...the same ground again.", "Back to the same line.", "Same ground, same watch."},
		{"Always this same ground!", "This ground again.", "This ground knows us."},
	},
	"recall": {
		{"Falling back.", "Standing down.", "Marching home."},
		{"Falling back once more.", "Back down the road.", "Homeward, again."},
		{"...back again already.", "The road home is worn.", "Marching back and forth."},
		{"Nothing but marching!", "Turned around again!", "The road again!"},
	},
	"upgrade": {
		{"To the depot.", "Off to the quartermaster.", "The runner sets out."},
		{"The runner returns.", "Back to the depot.", "Sent to the depot again."},
		{"...sent out once more.", "This errand wears thin.", "Another trip, another errand."},
		{"Always the same errand!", "The depot and back, again.", "Enough of this road."},
	},
	"garrison": {
		{"Manning the walls.", "Colors planted.", "Settling into the fort."},
		{"The walls are manned.", "The colors are up.", "Quarters are taken."},
		{"...the same wall.", "Posted here again.", "A long watch."},
		{"The wall is manned!", "Still this same gate!", "The same stones again."},
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
