package config

// A migration rewrites an older beeircd.conf for this version: a renamed
// setting, a moved block. It works on the text so comments stay, and
// reports what it changed in plain words. `beeircd update` runs the NEW
// program's migrations on a copy, checks the result with the new program,
// and only then puts it in place (the old file is kept next to it).
type migration struct {
	apply func(text string) (string, string) // new text, what changed ("" = nothing)
}

// migrations are the config changes since v1.0, oldest first. None yet.
var migrations []migration

// Upgrade applies every migration to text and lists what changed.
func Upgrade(text string) (string, []string) {
	var changed []string
	for _, m := range migrations {
		var what string
		text, what = m.apply(text)
		if what != "" {
			changed = append(changed, what)
		}
	}
	return text, changed
}
