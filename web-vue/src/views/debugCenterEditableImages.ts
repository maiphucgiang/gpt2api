import { computed, readonly, ref, shallowRef } from 'vue'

export const EDITABLE_REFERENCE_IMAGE_MAX_COUNT = 7
export const EDITABLE_REFERENCE_IMAGE_MAX_BYTES = 16 * 1024 * 1024
export const EDITABLE_REFERENCE_IMAGE_ACCEPT = 'image/*'

export type EditableReferenceImage = {
  id: string
  name: string
  size: number
  file: File
}

type EditableReferenceFileInfo = {
  name: string
  size: number
  type: string
}

let nextEditableImageId = 0

export function validateEditableReferenceFiles(
  currentCount: number,
  incoming: EditableReferenceFileInfo[],
): string {
  if (incoming.length === 0) {
    return currentCount > 0 ? '' : '请选择至少一张参考图'
  }
  if (currentCount + incoming.length > EDITABLE_REFERENCE_IMAGE_MAX_COUNT) {
    return `最多选择 ${EDITABLE_REFERENCE_IMAGE_MAX_COUNT} 张参考图`
  }
  for (const file of incoming) {
    if (file.type && !file.type.startsWith('image/')) {
      return `${file.name} 不是图片`
    }
    if (file.size <= 0 || file.size > EDITABLE_REFERENCE_IMAGE_MAX_BYTES) {
      return `${file.name} 超过 16MB 限制`
    }
  }
  return ''
}

export async function readEditableReferenceAsDataURL(file: File): Promise<string> {
  if (file.type && !file.type.startsWith('image/')) {
    throw new Error(`${file.name} 不是图片`)
  }
  const bytes = new Uint8Array(await file.arrayBuffer())
  let binary = ''
  const chunk = 0x8000
  for (let offset = 0; offset < bytes.length; offset += chunk) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + chunk))
  }
  const mime = file.type.startsWith('image/') ? file.type : 'image/png'
  return `data:${mime};base64,${btoa(binary)}`
}

export function useEditableReferenceImages() {
  const files = ref<EditableReferenceImage[]>([])
  const error = shallowRef('')
  const hasFiles = computed(() => files.value.length > 0)

  function addFiles(incoming: File[]) {
    const nextError = validateEditableReferenceFiles(files.value.length, incoming)
    if (nextError) {
      error.value = nextError
      return
    }
    error.value = ''
    files.value = [
      ...files.value,
      ...incoming.map(file => ({
        id: `ref-${++nextEditableImageId}`,
        name: file.name,
        size: file.size,
        file,
      })),
    ]
  }

  function removeFile(id: string) {
    files.value = files.value.filter(item => item.id !== id)
    error.value = ''
  }

  function clear() {
    files.value = []
    error.value = ''
  }

  async function toBase64Images() {
    if (files.value.length === 0) {
      throw new Error('请选择至少一张参考图')
    }
    return Promise.all(files.value.map(item => readEditableReferenceAsDataURL(item.file)))
  }

  return {
    files: readonly(files),
    error,
    hasFiles,
    addFiles,
    removeFile,
    clear,
    toBase64Images,
    accept: EDITABLE_REFERENCE_IMAGE_ACCEPT,
    maxCount: EDITABLE_REFERENCE_IMAGE_MAX_COUNT,
  }
}
