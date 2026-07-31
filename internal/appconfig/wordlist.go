package appconfig

// tokenWords is the vocabulary for generated auth tokens.
//
// Hand-curated rather than filtered out of a system dictionary, because a
// mechanical filter over /usr/share/dict/words yields things like "abaci" and
// "abeam" — fine as entropy, terrible for a credential whose entire point is
// being read aloud down a phone line and typed on a TV remote. Every word
// here is common, concrete, and unambiguously spelled.
//
// Constraints held by the list, enforced by TestWordlistProperties:
//
//   - 3 to 8 lowercase ASCII letters.
//   - No duplicates.
//   - No homophones or near-homophones (no bare/bear, steal/steel), since the
//     spoken form has to round-trip to exactly one spelling.
//   - No plural of another entry, for the same reason.
//   - Nothing violent, medical, or otherwise unpleasant to read out to a
//     family member.
//
// A distinct-three-character-prefix rule was considered and dropped: it would
// have cost roughly a fifth of the list (canary/canyon, temple/tempo,
// sunrise/sunset all collide) to buy typo tolerance for a partial-entry
// affordance that does not exist here — tokens are entered whole, not
// autocompleted. Whole-word distinctness is what actually matters, and that
// is guaranteed by the no-duplicates and no-homophones rules above.
//
// Length is deliberately not a power of two; tokenWord uses big.Int sampling,
// which is unbiased for any modulus. See TokenEntropyBits for what the size
// actually buys.
var tokenWords = []string{
	// animals
	"otter", "badger", "falcon", "walrus", "gecko", "puffin", "marten", "lemur",
	"bison", "cobra", "dingo", "ferret", "gibbon", "heron", "impala", "jackal",
	"koala", "llama", "meerkat", "newt", "ocelot", "panda", "quail", "rabbit",
	"salmon", "tapir", "urchin", "viper", "wombat", "yak", "zebra", "beaver",
	"cougar", "donkey", "egret", "finch", "gopher", "hornet", "iguana", "kitten",
	"lobster", "magpie", "narwhal", "osprey", "parrot", "raven", "shrimp", "turtle",
	"weasel", "bobcat", "canary", "dolphin", "elk", "flamingo", "gazelle", "hamster",

	// nature and weather
	"amber", "birch", "canyon", "delta", "ember", "fjord", "glacier", "harbor",
	"island", "jungle", "kelp", "lagoon", "meadow", "nebula", "oasis", "prairie",
	"quartz", "ridge", "summit", "tundra", "valley", "willow", "cedar", "dune",
	"forest", "grotto", "hollow", "inlet", "jasmine", "lichen", "marsh", "orchid",
	"pebble", "reef", "savanna", "thistle", "vine", "aspen", "boulder", "cliff",
	"drizzle", "eclipse", "frost", "gale", "haze", "monsoon", "nimbus", "aurora",
	"blizzard", "cyclone", "dew", "flurry", "hail", "mist", "rainbow", "thunder",

	// colors and materials
	"azure", "bronze", "copper", "crimson", "denim", "emerald", "fuchsia", "gold",
	"indigo", "ivory", "jade", "khaki", "lilac", "maroon", "olive", "pewter",
	"ruby", "sapphire", "teal", "umber", "velvet", "beige", "cobalt", "flax",
	"garnet", "henna", "linen", "mauve", "onyx", "pearl", "russet", "silver",
	"topaz", "walnut", "zinc", "granite", "marble", "opal", "satin", "wicker",

	// food and drink
	"almond", "bagel", "cocoa", "donut", "endive", "fennel", "ginger", "honey",
	"jam", "kiwi", "lemon", "mango", "nutmeg", "pepper", "papaya", "quinoa",
	"radish", "saffron", "tomato", "vanilla", "waffle", "yogurt", "apricot", "basil",
	"cashew", "date", "fig", "grape", "hazel", "juniper", "lentil", "melon",
	"noodle", "oregano", "peanut", "raisin", "sesame", "truffle", "wheat", "yam",

	// objects
	"anchor", "beacon", "compass", "domino", "engine", "fabric", "gadget", "hammer",
	"inkwell", "jigsaw", "kettle", "lantern", "mirror", "needle", "organ", "pencil",
	"quiver", "ribbon", "saddle", "teapot", "umbrella", "violin", "wagon", "anvil",
	"basket", "candle", "drum", "easel", "funnel", "goblet", "harp", "ladder",
	"mallet", "nozzle", "paddle", "rocket", "shovel", "tripod", "vessel", "whistle",
	"abacus", "bucket", "chisel", "dial", "flask", "gauge", "helmet", "kayak",
	"lever", "magnet", "piston", "quilt", "rudder", "socket", "trowel", "wrench",

	// places and structures
	"arcade", "bridge", "castle", "dome", "estate", "forge", "gallery", "hangar",
	"igloo", "jetty", "kiosk", "lodge", "manor", "nursery", "orchard", "pavilion",
	"quarry", "rampart", "studio", "temple", "vault", "windmill", "attic", "balcony",
	"cabin", "depot", "foyer", "garden", "hostel", "library", "market", "obelisk",
	"parlor", "rotunda", "spire", "tavern", "veranda", "wharf", "bazaar", "chapel",

	// abstract and pleasant
	"anthem", "ballad", "cadence", "duet", "echo", "fable", "gusto", "harmony",
	"idyll", "jubilee", "lyric", "melody", "nocturne", "octave", "poem", "rhythm",
	"sonnet", "tempo", "verse", "waltz", "aura", "bliss", "charm", "dream",
	"ease", "fervor", "glee", "hope", "jest", "kindle", "luster", "mirth",
	"nectar", "polish", "quest", "relish", "spark", "trust", "vigor", "whim",

	// motion and shape
	"arrow", "bounce", "circle", "dash", "eddy", "flutter", "glide", "hover",
	"jolt", "leap", "orbit", "pivot", "ripple", "spiral", "twirl", "vortex",
	"wander", "zigzag", "arch", "bevel", "cube", "diamond", "ellipse", "helix",
	"lattice", "mosaic", "prism", "sphere", "wedge", "zenith",

	// sky and time
	"comet", "dusk", "equinox", "galaxy", "horizon", "lunar", "meteor", "noon",
	"planet", "quasar", "solar", "twilight", "dawn", "evening", "midday", "season",
	"spring", "solstice", "autumn", "winter", "epoch", "moment", "sunrise", "sunset",
}
