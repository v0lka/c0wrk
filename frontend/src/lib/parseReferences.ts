// Skill reference extraction from user message text.

const SKILL_REF_PATTERN = /(?:^|\s)\/([\w-]+)/g

/**
 * Extract skill names referenced via /skill-name patterns in the user message.
 * Only matches when preceded by whitespace or start-of-string.
 */
export function extractSkillRefs(text: string): string[] {
  const skills: string[] = []
  const seen = new Set<string>()
  let match: RegExpExecArray | null

  SKILL_REF_PATTERN.lastIndex = 0
  while ((match = SKILL_REF_PATTERN.exec(text)) !== null) {
    const name = match[1]
    if (name === undefined) continue
    if (!seen.has(name)) {
      seen.add(name)
      skills.push(name)
    }
  }
  return skills
}
