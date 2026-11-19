package agent

// useSkill loads one effective skill's SKILL.md content in full. Agent
// Skills progressive disclosure means the system prompt only ever carries
// the bounded catalogue of names and descriptions; this tool is the one way
// the model pays for a skill's full body, and only for skills it activates.

//nolint:lll // Model-facing paragraphs stay on one line.
const useSkillDescription = `Load one skill's complete instructions by name, one of the names in the <skills> block. The result carries the skill's full SKILL.md, the absolute path of its own directory, and a content hash. An unknown name is answered with the names that exist, so you can correct it. Calling it again for the same name in the same turn returns the same content.

The skill's directory may hold files its SKILL.md mentions, commonly under references/ and scripts/. They are not loaded with it: read them with readFile once you know they exist. To run a bundled script, pass that directory as runCommand's directory so the script finds its neighbours. Skills in different layers can share a name, and the directory is always the one of the skill that won.

A skill can name other skills with :name in its text. Those, and the skills they name in turn, come back with it in a referencedSkills array of name, directory, and content, in the order they are named. A skill already delivered as a <skill> message or loaded earlier in this turn is left out, so an empty or missing array means nothing new was needed.`

//nolint:lll // Parameter descriptions stay on one line.
const useSkillSchema = `{
  "type": "object",
  "properties": {
    "name": {
      "type": "string",
      "description": "A skill name from the <skills> block, such as core:planning."
    }
  },
  "required": ["name"],
  "additionalProperties": false
}`
