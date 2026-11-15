// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick } from 'vue'
import DebugCenter from './DebugCenter.vue'
import { debugApi } from '@/api/debug'

vi.mock('@/api/debug', () => ({
  debugApi: {
    search: vi.fn(),
    chat: vi.fn(),
    createEditableFileTask: vi.fn(),
    listEditableFileTasks: vi.fn(async () => ({ items: [] })),
  },
}))

vi.mock('@/api/models', () => ({
  modelsApi: {
    catalog: vi.fn(async () => ({ chat_models: ['auto'], image_models: [], image_edit_models: [], video_models: [] })),
    list: vi.fn(async () => ({ data: [] })),
  },
}))

const tinyPng = Uint8Array.from([
  0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
  0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
  0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
  0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
  0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
  0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
])

let wrapper: VueWrapper | undefined

function submitButton() {
  return wrapper!.findAll('button').find(button => button.text().includes('提交任务'))
}

async function openPptTab() {
  const tab = wrapper!.findAll('button').find(button => button.text() === 'PPT 生成')
  await tab!.trigger('click')
  await flushPromises()
  await nextTick()
}

async function selectReferenceFile(name = 'ref.png') {
  const input = wrapper!.get('input[type="file"]')
  const file = new File([tinyPng], name, { type: 'image/png' })
  Object.defineProperty(input.element, 'files', {
    configurable: true,
    value: [file],
  })
  await input.trigger('change')
  await flushPromises()
  await nextTick()
}

beforeEach(() => {
  setActivePinia(createPinia())
  vi.mocked(debugApi.listEditableFileTasks).mockResolvedValue({ items: [] })
  vi.mocked(debugApi.createEditableFileTask).mockResolvedValue({ id: 'task-1', taskId: 'task-1', status: 'queued', kind: 'ppt' })
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  vi.clearAllMocks()
})

describe('debug center editable uploads', () => {
  it('requires local image files, allows remove, and never shows base64 text', async () => {
    wrapper = mount(DebugCenter, { global: { plugins: [createPinia()] } })
    await openPptTab()

    expect(wrapper.find('textarea[placeholder*="base64"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('参考图（必填）')
    expect(wrapper.get('input[type="file"]').attributes('accept')).toBe('image/*')
    expect(wrapper.get('input[type="file"]').attributes('multiple')).toBeDefined()
    expect(wrapper.html()).not.toMatch(/base64/i)
    expect(submitButton()?.attributes('disabled')).toBeDefined()

    await selectReferenceFile('cover.png')
    expect(wrapper.text()).toContain('cover.png')
    expect(wrapper.html()).not.toMatch(/data:image/i)
    expect(wrapper.html()).not.toMatch(/base64/i)
    expect(submitButton()?.attributes('disabled')).toBeUndefined()

    await wrapper.findAll('button').find(button => button.text() === '移除')!.trigger('click')
    await nextTick()
    expect(wrapper.text()).not.toContain('cover.png')
    expect(submitButton()?.attributes('disabled')).toBeDefined()
    expect(debugApi.createEditableFileTask).not.toHaveBeenCalled()
  })

  it('converts selected files internally when submitting', async () => {
    wrapper = mount(DebugCenter, { global: { plugins: [createPinia()] } })
    await openPptTab()
    await selectReferenceFile('slide.png')
    await submitButton()!.trigger('click')
    await flushPromises()
    await nextTick()
    expect(debugApi.createEditableFileTask).toHaveBeenCalledTimes(1)
    const payload = vi.mocked(debugApi.createEditableFileTask).mock.calls[0]
    expect(payload[0]).toBe('ppt')
    expect(payload[1].prompt).toContain('PPT')
    expect(payload[1].base64_images?.[0]).toMatch(/^data:image\/png;base64,/)
    expect(wrapper.html()).not.toMatch(/data:image\/png;base64,/)
  })
})
