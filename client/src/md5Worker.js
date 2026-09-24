// Incremental MD5 in a worker so hashing a multi-GB video never blocks the UI thread.
// Request:  { file: File|Blob, start?: number, end?: number }
// Replies:  { type: 'progress', loaded, total } … then { type: 'done', md5 } or { type: 'error', message }
import SparkMD5 from 'spark-md5'

const SLICE_BYTES = 2 * 1024 * 1024

self.onmessage = async ({ data }) => {
  const { file } = data
  const start = data.start ?? 0
  const end = Math.min(data.end ?? file.size, file.size)
  const spark = new SparkMD5.ArrayBuffer()
  try {
    let lastReport = 0
    for (let offset = start; offset < end; offset += SLICE_BYTES) {
      const buffer = await file.slice(offset, Math.min(offset + SLICE_BYTES, end)).arrayBuffer()
      spark.append(buffer)
      const loaded = Math.min(offset + SLICE_BYTES, end) - start
      const now = Date.now()
      if (now - lastReport > 120) {
        self.postMessage({ type: 'progress', loaded, total: end - start })
        lastReport = now
      }
    }
    self.postMessage({ type: 'done', md5: spark.end() })
  } catch (error) {
    self.postMessage({ type: 'error', message: error?.message || '文件读取失败' })
  }
}
