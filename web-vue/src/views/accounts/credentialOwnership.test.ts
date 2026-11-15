import { describe, expect, it } from 'vitest'
import type { Account } from '@/api/accounts'
import { accountRowSignature, accountSourceText } from './viewUtils'

describe('credential ownership labels', () => {
  const account = { id: 'stable', type: 'plus', source_type: 'codex', enabled: true } as Account

  it('distinguishes source ownership from plan and protocol', () => {
    expect(accountSourceText({ ...account, credential_owner: 'cpa' })).toBe('plus / CPA 托管')
    expect(accountSourceText({ ...account, credential_owner: 'sub2api' })).toBe('plus / Sub2API 托管')
    expect(accountSourceText({ ...account, credential_owner: 'external_unbound' })).toBe('plus / 外部来源待绑定')
    expect(accountSourceText({ ...account, credential_owner: 'local', source_type: 'oauth_login' })).toBe('plus / oauth_login')
  })

  it('invalidates the row cache when synchronization becomes pending', () => {
    const ready = { ...account, credential_owner: 'cpa', credential_sync_status: 'ready' }
    const pending = { ...ready, credential_sync_status: 'pending' }
    expect(accountSourceText(pending)).toContain('等待渠道更新')
    expect(accountRowSignature(ready)).not.toBe(accountRowSignature(pending))
    expect(accountRowSignature(ready)).not.toBe(accountRowSignature({ ...ready, bps_subscription_eligible: true }))
  })
})
