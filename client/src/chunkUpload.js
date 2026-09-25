import { apiRequest } from './api'
import { tryInstantUpload } from './instantUpload'
import { t } from './i18n'

const CHUNK_SIZE = 5 * 1024 * 1024
const UPLOAD_CONCURRENCY = 3
const MAX_TOTAL_CHUNKS = 410
const CHUNK_MAX_ATTEMPTS = 4
const CHUNK_RETRY_BASE_MS = 800
const CHUNK_RETRY_CEILING_MS = 8_000
const SPEED_WINDOW_MS = 8_000
const SPEED_MIN_SAMPLE_MS = 1_500

export const MAX_UPLOAD_BYTES = MAX_TOTAL_CHUNKS * CHUNK_SIZE

/** 用户主动取消上传时抛出，调用方据此区分“取消”与“失败”。 */
export class UploadAbortedError extends Error {
  constructor(message = t('chunk.cancelled')) {
    super(message)
    this.name = 'UploadAbortedError'
    this.aborted = true
  }
}

export function formatBytes(bytes) {
  const value = Number(bytes) || 0
  if (value < 1024) return `${value} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let scaled = value / 1024
  let unitIndex = 0
  while (scaled >= 1024 && unitIndex < units.length - 1) {
    scaled /= 1024
    unitIndex += 1
  }
  return `${scaled >= 10 ? Math.round(scaled) : scaled.toFixed(1)} ${units[unitIndex]}`
}

export function formatDurationText(seconds) {
  if (!Number.isFinite(seconds) || seconds <= 0) return ''
  const total = Math.round(seconds)
  if (total < 60) return t('time.s', { s: total })
  const minutes = Math.floor(total / 60)
  if (minutes < 60) return t('time.ms', { m: minutes, s: String(total % 60).padStart(2, '0') })
  return t('time.hm', { h: Math.floor(minutes / 60), m: String(minutes % 60).padStart(2, '0') })
}

/** 选择文件时的前置校验，避免进入上传态之后才失败。 */
export function validateVideoFile(file) {
  if (!file) return t('file.none')
  if (!file.size) return t('file.empty')
  if (file.size > MAX_UPLOAD_BYTES) {
    return t('file.tooBig', { size: formatBytes(file.size), max: formatBytes(MAX_UPLOAD_BYTES) })
  }
  return ''
}

function storageKey(file) {
  return `upload:${file.name}:${file.size}:${file.lastModified}`
}

function readStoredUploadId(file) {
  try {
    return localStorage.getItem(storageKey(file))
  } catch {
    // 隐私模式下 localStorage 可能不可用，此时退化为普通上传。
    return null
  }
}

function writeStoredUploadId(file, uploadId) {
  try {
    localStorage.setItem(storageKey(file), uploadId)
  } catch {
    // 存不下续传凭据不影响本次上传，只是失败后无法续传。
  }
}

export function forgetUploadProgress(file) {
  if (!file) return
  try {
    localStorage.removeItem(storageKey(file))
  } catch {
    // 同上，忽略存储不可用。
  }
}

export function hasUploadProgress(file) {
  return Boolean(file && readStoredUploadId(file))
}

/**
 * 分片上传 + 断点续传。
 * - 单个分片失败按指数退避重试，不再让整次上传直接失败。
 * - 支持 AbortSignal 取消；取消后保留续传凭据，便于用户继续。
 * - onProgress 携带字节级进度、实时速度与预计剩余时间。
 */
export async function uploadVideoInChunks(file, onProgress = () => {}, signal) {
  const invalid = validateVideoFile(file)
  if (invalid) throw new Error(invalid)
  throwIfAborted(signal)

  const totalBytes = file.size
  const totalChunks = Math.ceil(totalBytes / CHUNK_SIZE)

  // 没有续传凭据时先试秒传：算指纹 → 服务端有同内容 → 回答区间挑战 → 直接得到媒体记录。
  if (!readStoredUploadId(file)) {
    const instant = await tryInstantUpload(file, {
      signal,
      onHashProgress: (loaded, total) => onProgress({
        phase: 'hashing', uploadedBytes: loaded, totalBytes: total,
        percent: total ? Math.round((loaded / total) * 100) : 0,
        completedChunks: 0, totalChunks, resumedChunks: 0, resumedBytes: 0,
        bytesPerSecond: null, etaSeconds: null, retryingCount: 0, retryAttempt: 0,
        retryMaxAttempts: CHUNK_MAX_ATTEMPTS
      })
    })
    if (instant) return { ...instant, instant: true }
  }
  throwIfAborted(signal)

  const { uploadId, uploadedChunks } = await resolveUploadSession(file, totalChunks, signal)

  const pendingChunks = []
  let uploadedBytes = 0
  for (let index = 0; index < totalChunks; index += 1) {
    if (uploadedChunks.has(index)) uploadedBytes += chunkSize(file, index)
    else pendingChunks.push(index)
  }

  const resumedBytes = uploadedBytes
  const resumedChunks = totalChunks - pendingChunks.length
  const meter = createSpeedMeter()
  const retrying = new Map()
  let completedChunks = resumedChunks

  const emit = phase => {
    const transferred = uploadedBytes - resumedBytes
    const speed = meter.speed(transferred)
    const remainingBytes = Math.max(0, totalBytes - uploadedBytes)
    const attempts = [...retrying.values()]
    onProgress({
      phase,
      completedChunks,
      totalChunks,
      uploadedBytes,
      totalBytes,
      resumedBytes,
      resumedChunks,
      percent: totalBytes
        ? Math.min(100, Math.round((uploadedBytes / totalBytes) * 100))
        : 0,
      bytesPerSecond: speed,
      etaSeconds: speed && remainingBytes ? remainingBytes / speed : null,
      retryingCount: attempts.length,
      retryAttempt: attempts.length ? Math.max(...attempts) : 0,
      retryMaxAttempts: CHUNK_MAX_ATTEMPTS
    })
  }

  emit(resumedChunks ? 'resuming' : 'uploading')

  let cursor = 0
  let fatalError = null
  const worker = async () => {
    while (!fatalError && cursor < pendingChunks.length) {
      if (signal?.aborted) {
        fatalError = new UploadAbortedError()
        return
      }
      const index = pendingChunks[cursor++]
      try {
        await uploadChunkWithRetry(file, uploadId, index, totalChunks, signal, attempt => {
          retrying.set(index, attempt)
          emit('uploading')
        })
        retrying.delete(index)
        uploadedBytes += chunkSize(file, index)
        completedChunks += 1
        meter.record(uploadedBytes - resumedBytes)
        emit('uploading')
      } catch (error) {
        retrying.delete(index)
        fatalError = error
      }
    }
  }

  const workerCount = pendingChunks.length
    ? Math.min(UPLOAD_CONCURRENCY, pendingChunks.length)
    : 0
  await Promise.all(Array.from({ length: workerCount }, worker))
  if (fatalError) throw fatalError
  throwIfAborted(signal)

  emit('merging')
  const params = new URLSearchParams({ uploadId })
  const response = await apiRequest(`/media/complete-upload?${params}`, {
    method: 'POST',
    signal
  })
  if (!response.ok) {
    throwIfAborted(signal)
    throw new Error(await readErrorText(response) || t('chunk.mergeFailed'))
  }
  const media = await response.json()
  forgetUploadProgress(file)
  return media
}

/**
 * 只有服务端明确表示凭据不存在、已过期或格式损坏时，才丢弃本地续传进度。
 * 后端这两种情况都以 400 返回（见 server-go internal/upload 的 requireUpload / validateUploadID）：
 *   "uploadId does not exist or has expired" / "invalid uploadId"
 */
function isDeadUploadSession(status, detail) {
  if (status !== 400) return false
  const text = (detail || '').toLowerCase()
  return text.includes('does not exist')
    || text.includes('has expired')
    || text.includes('invalid uploadid')
}

async function resolveUploadSession(file, totalChunks, signal) {
  const storedUploadId = readStoredUploadId(file)
  if (storedUploadId) {
    let response
    try {
      const params = new URLSearchParams({ uploadId: storedUploadId })
      response = await apiRequest(`/media/upload-status?${params}`, { signal })
    } catch (error) {
      if (isAbortError(error, signal)) throw new UploadAbortedError()
      // 断网/超时意味着“进度未知”，而不是“进度失效”。此时必须保留凭据并中断本次上传，
      // 否则用户恢复网络后会被迫从 0 重传，之前传完的分片全部作废。
      throw new Error(t('chunk.statusOffline'))
    }

    if (response.ok) {
      const indexes = await response.json()
      const uploadedChunks = new Set((Array.isArray(indexes) ? indexes : [])
        .map(Number)
        .filter(index => Number.isInteger(index) && index >= 0 && index < totalChunks))
      return { uploadId: storedUploadId, uploadedChunks }
    }

    const detail = await readErrorText(response)
    if (!isDeadUploadSession(response.status, detail)) {
      // 401 / 403 / 429 / 5xx：凭据本身可能仍然有效，一律保留，交由用户稍后重试。
      const error = new Error(detail
        || t('chunk.statusHttp', { status: response.status }))
      error.status = response.status
      throw error
    }
    forgetUploadProgress(file)
  }

  const uploadId = await initializeUpload(file.name, totalChunks, signal)
  writeStoredUploadId(file, uploadId)
  return { uploadId, uploadedChunks: new Set() }
}

async function initializeUpload(filename, totalChunks, signal) {
  const params = new URLSearchParams({ filename, totalChunks: String(totalChunks) })
  const response = await apiRequest(`/media/init-upload?${params}`, {
    method: 'POST',
    signal
  })
  const body = (await response.text()).trim()
  if (!response.ok) throw new Error(body || t('chunk.initFailed'))
  return body
}

async function uploadChunkWithRetry(file, uploadId, chunkIndex, totalChunks, signal, onRetry) {
  let lastError = null
  for (let attempt = 1; attempt <= CHUNK_MAX_ATTEMPTS; attempt += 1) {
    throwIfAborted(signal)
    try {
      await uploadChunk(file, uploadId, chunkIndex, totalChunks, signal)
      return
    } catch (error) {
      if (isAbortError(error, signal)) throw new UploadAbortedError()
      lastError = error
      if (attempt === CHUNK_MAX_ATTEMPTS || !isRetriable(error)) break
      onRetry(attempt)
      await sleep(retryDelay(attempt), signal)
    }
  }
  throw new Error(
    t('chunk.failed', { n: chunkIndex + 1, total: totalChunks, error: lastError?.message || t('chunk.network') })
  )
}

async function uploadChunk(file, uploadId, chunkIndex, totalChunks, signal) {
  const [start, end] = chunkBounds(file, chunkIndex)
  const formData = new FormData()
  formData.append('uploadId', uploadId)
  formData.append('chunkIndex', String(chunkIndex))
  formData.append('totalChunks', String(totalChunks))
  formData.append('file', file.slice(start, end))

  const response = await apiRequest('/media/upload-chunk', {
    method: 'POST',
    body: formData,
    signal
  })
  if (response.ok) return
  const error = new Error(await readErrorText(response) || t('chunk.rejected'))
  error.status = response.status
  throw error
}

/** 只重试网络抖动与服务端临时故障；参数或鉴权错误重试没有意义。 */
function isRetriable(error) {
  const status = error?.status
  if (status === undefined) return true
  if (status === 408 || status === 429) return true
  return status >= 500
}

function retryDelay(attempt) {
  const base = Math.min(CHUNK_RETRY_CEILING_MS, CHUNK_RETRY_BASE_MS * 2 ** (attempt - 1))
  return Math.round(base * (0.75 + Math.random() * 0.5))
}

function chunkBounds(file, chunkIndex) {
  return [
    chunkIndex * CHUNK_SIZE,
    Math.min(file.size, (chunkIndex + 1) * CHUNK_SIZE)
  ]
}

function chunkSize(file, chunkIndex) {
  const [start, end] = chunkBounds(file, chunkIndex)
  return end - start
}

/** 滑动窗口测速，避免用整段平均值把弱网下的瞬时速度算得过于乐观。 */
function createSpeedMeter() {
  const samples = [{ at: performance.now(), bytes: 0 }]
  return {
    record(bytes) {
      const at = performance.now()
      samples.push({ at, bytes })
      while (samples.length > 2 && at - samples[0].at > SPEED_WINDOW_MS) samples.shift()
    },
    speed(currentBytes) {
      const first = samples[0]
      const elapsed = performance.now() - first.at
      if (elapsed < SPEED_MIN_SAMPLE_MS) return null
      const bytes = currentBytes - first.bytes
      if (bytes <= 0) return null
      return bytes / (elapsed / 1000)
    }
  }
}

async function readErrorText(response) {
  try {
    return (await response.text()).trim()
  } catch {
    return ''
  }
}

function throwIfAborted(signal) {
  if (signal?.aborted) throw new UploadAbortedError()
}

function isAbortError(error, signal) {
  return Boolean(signal?.aborted) || error?.name === 'AbortError' || error?.aborted === true
}

function sleep(ms, signal) {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new UploadAbortedError())
      return
    }
    const cleanup = () => signal?.removeEventListener('abort', onAbort)
    const onAbort = () => {
      clearTimeout(timer)
      cleanup()
      reject(new UploadAbortedError())
    }
    const timer = setTimeout(() => {
      cleanup()
      resolve()
    }, ms)
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}
