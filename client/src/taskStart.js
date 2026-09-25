// The panel's "waited mm:ss" counts from when a task was first submitted. Remembering that moment per task
// means closing the panel, reopening it or reloading the page does not restart the clock.
const TASK_START_KEY = 'vparser:taskStarted'
const TASK_START_TTL = 6 * 60 * 60 * 1000   // same as the server's active-task marker

export function taskStartKey(id, type, scope) {
  return `${id}|${type}|${scope || ''}`
}

function readTaskStarts() {
  try {
    const all = JSON.parse(localStorage.getItem(TASK_START_KEY) || '{}')
    return all && typeof all === 'object' ? all : {}
  } catch {
    return {}
  }
}

function writeTaskStarts(all) {
  try { localStorage.setItem(TASK_START_KEY, JSON.stringify(all)) } catch { /* private mode: the timer just restarts */ }
}

/** Start time of a task: the remembered one if it is still fresh, otherwise `at` (now), which is then remembered.
 *  `restart` overwrites it, for a task that was just submitted again. */
export function rememberTaskStart(key, at = Date.now(), restart = false) {
  const now = Date.now()
  const all = readTaskStarts()
  for (const [k, v] of Object.entries(all)) if (!(now - v < TASK_START_TTL)) delete all[k]
  if (restart || !all[key]) all[key] = at
  writeTaskStarts(all)
  return all[key]
}

export function forgetTaskStart(key) {
  const all = readTaskStarts()
  if (!(key in all)) return
  delete all[key]
  writeTaskStarts(all)
}
