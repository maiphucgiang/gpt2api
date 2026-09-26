import { describe, expect, it } from 'vitest'
import type { Settings } from '@/types/api'
import { resolveChatModels, resolveImageModels } from './modelCatalog'

describe('BPS model catalog', () => {
  it('keeps enabled BPS IDs from the server distinct from Web slugs', () => {
    const models = ['bsg/gpt-6-astra', 'bsg/gpt-5.6-sol', 'bsg/gpt-5.6-luna', 'bsg/gpt-5.6-terra', 'gpt-5-6-sol']
    const settings = { model_catalog: { chat_models: models } } as Settings
    expect(resolveChatModels(settings)).toEqual(models)
    expect(resolveImageModels(settings)).not.toContain('bsg/gpt-6-astra')
  })

  it('does not advertise BPS when no server catalog is available', () => {
    expect(resolveChatModels(null)).not.toContain('bsg/gpt-6-astra')
    expect(resolveChatModels(null)).not.toContain('bsg/gpt-5.6-sol')
    expect(resolveChatModels(null)).toContain('gpt-5-6-sol')
  })
})
