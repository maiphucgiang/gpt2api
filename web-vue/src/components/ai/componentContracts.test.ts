// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import DateRangeInputs from './DateRangeInputs.vue'
import GroupedSelectMenu from '@/components/ui/GroupedSelectMenu.vue'
import LogsSystemTable from '@/views/logs/LogsSystemTable.vue'
import RegisterProviderCard from '@/views/register/RegisterProviderCard.vue'
import { normalizeSystemLogRow } from '@/api/logs'
import { prepareSettingsForEdit } from '@/api/settings'

enableAutoUnmount(afterEach)

describe('typed component contracts', () => {
  it('emits native date input changes and clearing', async () => {
    const wrapper = mount(DateRangeInputs, { props: { start: '2026-09-01', end: '2026-09-22' } })
    await wrapper.get('input[aria-label="开始日期"]').setValue('2026-09-02')
    await wrapper.get('input[aria-label="结束日期"]').setValue('')
    expect(wrapper.emitted('update:start')).toEqual([['2026-09-02']])
    expect(wrapper.emitted('update:end')).toEqual([['']])
  })

  it('accepts immutable select options and emits a single selection', async () => {
    const options = Object.freeze([{ label: 'One', value: 'a' }, { label: 'Two', value: 'b' }])
    const wrapper = mount(GroupedSelectMenu, {
      props: { modelValue: 'a', options },
      global: { stubs: { teleport: true } },
    })
    await wrapper.get('.grouped-select-trigger').trigger('click')
    const option = wrapper.findAll('.grouped-select-option').find(item => item.text() === 'Two')
    expect(option).toBeDefined()
    await option!.trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([['b']])
    expect(Object.isFrozen(options)).toBe(true)
  })

  it('forwards both the failed image event and URL through the log table', async () => {
    const row = normalizeSystemLogRow({ id: 'fixture', type: 'image', summary: 'fixture' }, 0)
    row.imageUrls = ['https://example.invalid/result.png']
    const wrapper = mount(LogsSystemTable, {
      props: {
        visibleLogs: [row], logs: [row], isFetching: false, logsLoadError: '',
        allVisibleLogsSelected: false, page: 1, pageSize: 20, totalCount: 1,
        isLogSelected: () => false, isPreviewBroken: () => false,
      },
      global: { stubs: { ListPagination: true } },
    })
    await wrapper.get('img').trigger('error')
    const forwarded = wrapper.emitted('image-error')?.[0]
    expect(forwarded?.[0]).toBeInstanceOf(Event)
    expect(forwarded?.[1]).toBe(row.imageUrls[0])
  })

  it('writes mail.com proxy lines through the provider update event', async () => {
    const wrapper = mount(RegisterProviderCard, {
      props: {
        provider: { id: 'mail', type: 'mailcom_mother', enable: true, proxies: ['http://old.example:8080'] },
        index: 0, providerCount: 1, disabled: false, saving: false, outlookPoolActionItems: [],
        gptMail: {
          statusByIndex: () => null, statusBusy: () => false, statusTone: () => 'muted',
          statusTitle: () => '', remainingText: () => '', resetText: () => '', statusHint: () => '',
        },
      },
    })
    const input = wrapper.get('textarea[placeholder^="http://privoxy"]')
    expect((input.element as HTMLTextAreaElement).value).toBe('http://old.example:8080')
    await input.setValue(' http://first.example:8080 \n\n socks5://second.example:1080 ')
    expect(wrapper.emitted('update-field')?.at(-1)).toEqual([
      0, 'proxies', ['http://first.example:8080', 'socks5://second.example:1080'],
    ])
  })

  it('normalizes optional server settings before binding checkbox and backup controls', () => {
    const settings = prepareSettingsForEdit({})
    expect(settings.backup.interval_minutes).toBe(1440)
    expect(settings.auto_remove_invalid_accounts).toBe(false)
    expect(settings.auto_remove_rate_limited_accounts).toBe(false)
    expect(settings.image_error_friendly_enabled).toBe(false)
    expect(settings.image_remove_conversation_after_result).toBe(false)
    expect(settings.image_settle_enabled).toBe(true)
  })
})
