export type ReleaseInfo = {
  version: string
  date: string
  items: { type: string; content: string }[]
}

export function parseChangelog(content: string): ReleaseInfo[] {
  return content
    .split(/^## /m)
    .slice(1)
    .map((block) => {
      const [title = '', ...lines] = block.trim().split('\n')
      const [, version = title.trim(), date = ''] = title.match(/^(.+?)(?:\s+-\s+(.+))?$/) || []
      return {
        version: version.trim(),
        date: date.trim(),
        items: parseReleaseItems(lines),
      }
    })
    .filter((release) => release.items.length)
}

function parseReleaseItems(lines: string[]): { type: string; content: string }[] {
  const items: { type: string; content: string }[] = []
  let groupType = ''

  for (const raw of lines) {
    const line = raw.trim()
    if (!line) continue

    const group = line.match(/^###\s+(.+)$/)
    if (group) {
      groupType = group[1].trim()
      continue
    }

    const typed = line.match(/^[+-]\s+\[(.+?)]\s+(.+)$/)
    if (typed) {
      items.push({ type: typed[1].trim(), content: typed[2].trim() })
      continue
    }

    const bullet = line.match(/^-\s+(.+)$/)
    if (bullet) {
      items.push({ type: groupType || '变更', content: bullet[1].trim() })
    }
  }

  return items
}

export function normalizeVersionTag(value: string): string {
  const clean = value.trim()
  if (!clean) return ''
  return clean.startsWith('v') ? clean : `v${clean}`
}

function versionParts(value: string) {
  const match = value.trim().match(/^v?(\d+)\.(\d+)\.(\d+)/)
  return match ? match.slice(1).map(Number) : null
}

export function isNewerVersion(latestVersion: string, currentVersion: string): boolean {
  const latest = versionParts(latestVersion)
  const current = versionParts(currentVersion)
  if (!latest || !current) return false
  for (let index = 0; index < latest.length; index += 1) {
    if (latest[index] > current[index]) return true
    if (latest[index] < current[index]) return false
  }
  return false
}
