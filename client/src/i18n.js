// 界面文案的中 / 英 / 瑞典语三份翻译。默认中文（与原来一致），?lang=en|sv 或页头切换后记住选择。
// 不引入 vue-i18n：文案量小，一个响应式的 locale 加一个 t() 就够了。
import { ref, watch } from 'vue'

const STORAGE_KEY = 'vparser:lang'
const HTML_LANG = { zh: 'zh-CN', en: 'en', sv: 'sv' }
export const LOCALES = [
  { value: 'zh', label: '中' },
  { value: 'en', label: 'EN' },
  { value: 'sv', label: 'SV' }
]

/** Chinese and Swedish browsers get their own language, every other browser gets English. */
export function browserLocale(languages) {
  for (const tag of languages || []) {
    const l = String(tag).toLowerCase()
    if (l.startsWith('zh')) return 'zh'
    if (l.startsWith('sv')) return 'sv'
    if (l.startsWith('en')) return 'en'
  }
  return 'en'
}

/** ?lang= wins, then the visitor's own earlier choice, then the browser's languages. */
function initialLocale() {
  try {
    const wanted = new URLSearchParams(window.location.search).get('lang') || localStorage.getItem(STORAGE_KEY)
    if (wanted && HTML_LANG[wanted]) return wanted
    return browserLocale(navigator.languages || [navigator.language])
  } catch {
    // Outside the browser (unit tests) or with storage disabled: fall back to Chinese.
    return 'zh'
  }
}

export const locale = ref(initialLocale())

/** An explicit choice is remembered; until then the language keeps following the browser. */
export function setLocale(value) {
  if (!HTML_LANG[value]) return
  locale.value = value
  try { localStorage.setItem(STORAGE_KEY, value) } catch { /* private mode */ }
}

watch(locale, value => {
  if (typeof document !== 'undefined') document.documentElement.lang = HTML_LANG[value] || 'zh-CN'
}, { immediate: true })

/** BCP 47 tag for number and date formatting in the current language. */
export const localeTag = () => HTML_LANG[locale.value] || 'zh-CN'

/** Translate a key; {name} placeholders are filled from params. Missing translations fall back to Chinese. */
export function t(key, params) {
  const table = MESSAGES[locale.value] || MESSAGES.zh
  const text = table[key] ?? MESSAGES.zh[key] ?? key
  return params ? text.replace(/\{(\w+)\}/g, (_, name) => (params[name] ?? '')) : text
}

// [zh, en, sv]
const ROWS = {
  // header, auth
  'nav.login': ['登录 / 注册', 'Log in / Sign up', 'Logga in / Registrera'],
  'nav.logout': ['退出登录', 'Log out', 'Logga ut'],
  'nav.language': ['切换语言', 'Language', 'Språk'],
  'status.offline': ['网络已断开', 'Offline', 'Offline'],
  'status.uploading': ['上传 {percent}%', 'Uploading {percent}%', 'Laddar upp {percent} %'],
  'status.processing': ['处理中', 'Processing', 'Bearbetar'],
  'status.tasks': ['后台任务 {count}', '{count} background tasks', '{count} bakgrundsjobb'],
  'status.ready': ['系统就绪', 'Ready', 'Redo'],

  // hero
  'hero.title': ['把一段长视频，<br />变成可以追溯的笔记。', 'Turn a long video<br />into notes you can trace back.', 'Gör en lång video<br />till anteckningar du kan spåra.'],
  'hero.sub': ['上传课程或讲座视频，写下你想得到什么。Agent 会读懂语音和画面文字，给出每条结论对应的时间点。',
    'Upload a lecture or talk and say what you want from it. The agent reads the speech and the text on screen, and gives every conclusion the moment it comes from.',
    'Ladda upp en föreläsning och skriv vad du vill få ut av den. Agenten läser både talet och texten i bild och anger tidpunkten för varje slutsats.'],
  'hero.step1': ['上传视频', 'Upload a video', 'Ladda upp en video'],
  'hero.step1.sub': ['本地文件或在线链接', 'A file or a link', 'En fil eller en länk'],
  'hero.step2': ['写下目标', 'Say what you want', 'Skriv ditt mål'],
  'hero.step2.sub': ['复习笔记、观点审查、剪辑脚本……', 'Study notes, a critical review, a clip script…', 'Studieanteckningar, granskning, klippmanus…'],
  'hero.step3': ['核对结论', 'Check the conclusions', 'Kontrollera slutsatserna'],
  'hero.step3.sub': ['点时间点，直接跳到原画面', 'Click a timestamp to jump to the frame', 'Klicka på en tidpunkt för att hoppa dit'],

  // upload
  'upload.drop': ['松开即可上传', 'Drop to upload', 'Släpp för att ladda upp'],
  'upload.local': ['上传本地视频', 'Upload a video file', 'Ladda upp en videofil'],
  'upload.local.desc': ['点击选择，或把文件拖到这里。支持 mp4、mov、mkv、webm，大文件可断点续传。',
    'Click to choose, or drop a file here. mp4, mov, mkv and webm; large files resume where they stopped.',
    'Klicka för att välja eller släpp en fil här. mp4, mov, mkv och webm; stora filer fortsätter där de avbröts.'],
  'upload.url': ['从链接导入', 'Import from a link', 'Importera från länk'],
  'upload.url.desc': ['粘贴 B 站、YouTube 或抖音的视频地址。', 'Paste a Bilibili, YouTube or Douyin video URL.', 'Klistra in en länk från Bilibili, YouTube eller Douyin.'],
  'upload.url.aria': ['视频链接', 'Video link', 'Videolänk'],
  'upload.url.go': ['导入', 'Import', 'Importera'],
  'upload.progress.aria': ['视频上传进度', 'Upload progress', 'Uppladdningsförlopp'],
  'upload.cancel': ['取消上传', 'Cancel upload', 'Avbryt uppladdning'],
  'upload.resume': ['继续上传', 'Resume upload', 'Fortsätt ladda upp'],
  'upload.restart': ['重新开始', 'Start over', 'Börja om'],
  'upload.resume.done': ['已完成 {percent}%', '{percent}% done', '{percent} % klart'],
  'upload.resume.kept': ['已保留上传进度', 'progress saved', 'förloppet är sparat'],
  'upload.resume.hint': ['{name} {progress}，可继续未完成的上传', '{name}: {progress}. You can resume the upload.', '{name}: {progress}. Du kan fortsätta uppladdningen.'],
  'upload.prepare': ['准备上传', 'Preparing upload', 'Förbereder uppladdning'],
  'upload.checking': ['正在核对已上传分片', 'Checking uploaded chunks', 'Kontrollerar uppladdade delar'],
  'upload.preparing': ['准备分片上传', 'Preparing chunked upload', 'Förbereder uppladdning i delar'],
  'upload.hashing': ['正在计算文件指纹，检查是否可以秒传', 'Fingerprinting the file to see if it is already on the server', 'Beräknar filens fingeravtryck för att se om den redan finns'],
  'upload.merging': ['分片已全部送达，正在服务端合并', 'All chunks arrived, merging on the server', 'Alla delar har kommit fram, slås ihop på servern'],
  'upload.sending': ['正在安全上传', 'Uploading', 'Laddar upp'],
  'upload.chunks': ['分片 {done}/{total}', 'chunk {done}/{total}', 'del {done}/{total}'],
  'upload.eta': ['剩余约 {eta}', 'about {eta} left', 'cirka {eta} kvar'],
  'upload.retrying': ['网络不稳定，正在重试 {count} 个分片（第 {attempt}/{max} 次）', 'Unstable network, retrying {count} chunks (attempt {attempt}/{max})', 'Instabilt nätverk, försöker igen med {count} delar (försök {attempt}/{max})'],
  'upload.resumed': ['已续传：跳过 {count} 个此前完成的分片', 'Resumed: skipped {count} chunks uploaded earlier', 'Återupptaget: hoppade över {count} redan uppladdade delar'],
  'upload.cancelling': ['正在取消上传', 'Cancelling upload', 'Avbryter uppladdningen'],
  'upload.url.parsing': ['正在解析视频链接', 'Reading the video link', 'Läser videolänken'],
  'upload.url.fetching': ['服务端正在拉取源视频，时长取决于源站速度', 'The server is downloading the video; this depends on the source site', 'Servern hämtar videon; tiden beror på källsajten'],
  'upload.url.started': ['正在解析链接并极速下载 (低码率模式)...', 'Reading the link and downloading (low bitrate)…', 'Läser länken och laddar ner (låg bithastighet)…'],

  // messages
  'msg.busy': ['已有上传任务在进行，请等当前任务结束', 'An upload is already running. Please wait for it to finish.', 'En uppladdning pågår redan. Vänta tills den är klar.'],
  'msg.loginFirst': ['⚠️ 权限受限：请先登录系统', '⚠️ Please log in first', '⚠️ Logga in först'],
  'msg.unsupported': ['⚠️ {name} 不是受支持的视频格式', '⚠️ {name} is not a supported video format', '⚠️ {name} är inte ett videoformat som stöds'],
  'msg.onlyOne': ['一次只处理一个视频，已选择 {name}，其余 {count} 个已忽略', 'One video at a time: using {name}, ignored {count} more', 'En video i taget: använder {name}, ignorerade {count} till'],
  'msg.demoUpload': ['演示模式：已模拟完成分片上传', 'Demo mode: chunked upload simulated', 'Demoläge: uppladdningen simulerades'],
  'msg.instant': ['{name} 秒传完成：服务器已有相同内容', '{name} uploaded instantly: the server already had it', '{name} laddades upp direkt: servern hade den redan'],
  'msg.uploaded': ['{name} 上传完成', '{name} uploaded', '{name} har laddats upp'],
  'msg.uploadCancelled': ['上传已取消，进度已保留，可点“继续上传”接着传', 'Upload cancelled. Progress is saved; click “Resume upload” to continue.', 'Uppladdningen avbröts. Förloppet är sparat; klicka på ”Fortsätt ladda upp”.'],
  'msg.uploadInterrupted': ['❌ 上传中断：{error}（进度已保留，可继续上传）', '❌ Upload interrupted: {error} (progress saved, you can resume)', '❌ Uppladdningen avbröts: {error} (förloppet är sparat)'],
  'msg.uploadFailed': ['❌ 上传失败：{error}', '❌ Upload failed: {error}', '❌ Uppladdningen misslyckades: {error}'],
  'msg.progressCleared': ['已清除保留的上传进度，下次将从头开始', 'Saved progress cleared; the next upload starts from the beginning', 'Sparat förlopp rensat; nästa uppladdning börjar från början'],
  'msg.demoUrl': ['演示模式：已模拟完成链接解析', 'Demo mode: link import simulated', 'Demoläge: länkimporten simulerades'],
  'msg.badUrl': ['⚠️ 请输入合法的 http/https 链接', '⚠️ Enter a valid http or https link', '⚠️ Ange en giltig http- eller https-länk'],
  'msg.urlImported': ['✅ 链接资源已入库', '✅ Video imported', '✅ Videon har importerats'],
  'msg.urlUnsupported': ['不支持该平台链接', 'This site is not supported', 'Den här sajten stöds inte'],
  'msg.urlFailed': ['❌ 解析失败: {error}', '❌ Import failed: {error}', '❌ Importen misslyckades: {error}'],
  'msg.listFailed': ['视频资料库加载失败，请稍后刷新', 'Could not load your videos. Refresh in a moment.', 'Det gick inte att läsa in dina videor. Ladda om om en stund.'],
  'msg.listError': ['加载视频列表失败', 'Could not load the video list', 'Det gick inte att läsa in videolistan'],
  'msg.seekFailed': ['原视频加载失败，无法跳转，可先点“重新加载”', 'The video failed to load, so it cannot jump. Click “Reload” first.', 'Videon kunde inte läsas in. Klicka på ”Ladda om” först.'],
  'msg.seekLoading': ['原视频还在载入，稍等一下再点这个时间戳', 'The video is still loading; click the timestamp again in a moment', 'Videon läses fortfarande in; klicka på tidpunkten igen om en stund'],
  'msg.seekNone': ['这个视频暂时没有可播放的原片，无法跳转', 'There is no playable video for this item yet', 'Det finns ingen spelbar video ännu'],
  'msg.nothingToCopy': ['还没有可复制的内容', 'Nothing to copy yet', 'Inget att kopiera ännu'],
  'msg.copiedAnalysis': ['分析结果已复制', 'Analysis copied', 'Analysen har kopierats'],
  'msg.copiedTranscript': ['转写全文已复制', 'Transcript copied', 'Transkriptionen har kopierats'],
  'msg.copyFailed': ['复制失败，请手动选中内容后复制', 'Copy failed. Select the text and copy it manually.', 'Kopieringen misslyckades. Markera texten och kopiera manuellt.'],
  'msg.nothingToExport': ['还没有可导出的内容', 'Nothing to export yet', 'Inget att exportera ännu'],
  'msg.exported': ['已导出 {file}', 'Exported {file}', 'Exporterade {file}'],
  'msg.demoRemoved': ['演示任务已移除', 'Demo item removed', 'Demoobjektet togs bort'],
  'msg.deleteRunning': ['\n\n注意：该视频还有任务正在后台执行，删除后这次的结果会丢失。', '\n\nNote: a task is still running for this video. Its result will be lost.', '\n\nObs: ett jobb körs fortfarande för videon. Resultatet går förlorat.'],
  'msg.deleteConfirm': ['确认要永久删除 "{name}" 吗？{warning}', 'Delete "{name}" permanently?{warning}', 'Radera "{name}" permanent?{warning}'],
  'msg.deleted': ['已删除 {name}', 'Deleted {name}', 'Raderade {name}'],
  'msg.deleteFailed': ['❌ 删除请求失败', '❌ Delete request failed', '❌ Det gick inte att radera'],
  'msg.demoAudio': ['演示模式：{name} 音频已准备', 'Demo mode: audio for {name} is ready', 'Demoläge: ljudet för {name} är klart'],
  'msg.audioPreparing': ['正在转码并下载...', 'Converting and downloading…', 'Konverterar och laddar ner…'],
  'msg.retryLater': ['请稍后重试', 'please try again later', 'försök igen senare'],
  'msg.audioDone': ['✅ 下载完成', '✅ Download complete', '✅ Nedladdningen är klar'],
  'msg.audioFailed': ['音频下载失败：{error}', 'Audio download failed: {error}', 'Ljudet kunde inte laddas ner: {error}'],
  'msg.fillCredentials': ['请输入完整的账号和密码', 'Enter both username and password', 'Ange både användarnamn och lösenord'],
  'msg.httpFailed': ['请求失败（HTTP {status}）', 'Request failed (HTTP {status})', 'Begäran misslyckades (HTTP {status})'],
  'msg.badResponse': ['服务端返回异常，请稍后重试', 'Unexpected server response, try again later', 'Oväntat svar från servern, försök igen senare'],
  'msg.welcome': ['欢迎回来，{name}', 'Welcome back, {name}', 'Välkommen tillbaka, {name}'],
  'msg.registered': ['注册成功，账号密码已保留，直接点“立即登录”即可', 'Account created. Your details are filled in, just log in.', 'Kontot är skapat. Uppgifterna är ifyllda, logga bara in.'],
  'msg.network': ['网络连接错误', 'Network error', 'Nätverksfel'],
  'msg.loggedOut': ['已退出系统', 'Logged out', 'Du är utloggad'],
  'msg.sessionExpired': ['登录状态已失效，请重新登录', 'Your session expired. Please log in again.', 'Sessionen har gått ut. Logga in igen.'],
  'msg.online': ['网络已恢复，正在同步最新状态', 'Back online, syncing', 'Uppkopplad igen, synkar'],
  'msg.offline': ['网络已断开：上传会自动重试，后台任务会在恢复后继续', 'Offline: uploads retry automatically and background tasks continue once you are back', 'Offline: uppladdningar försöker igen och bakgrundsjobb fortsätter när du är uppkopplad'],
  'msg.dismiss': ['点击关闭这条提示', 'Click to dismiss', 'Klicka för att stänga'],

  // library
  'lib.title': ['我的视频', 'My videos', 'Mina videor'],
  'lib.search': ['按名称查找', 'Search by name', 'Sök på namn'],
  'lib.search.aria': ['按名称查找视频', 'Search videos by name', 'Sök videor på namn'],
  'lib.analyse': ['分析视频', 'Analyse', 'Analysera'],
  'lib.analyse.title': ['用 Agent 分析', 'analyse with the agent', 'analysera med agenten'],
  'lib.transcribe': ['提取文字', 'Transcribe', 'Transkribera'],
  'lib.transcribe.title': ['提取文字', 'transcribe', 'transkribera'],
  'lib.audio': ['下载音频', 'Audio', 'Ljud'],
  'lib.audio.title': ['下载音频', 'download the audio', 'ladda ner ljudet'],
  'lib.notReady': ['视频尚未处理完成，暂时无法{action}', 'The video is still processing, so you cannot {action} yet', 'Videon bearbetas fortfarande, så du kan inte {action} ännu'],
  'lib.deleting': ['正在删除…', 'Deleting…', 'Raderar…'],
  'lib.delete': ['删除视频', 'Delete video', 'Radera video'],
  'lib.delete.aria': ['删除 {name}', 'Delete {name}', 'Radera {name}'],
  'lib.noMatch': ['没有名称包含“{query}”的视频。', 'No video name contains “{query}”.', 'Ingen video heter något med ”{query}”.'],
  'lib.clear': ['清除搜索', 'Clear search', 'Rensa sökningen'],
  'lib.empty': ['还没有视频。先在上方上传一段，或者用 <code>docs/samples/binary-tree-demo.mp4</code> 试试。',
    'No videos yet. Upload one above, or try <code>docs/samples/binary-tree-demo.mp4</code>.',
    'Inga videor ännu. Ladda upp en ovan, eller prova <code>docs/samples/binary-tree-demo.mp4</code>.'],
  'lib.state.analysing': ['分析中', 'Analysing', 'Analyserar'],
  'lib.state.transcribing': ['转写中', 'Transcribing', 'Transkriberar'],
  'lib.state.aiRunning': ['AI 分析正在后台执行，完成后会提示你', 'The analysis is running in the background; you will be notified', 'Analysen körs i bakgrunden; du får ett meddelande'],
  'lib.state.asrRunning': ['文字提取正在后台执行，完成后会提示你', 'The transcription is running in the background; you will be notified', 'Transkriberingen körs i bakgrunden; du får ett meddelande'],
  'lib.state.COMPLETED': ['可分析', 'Ready', 'Klar'],
  'lib.state.PROCESSING': ['处理中', 'Processing', 'Bearbetas'],
  'lib.state.FAILED': ['失败', 'Failed', 'Misslyckades'],
  'lib.state.waiting': ['等待中', 'Waiting', 'Väntar'],

  // sidebar
  'side.details': ['任务详情', 'Task details', 'Jobbdetaljer'],
  'side.close': ['关闭分析面板', 'Close the panel', 'Stäng panelen'],
  'side.videoLoading': ['正在载入原视频…', 'Loading the video…', 'Läser in videon…'],
  'side.reload': ['重新加载', 'Reload', 'Ladda om'],
  'side.seekHint': ['点击分析结果中的时间戳，可跳转到对应画面', 'Click a timestamp in the result to jump to that frame', 'Klicka på en tidpunkt i resultatet för att hoppa dit'],
  'side.mode': ['分析模式', 'Analysis mode', 'Analysläge'],
  'side.goal': ['你想从这段视频里得到什么？', 'What do you want from this video?', 'Vad vill du få ut av videon?'],
  'side.goal.placeholder': ['例如：梳理核心观点，给出带时间戳的证据和可执行建议（Ctrl / ⌘ + Enter 提交）',
    'For example: summarise the key points with timestamped evidence and practical advice (Ctrl / ⌘ + Enter to submit)',
    'Till exempel: sammanfatta huvudpoängerna med tidsstämplade belägg och praktiska råd (Ctrl / ⌘ + Enter för att skicka)'],
  'side.counter': ['已输入 {count} / 500 字', '{count} / 500 characters', '{count} / 500 tecken'],
  'side.run': ['开始分析', 'Start analysis', 'Starta analysen'],
  'side.rerun': ['重新分析', 'Analyse again', 'Analysera igen'],
  'side.reconnecting': ['连接中断，正在自动重连（第 {count} 次）· 任务仍在服务端继续', 'Connection lost, reconnecting (attempt {count}). The task keeps running on the server.', 'Anslutningen bröts, återansluter (försök {count}). Jobbet fortsätter på servern.'],
  'side.background': ['可以关闭本面板，任务会在后台继续，完成后会通知你', 'You can close this panel; the task continues and you will be notified', 'Du kan stänga panelen; jobbet fortsätter och du får ett meddelande'],
  'side.plan': ['任务计划', 'Plan', 'Plan'],
  'side.stagesDone': ['已完成阶段', 'Completed stages', 'Klara steg'],
  'side.newResult': ['更换产物', 'New analysis', 'Ny analys'],
  'side.copy': ['复制结果', 'Copy', 'Kopiera'],
  'side.export': ['导出 Markdown', 'Export Markdown', 'Exportera Markdown'],
  'side.evidence.aria': ['视频证据检索', 'Search the video for evidence', 'Sök belägg i videon'],
  'side.evidence.placeholder': ['定位 PPT、字幕、代码或某段讲解', 'Find a slide, a subtitle, code or a passage', 'Hitta en bild, en undertext, kod eller ett avsnitt'],
  'side.evidence.searching': ['检索中', 'Searching', 'Söker'],
  'side.evidence.go': ['定位证据', 'Find evidence', 'Hitta belägg'],
  'side.evidence.empty': ['该时间段暂无可展示文本', 'No text for this time range', 'Ingen text för det här tidsintervallet'],
  'side.evidence.source': ['视频证据', 'Video evidence', 'Belägg i videon'],
  'side.inspector': ['分析详情', 'Analysis details', 'Analysdetaljer'],
  'side.plannerTasks': ['Planner 任务', 'Planner tasks', 'Planner-uppgifter'],
  'side.task': ['任务 {n}', 'Task {n}', 'Uppgift {n}'],
  'side.removeTask': ['删除任务', 'Remove task', 'Ta bort uppgift'],
  'side.addTask': ['添加任务', 'Add task', 'Lägg till uppgift'],
  'side.cancel': ['取消', 'Cancel', 'Avbryt'],
  'side.submitting': ['提交中', 'Submitting', 'Skickar'],
  'side.rerunPlan': ['按新计划重跑', 'Run the new plan', 'Kör den nya planen'],
  'side.editPlan': ['调整计划', 'Edit plan', 'Ändra planen'],
  'side.trace': ['执行轨迹', 'Trace', 'Körningsspår'],
  'side.quality.structure': ['结构完整 {state}', 'Structure {state}', 'Struktur {state}'],
  'side.quality.pass': ['通过', 'passed', 'godkänd'],
  'side.quality.todo': ['待完善', 'incomplete', 'ofullständig'],
  'side.quality.evidence': ['证据支持 {rate}', 'Evidence support {rate}', 'Stöd i belägg {rate}'],
  'side.quality.critic': ['Critic {state}', 'Critic {state}', 'Critic {state}'],
  'side.quality.limit': ['达到轮次上限', 'hit the round limit', 'nådde max antal varv'],
  'side.followUp.placeholder': ['基于视频继续追问...（Ctrl / ⌘ + Enter 发送）', 'Ask a follow-up about the video… (Ctrl / ⌘ + Enter to send)', 'Ställ en följdfråga om videon… (Ctrl / ⌘ + Enter för att skicka)'],
  'side.followUp.loading': ['分析中', 'Thinking', 'Tänker'],
  'side.followUp.send': ['追问', 'Ask', 'Fråga'],
  'side.helpful': ['这个结果有帮助吗？', 'Was this helpful?', 'Var det här till hjälp?'],
  'side.up': ['赞', 'Yes', 'Ja'],
  'side.up.title': ['有帮助', 'Helpful', 'Till hjälp'],
  'side.down': ['踩', 'No', 'Nej'],
  'side.down.title': ['需改进', 'Needs work', 'Kan bli bättre'],
  'side.copyAll': ['复制全文', 'Copy all', 'Kopiera allt'],
  'side.exportText': ['导出文本', 'Export text', 'Exportera text'],
  'side.noTranscript': ['这个视频还没有可展示的转写文本。', 'This video has no transcript yet.', 'Videon har ingen transkription ännu.'],
  'side.chars': ['共 {count} 字', '{count} characters', '{count} tecken'],
  'side.agentWorking': ['Agent 正在分析视频证据', 'The agent is analysing the video', 'Agenten analyserar videon'],
  'side.asrWorking': ['正在识别视频语音', 'Transcribing the speech', 'Transkriberar talet'],
  'side.waited': ['{headline} · 已等待 {time}', '{headline} · waited {time}', '{headline} · väntat {time}'],

  // auth modal
  'auth.login': ['登录', 'Log in', 'Logga in'],
  'auth.register': ['创建账号', 'Create account', 'Skapa konto'],
  'auth.close': ['关闭登录窗口', 'Close', 'Stäng'],
  'auth.username': ['用户名', 'Username', 'Användarnamn'],
  'auth.username.placeholder': ['输入账号', 'Your username', 'Ditt användarnamn'],
  'auth.password': ['密码', 'Password', 'Lösenord'],
  'auth.password.placeholder': ['输入密码', 'Your password', 'Ditt lösenord'],
  'auth.nickname': ['昵称', 'Display name', 'Visningsnamn'],
  'auth.nickname.placeholder': ['别人看到的名字（可选）', 'What others see (optional)', 'Det andra ser (valfritt)'],
  'auth.wait': ['请稍候…', 'Please wait…', 'Vänta…'],
  'auth.noAccount': ['没有账号?', 'No account?', 'Inget konto?'],
  'auth.hasAccount': ['已有账号?', 'Have an account?', 'Har du ett konto?'],
  'auth.toRegister': ['去注册', 'Sign up', 'Registrera dig'],
  'auth.toLogin': ['去登录', 'Log in', 'Logga in'],
  'auth.beta.title': ['项目内测中', 'Private beta', 'Privat beta'],
  'auth.beta.demo': ['公开演示账号', 'Public demo account', 'Öppet demokonto'],
  'auth.beta.limit': ['公用账号，Token 额度有限', 'shared account with a limited token allowance', 'delat konto med begränsad tokenkvot'],
  'auth.beta.fill': ['一键填入', 'Fill in', 'Fyll i'],
  'auth.beta.invite': ['注册需要内测码，请联系作者索要：yunfanteo@outlook.com', 'Signing up needs an invite code. Ask the author: yunfanteo@outlook.com', 'Registrering kräver en inbjudningskod. Fråga författaren: yunfanteo@outlook.com'],
  'auth.invite': ['内测码', 'Invite code', 'Inbjudningskod'],
  'auth.invite.placeholder': ['向作者索要的内测码', 'The code the author gave you', 'Koden du fått av författaren'],
  'auth.inviteRequired': ['项目内测中，请联系作者索要内测码', 'This project is in private beta. Ask the author for an invite code.', 'Projektet är i privat beta. Be författaren om en inbjudningskod.'],
  'quota.exhausted': ['这个账号今天的 AI 额度已用完，明天再来，或联系作者获取内测账号', 'This account has used up today\'s AI allowance. Come back tomorrow, or ask the author for a beta account.', 'Kontot har använt upp dagens AI-kvot. Kom tillbaka i morgon, eller be författaren om ett betakonto.'],

  // analysis workspace
  'goal.default': ['理解视频核心内容，提炼关键结论，并给出带时间戳的证据和可执行建议',
    'Understand the core content of the video, extract the key conclusions, and give timestamped evidence and actionable advice',
    'Förstå videons kärninnehåll, sammanfatta de viktigaste slutsatserna och ge tidsstämplade belägg och konkreta råd'],
  'mode.AUTO': ['自动', 'Auto', 'Auto'],
  'mode.AUTO.desc': ['AI 按目标智能选择模式', 'The AI picks a mode from your goal', 'AI:n väljer läge utifrån målet'],
  'mode.GENERAL': ['通用', 'General', 'Allmänt'],
  'mode.GENERAL.desc': ['结论 · 时间戳证据 · 建议', 'Conclusions · timestamped evidence · advice', 'Slutsatser · tidsstämplade belägg · råd'],
  'mode.LEARNING': ['学习', 'Study', 'Studier'],
  'mode.LEARNING.desc': ['知识点大纲 · 重点难点 · 自测题', 'Outline · key and hard points · self-test', 'Översikt · svåra punkter · självtest'],
  'mode.REVIEW': ['审查', 'Review', 'Granskning'],
  'mode.REVIEW.desc': ['逻辑漏洞 · 夸大表述 · 遗漏点', 'Logic gaps · overclaims · omissions', 'Logiska luckor · överdrifter · utelämnanden'],
  'mode.CREATION': ['创作', 'Create', 'Skapa'],
  'mode.CREATION.desc': ['爆点片段 · 标题 · 口播脚本', 'Highlight clips · titles · voice-over script', 'Höjdpunkter · rubriker · speakermanus'],
  'preset.notes': ['学习笔记', 'Study notes', 'Studieanteckningar'],
  'preset.notes.desc': ['章节、知识点与复习建议', 'Sections, key points and revision tips', 'Avsnitt, nyckelbegrepp och repetitionstips'],
  'preset.notes.prompt': ['生成结构化学习笔记，按章节提炼知识点，引用关键时间戳，并给出复习建议',
    'Write structured study notes: key points by section, citing timestamps, with revision tips',
    'Skriv strukturerade studieanteckningar: nyckelbegrepp per avsnitt med tidsstämplar och repetitionstips'],
  'preset.minutes': ['会议纪要', 'Meeting minutes', 'Mötesanteckningar'],
  'preset.minutes.desc': ['结论、分歧与待办事项', 'Decisions, disagreements and action items', 'Beslut, oenigheter och åtgärdspunkter'],
  'preset.minutes.prompt': ['生成会议纪要，整理核心议题、明确结论、分歧点和待办事项，并引用对应时间戳',
    'Write meeting minutes: main topics, decisions, disagreements and action items, citing timestamps',
    'Skriv mötesanteckningar: huvudämnen, beslut, oenigheter och åtgärdspunkter med tidsstämplar'],
  'preset.manual': ['操作手册', 'How-to guide', 'Instruktion'],
  'preset.manual.desc': ['步骤、条件与异常处理', 'Steps, prerequisites and troubleshooting', 'Steg, förutsättningar och felsökning'],
  'preset.manual.prompt': ['生成可执行操作手册，提取前置条件、操作步骤、注意事项和异常处理，并引用对应时间戳',
    'Write a practical how-to guide: prerequisites, steps, caveats and troubleshooting, citing timestamps',
    'Skriv en praktisk instruktion: förutsättningar, steg, varningar och felsökning med tidsstämplar'],
  'stage.VIDEO_CONTEXT': ['解析语音与画面', 'Reading speech and frames', 'Läser tal och bild'],
  'stage.RETRIEVAL': ['检索相关证据', 'Retrieving evidence', 'Hämtar belägg'],
  'stage.PLANNER': ['拆解分析任务', 'Planning', 'Planerar'],
  'stage.EXECUTOR': ['生成结构化结果', 'Writing the result', 'Skriver resultatet'],
  'stage.CRITIC': ['核验结论与证据', 'Checking the evidence', 'Kontrollerar beläggen'],
  'ws.videoFailed': ['视频加载失败', 'The video failed to load', 'Videon kunde inte läsas in'],
  'ws.videoUnavailable': ['原视频暂时无法加载', 'The video cannot be loaded right now', 'Videon kan inte läsas in just nu'],
  'ws.task.ai': ['AI 分析', 'Analysis', 'Analysen'],
  'ws.task.asr': ['文字提取', 'Transcription', 'Transkriberingen'],
  'ws.taskFailed': ['{task}失败{suffix}：{error}', '{task} failed{suffix}: {error}', '{task} misslyckades{suffix}: {error}'],
  'ws.taskDone': ['{task}完成{suffix}', '{task} finished{suffix}', '{task} är klar{suffix}'],
  'ws.analysisDone': ['分析完成', 'Analysis finished', 'Analysen är klar'],
  'ws.taskError': ['任务执行失败', 'The task failed', 'Jobbet misslyckades'],
  'ws.streamLost': ['任务事件流已断开，请稍后重试', 'Lost the task updates. Please try again later.', 'Jobbuppdateringarna bröts. Försök igen senare.'],
  'ws.asrTitle': ['ASR 转写结果', 'Transcript', 'Transkription'],
  'ws.asrPanel': ['全量文字提取', 'Full transcript', 'Fullständig transkription'],
  'ws.asrContinuing': ['文字提取正在后台继续，进度会自动同步', 'Transcription continues in the background and updates automatically', 'Transkriberingen fortsätter i bakgrunden och uppdateras automatiskt'],
  'ws.asrSubmitted': ['提取任务已提交，正在识别语音', 'Submitted, transcribing the speech', 'Skickat, transkriberar talet'],
  'ws.asrFailed': ['文字提取失败，请稍后重试', 'Transcription failed, try again later', 'Transkriberingen misslyckades, försök igen senare'],
  'ws.takeover': ['这个目标已有分析在进行，正在接管进度', 'An analysis for this goal is already running; following it', 'En analys för det här målet pågår redan; följer den'],
  'ws.queued': ['任务已提交，正在排队进入 Agent 流水线', 'Submitted, waiting in the agent queue', 'Skickat, väntar i agentkön'],
  'ws.historyFailed': ['历史分析状态加载失败', 'Could not load the earlier analysis', 'Det gick inte att läsa in den tidigare analysen'],
  'ws.restoring': ['正在恢复上一次未完成的分析任务', 'Resuming the unfinished analysis', 'Återupptar den ofullständiga analysen'],
  'ws.lastUnfinished': ['上次分析未完成，可以重新提交', 'The last analysis did not finish; you can submit it again', 'Den senaste analysen blev inte klar; du kan skicka den igen'],
  'ws.historyRetry': ['历史分析状态加载失败，可以重新提交', 'Could not load the earlier analysis; you can submit again', 'Det gick inte att läsa in den tidigare analysen; du kan skicka igen'],
  'ws.routing': ['正在识别分析意图…', 'Working out what you want…', 'Tolkar vad du vill…'],
  'ws.routed': ['AI 已识别为「{mode}」模式：{reason}', 'The AI chose the “{mode}” mode: {reason}', 'AI:n valde läget ”{mode}”: {reason}'],
  'ws.routeUnavailable': ['意图识别暂不可用，已按通用模式分析', 'Mode detection is unavailable; using General', 'Lägesvalet är inte tillgängligt; använder Allmänt'],
  'ws.planSize': ['计划需保留 1 至 5 个有效任务', 'A plan needs 1 to 5 tasks', 'En plan behöver 1 till 5 uppgifter'],
  'ws.planComment': ['用户调整 Planner 任务后重新执行', 'Re-run after the user edited the Planner tasks', 'Körs om efter att användaren ändrat planen'],
  'ws.resubmitFailed': ['重新提交失败', 'Resubmission failed', 'Det gick inte att skicka igen'],
  'ws.resubmitted': ['已按新计划重新提交，正在重新执行', 'Resubmitted with the new plan, running again', 'Skickat med den nya planen, körs igen'],
  'ws.followUp': ['追问', 'Follow-up', 'Följdfråga'],
  'ws.followUpFailed': ['追问失败', 'The follow-up failed', 'Följdfrågan misslyckades'],
  'ws.evidenceFailed': ['视频证据检索失败', 'Evidence search failed', 'Sökningen efter belägg misslyckades'],
  'ws.evidenceNone': ['没有找到匹配的视频证据', 'No matching evidence found', 'Inga matchande belägg hittades'],
  'ws.feedbackDemo': ['演示反馈已记录', 'Demo feedback recorded', 'Demofeedback sparad'],
  'ws.feedbackSaved': ['反馈已记录', 'Feedback recorded', 'Tack för din feedback'],
  'ws.playbackFailed': ['视频无法播放：可能是播放地址不可用，或视频编码不受当前浏览器支持。请重新加载；链接导入的视频可重新导入后再试。',
    'The video cannot play: the link may have expired or your browser may not support its encoding. Reload; for imported links, import the video again.',
    'Videon kan inte spelas upp: länken kan ha gått ut eller så stöds inte kodningen av webbläsaren. Ladda om; importera länkade videor igen.'],
  'ws.ms': ['{n} 毫秒', '{n} ms', '{n} ms'],
  'ws.s': ['{n} 秒', '{n} s', '{n} s'],


  // progress pushed by the server, by stage (mirrors StatusMessage in server-go/internal/analysis/status.go)
  'progress.QUEUED': ['任务已排队', 'Queued', 'I kö'],
  'progress.CONTEXT': ['正在解析视频语音和关键画面', 'Reading the speech and key frames', 'Läser talet och viktiga bildrutor'],
  'progress.RETRIEVAL': ['正在检索与目标相关的视频证据', 'Finding evidence related to your goal', 'Söker belägg som hör till ditt mål'],
  'progress.AGENT_LOOP': ['多模态上下文已就绪，Agent 开始分析', 'The context is ready, the agent is starting', 'Underlaget är klart, agenten börjar'],
  'progress.PLAN_COMPLETED': ['Planner 已完成任务拆解', 'The Planner has broken the goal into tasks', 'Planner har delat upp målet i uppgifter'],
  'progress.EXECUTOR': ['Executor 正在生成结构化产物', 'The Executor is writing the result', 'Executor skriver resultatet'],
  'progress.CRITIC': ['Critic 正在核验结论和证据', 'The Critic is checking conclusions and evidence', 'Critic kontrollerar slutsatser och belägg'],
  'progress.REFRESH': ['正在根据 Critic 反馈补充证据', "Adding evidence based on the Critic's feedback", 'Kompletterar belägg utifrån Critics återkoppling'],
  'progress.RETRYING': ['任务执行异常，正在自动重试', 'Something went wrong, retrying automatically', 'Något gick fel, försöker igen automatiskt'],
  'progress.DEFAULT': ['正在分析视频', 'Analysing the video', 'Analyserar videon'],

  // helpers
  'time.s': ['{s} 秒', '{s} s', '{s} s'],
  'time.ms': ['{m} 分 {s} 秒', '{m} min {s} s', '{m} min {s} s'],
  'time.hm': ['{h} 小时 {m} 分', '{h} h {m} min', '{h} h {m} min'],
  'file.none': ['请先选择视频文件', 'Choose a video file first', 'Välj en videofil först'],
  'file.empty': ['该文件大小为 0，可能已损坏或仍在同步，请重新选择', 'This file is empty; it may be damaged or still syncing. Choose it again.', 'Filen är tom; den kan vara skadad eller synkas fortfarande. Välj den igen.'],
  'file.tooBig': ['文件 {size}，超过 {max} 上限，请先压缩或分段', 'The file is {size}, over the {max} limit. Compress or split it first.', 'Filen är {size}, över gränsen på {max}. Komprimera eller dela upp den först.'],
  'chunk.cancelled': ['上传已取消', 'Upload cancelled', 'Uppladdningen avbröts'],
  'chunk.mergeFailed': ['分片合并失败，可重新选择同一文件继续', 'Merging the chunks failed; choose the same file again to continue', 'Det gick inte att slå ihop delarna; välj samma fil igen för att fortsätta'],
  'chunk.statusOffline': ['网络异常，暂时无法确认上传进度。续传进度已保留，请稍后继续上传', 'Network problem, cannot confirm the progress. Progress is saved; resume later.', 'Nätverksproblem, förloppet kan inte bekräftas. Det är sparat; fortsätt senare.'],
  'chunk.statusHttp': ['暂时无法确认上传进度（HTTP {status}）。续传进度已保留，请稍后继续上传', 'Cannot confirm the progress (HTTP {status}). Progress is saved; resume later.', 'Förloppet kan inte bekräftas (HTTP {status}). Det är sparat; fortsätt senare.'],
  'chunk.initFailed': ['上传初始化失败，请稍后重试', 'Could not start the upload, try again later', 'Det gick inte att starta uppladdningen, försök igen senare'],
  'chunk.failed': ['分片 {n}/{total} 上传失败：{error}', 'Chunk {n}/{total} failed: {error}', 'Del {n}/{total} misslyckades: {error}'],
  'chunk.network': ['网络异常', 'network error', 'nätverksfel'],
  'chunk.rejected': ['服务端未接收该分片', 'The server did not accept the chunk', 'Servern tog inte emot delen'],
  'hash.failed': ['文件指纹计算失败', 'Could not fingerprint the file', 'Det gick inte att beräkna filens fingeravtryck'],
  'api.noToken': ['登录接口未返回有效令牌', 'The login did not return a valid token', 'Inloggningen gav ingen giltig token'],
  'api.unreachable': ['无法连接后端服务，请确认后端已启动且地址配置正确', 'Cannot reach the server. Check that the backend is running and the address is right.', 'Servern går inte att nå. Kontrollera att backend körs och att adressen stämmer.'],
  'stream.failed': ['事件流连接失败（HTTP {status}）', 'Could not connect to task updates (HTTP {status})', 'Kunde inte ansluta till jobbuppdateringar (HTTP {status})'],
  'stream.noBody': ['服务端未返回事件流', 'The server returned no update stream', 'Servern skickade ingen uppdateringsström']
}

const STAGE_PROGRESS = {
  '': 'QUEUED', QUEUED: 'QUEUED', VIDEO_CONTEXT: 'CONTEXT', CONTEXT_COMPLETED: 'CONTEXT', CHUNKS_COMPLETED: 'RETRIEVAL',
  RETRIEVAL: 'RETRIEVAL', AGENT_LOOP: 'AGENT_LOOP', PLAN_COMPLETED: 'PLAN_COMPLETED', EXECUTOR_STARTED: 'EXECUTOR',
  EXECUTOR_COMPLETED: 'EXECUTOR', CRITIC_STARTED: 'CRITIC', CRITIC_RETRY_REQUIRED: 'REFRESH', EVIDENCE_REFRESHED: 'REFRESH',
  RETRYING: 'RETRYING'
}
const HAS_CHINESE = /[\u3400-\u9fff]/

/**
 * Text the server sent (progress, failure reasons) is Chinese. In Chinese it is shown as is; in other
 * languages a known stage gets its translated progress line and any other Chinese text a translated fallback.
 */
export function serverText(message, { stage, fallback = 'progress.DEFAULT' } = {}) {
  if (locale.value === 'zh') return message || t(fallback)
  if (stage !== undefined && stage !== null && STAGE_PROGRESS[stage]) return t(`progress.${STAGE_PROGRESS[stage]}`)
  if (!message || HAS_CHINESE.test(message)) return t(fallback)
  return message
}

const MESSAGES = { zh: {}, en: {}, sv: {} }
for (const [key, [zh, en, sv]] of Object.entries(ROWS)) {
  MESSAGES.zh[key] = zh
  MESSAGES.en[key] = en
  MESSAGES.sv[key] = sv
}
