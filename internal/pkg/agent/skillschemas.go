package agent

// use_skill loads one effective skill's SKILL.md content in full. Agent
// Skills progressive disclosure means the system prompt only ever carries
// the bounded catalogue of names and descriptions; this tool is the one way
// the model pays for a skill's full body, and only for skills it activates.

const useSkillDescription = `Load one skill's complete instructions by name.
The system prompt lists every effective skill for this workspace with its
name and a short description. Call this with one of those exact names to
load its full SKILL.md content, its source directory, and a content hash.
The returned directory is an absolute path to that skill's own directory.
It may contain sibling files its SKILL.md references, commonly references/
and scripts/. Those are not preloaded: read them yourself with read_file
once you know they exist, so each read shows up as its own ordinary tool
call.
To run a bundled script, pass that directory as run_command's directory so
the script runs from its own location and can find its neighbours. Skills
in different layers can share a name, and you always get the directory of
the one that actually won, so a bundled script is always the winning
copy's.
Calling this again for the same name in the same turn returns identical
content.
A skill can name other skills with :name in its text. The skills it names,
and the skills those name in turn, come back with it in a referencedSkills
array of name, directory, and content, in the order they are named. A
referenced skill that was already delivered as a <skill> message, or loaded
earlier in this turn, is left out, so an empty or missing array means there
was nothing new to load.
An unknown name is reported back with the currently effective skill names
so you can correct it.`

const useSkillSchema = `{
  "type": "object",
  "properties": {
    "name": {
      "type": "string",
      "description": "One of the effective skill names from the catalogue."
    }
  },
  "required": ["name"],
  "additionalProperties": false
}`
