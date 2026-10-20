package setup

import "bytes"

const skillMarker = "meow-skill:"

// IsMeowSkill reports whether a SKILL.md was written by an earlier meow
// setup. Setup no longer installs the skill (it is published in the
// repository's skills/meow and installed with skills.sh), but it still
// recognizes those copies so that it can remove them.
func IsMeowSkill(b []byte) bool { return bytes.Contains(b, []byte(skillMarker)) }
