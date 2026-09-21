export function parseCPAAccounts(text: string, label: string): Record<string, unknown>[] {
  const value: unknown = JSON.parse(text)
  const isRecord = (item: unknown): item is Record<string, unknown> =>
    Boolean(item && typeof item === 'object' && !Array.isArray(item))
  let candidates: unknown[] = []
  if (Array.isArray(value)) candidates = value
  else if (isRecord(value)) {
    if ('access_token' in value || 'accessToken' in value || 'type' in value) candidates = [value]
    else candidates = ['accounts', 'items', 'data', 'results'].flatMap(key => Array.isArray(value[key]) ? value[key] : [])
  }
  const accounts = candidates.filter(isRecord)
  if (!accounts.length) throw new Error(`${label} 中没有账号记录`)
  return accounts
}

export async function readCPAFiles(files: readonly Pick<File, 'name' | 'text'>[]) {
  const accounts: Record<string, unknown>[] = []
  let invalidFiles = 0
  for (const file of files) {
    try {
      accounts.push(...parseCPAAccounts(await file.text(), file.name))
    } catch {
      invalidFiles += 1
    }
  }
  return { accounts, invalidFiles }
}
