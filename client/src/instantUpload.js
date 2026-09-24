import { apiRequest } from './api'

/**
 * 秒传：先算整文件 MD5 问服务端“有没有这份内容”，有的话再回答一道挑战——
 * 服务端随机指定一个字节区间，要求给出这段的 MD5。只知道整文件 MD5（比如泄露了）
 * 而没有文件本身的人答不出来，因此不能借秒传把别人的视频挂到自己名下。
 */

const hashingSupported = () => typeof Worker !== 'undefined'

function runHashWorker(message, onProgress, signal) {
  return new Promise((resolve, reject) => {
    const worker = new Worker(new URL('./md5Worker.js', import.meta.url), { type: 'module' })
    const stop = () => worker.terminate()
    const onAbort = () => {
      stop()
      reject(Object.assign(new Error('上传已取消'), { aborted: true }))
    }
    signal?.addEventListener('abort', onAbort, { once: true })
    worker.onmessage = ({ data }) => {
      if (data.type === 'progress') {
        onProgress?.(data.loaded, data.total)
        return
      }
      signal?.removeEventListener('abort', onAbort)
      stop()
      if (data.type === 'done') resolve(data.md5)
      else reject(new Error(data.message))
    }
    worker.onerror = event => {
      signal?.removeEventListener('abort', onAbort)
      stop()
      reject(new Error(event.message || '文件指纹计算失败'))
    }
    worker.postMessage(message)
  })
}

export function hashFile(file, onProgress, signal) {
  return runHashWorker({ file }, onProgress, signal)
}

function hashRange(file, offset, length, signal) {
  return runHashWorker({ file, start: offset, end: offset + length }, null, signal)
}

async function postJson(path, body, signal) {
  const response = await apiRequest(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal
  })
  if (!response.ok) throw new Error((await response.text()) || `HTTP ${response.status}`)
  return response.json()
}

/**
 * 尝试秒传。返回新建的媒体记录；服务端没有这份内容、浏览器不支持 Worker
 * 或任何一步失败时返回 null，由调用方退回普通分片上传——秒传只是加速，不能成为失败点。
 */
export async function tryInstantUpload(file, { onHashProgress, signal } = {}) {
  if (!hashingSupported()) return null
  try {
    const md5 = await hashFile(file, onHashProgress, signal)
    const challenge = await postJson('/media/instant-upload/challenge',
      { md5, size: file.size, filename: file.name }, signal)
    if (!challenge) return null
    const rangeMd5 = await hashRange(file, challenge.offset, challenge.length, signal)
    return await postJson('/media/instant-upload', { challengeId: challenge.challengeId, rangeMd5 }, signal)
  } catch (error) {
    if (error?.aborted || signal?.aborted) throw error
    console.warn('instant upload skipped', error)
    return null
  }
}
