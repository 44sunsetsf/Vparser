// 首屏点云：Store Blåmann（照片里那座山）的真实高程，Copernicus 30 m DEM，由 deploy/terrain.py 生成。
// 纯 Canvas 2D：透视投影 16k 个点 + 等高剖面线 + 一道扫过的激光。只在首屏可见时绘制。
(() => {
  const canvas = document.querySelector('.terrain')
  if (!canvas || !canvas.getContext) return
  const ctx = canvas.getContext('2d')
  const root = document.documentElement
  const calm = matchMedia('(prefers-reduced-motion: reduce)').matches
  const small = matchMedia('(max-width: 640px)').matches

  // world units are km: x across (−4.5…4.5), z depth (0 near … 7 far), y height
  const WIDTH_KM = 9, DEPTH_KM = 7, EXAGGERATE = 2.6
  let pts = null, screen = null, cols = 0, rows = 0, inView = false
  let w = 0, h = 0, dpr = 1, colors = null
  let yaw = 0, pitch = 0, tYaw = 0, tPitch = 0
  let running = false, raf = 0, t0 = performance.now()

  function readColors() {
    const cs = getComputedStyle(root)
    colors = {
      hi: cs.getPropertyValue('--pc-hi').trim() || '255,255,255',
      lo: cs.getPropertyValue('--pc-lo').trim() || '220,232,242',
      alpha: parseFloat(cs.getPropertyValue('--pc-alpha')) || 0.6,
      dark: root.classList.contains('dark'),
    }
  }

  function resize() {
    dpr = Math.min(devicePixelRatio || 1, 2)
    w = canvas.clientWidth; h = canvas.clientHeight
    canvas.width = Math.round(w * dpr); canvas.height = Math.round(h * dpr)
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  }

  async function load() {
    const buf = await (await fetch('img/terrain.bin')).arrayBuffer()
    const head = new DataView(buf, 0, 8)
    const W = head.getUint16(0, true), H = head.getUint16(2, true), top = head.getUint16(4, true) / 1000
    const data = new Uint8Array(buf, 8)
    const step = 1
    cols = Math.ceil(W / step); rows = Math.ceil(H / step)
    pts = new Float32Array(cols * rows * 4)          // x, y, z, height 0…1
    let k = 0
    for (let j = 0; j < H; j += step) {
      for (let i = 0; i < W; i += step) {
        const v = data[j * W + i] / 255
        pts[k] = (i / (W - 1) - 0.5) * WIDTH_KM
        pts[k + 1] = v * top * EXAGGERATE
        pts[k + 2] = (1 - j / (H - 1)) * DEPTH_KM
        pts[k + 3] = v
        k += 4
      }
    }
    screen = new Float32Array(cols * rows * 3)
    const n = cols * rows
    const label = document.getElementById('hud-pts')
    if (label) label.textContent = `canvas 2d · ${n.toLocaleString('en-US')} pts`
  }

  // camera: south-east of the fjord, looking north-west at the peak
  const cam = { x: 0, y: 1.1, z: -3.2 }
  function project(x, y, z, cy, sy, cp, sp, f) {
    let dx = x - cam.x, dy = y - cam.y, dz = z - cam.z
    const rx = dx * cy - dz * sy, rz1 = dx * sy + dz * cy     // yaw
    const ry = dy * cp - rz1 * sp, rz = dy * sp + rz1 * cp    // pitch
    if (rz < 0.1) return null
    return [w / 2 + rx / rz * f, h * 0.66 - ry / rz * f, rz]
  }

  function camera() {
    const p0 = -0.02 + pitch
    const f = w > h ? Math.max(w, h * 1.4) * 0.85 : w * 1.6   // portrait phones zoom in on the peak
    return [Math.cos(yaw), Math.sin(yaw), Math.cos(p0), Math.sin(p0), f]
  }
  // a point's look without the laser: the intro's particles land exactly on these
  const shade = (v, z) => {
    const depth = Math.max(0.15, 1 - (z - 3) / 9)
    return [Math.min(1, (0.3 + 0.9 * v) * depth * colors.alpha), 1 + 1.6 * depth]
  }

  function draw(now) {
    yaw += (tYaw - yaw) * 0.05; pitch += (tPitch - pitch) * 0.05
    const [cy, sy, cp, sp, f] = camera()
    ctx.clearRect(0, 0, w, h)
    ctx.globalCompositeOperation = colors.dark ? 'lighter' : 'source-over'

    // the laser sweeps across every 7 s
    const scan = calm ? 99 : ((now - t0) / 7000 % 1) * (WIDTH_KM + 2) - WIDTH_KM / 2 - 1

    for (let n = 0, k = 0; k < pts.length; k += 4, n += 3) {
      const s = project(pts[k], pts[k + 1], pts[k + 2], cy, sy, cp, sp, f)
      if (!s) { screen[n + 2] = -1; continue }
      screen[n] = s[0]; screen[n + 1] = s[1]; screen[n + 2] = s[2]
      const v = pts[k + 3]
      if (v === 0 && (k / 4) % 3) continue           // the fjord surface: a sparse dotted plane
      const [base, dot] = shade(v, s[2])
      const near = Math.abs(pts[k] - scan)
      const lit = near < 0.35 ? 1 - near / 0.35 : 0
      const a = Math.min(1, base + lit * 0.9)
      const size = dot + lit * 1.2
      ctx.fillStyle = `rgba(${v > 0.45 || lit ? colors.hi : colors.lo},${a.toFixed(3)})`
      ctx.fillRect(s[0], s[1], size, size)
    }

    // profile lines every few rows give the wireframe feel
    ctx.lineWidth = 1
    const every = small ? 4 : 6
    for (let r = 0; r < rows; r += every) {
      ctx.beginPath()
      let pen = false
      for (let c = 0; c < cols; c++) {
        const n = (r * cols + c) * 3
        if (screen[n + 2] < 0) { pen = false; continue }
        if (pen) ctx.lineTo(screen[n], screen[n + 1]); else { ctx.moveTo(screen[n], screen[n + 1]); pen = true }
      }
      const depth = Math.max(0, 1 - r / rows)
      ctx.strokeStyle = `rgba(${colors.lo},${(0.05 + 0.12 * (1 - depth)) * colors.alpha})`
      ctx.stroke()
    }
    ctx.globalCompositeOperation = 'source-over'
    if (running && !calm) raf = requestAnimationFrame(draw)
  }

  function start() { if (running || !pts) return; running = true; raf = requestAnimationFrame(draw) }
  function stop() { running = false; cancelAnimationFrame(raf) }

  addEventListener('pointermove', (e) => {
    tYaw = (e.clientX / innerWidth - 0.5) * 0.2          // about ±6°
    tPitch = (e.clientY / innerHeight - 0.5) * 0.06
  }, { passive: true })
  if (small) addEventListener('scroll', () => { tPitch = Math.min(scrollY / innerHeight, 1) * 0.12 }, { passive: true })
  addEventListener('resize', () => { resize(); if (!running && pts) draw(performance.now()) })
  // the page's theme switch fires this so the colours follow
  addEventListener('themechange', () => { readColors(); if (!running && pts) draw(performance.now()) })

  // For the intro in site.js: `count` random points as they will appear on screen (viewport coordinates), and a way to
  // show the canvas at once instead of fading it in.
  function screenPoints(count) {
    if (!pts || !w) return null
    const [cy, sy, cp, sp, f] = camera()
    const box = canvas.getBoundingClientRect(), n = cols * rows, out = []
    for (let tries = 0; out.length < count && tries < count * 4; tries++) {
      const k = Math.floor(Math.random() * n) * 4, v = pts[k + 3]
      if (v === 0 && Math.random() < 0.7) continue           // keep the fjord plane as sparse as it is drawn
      const s = project(pts[k], pts[k + 1], pts[k + 2], cy, sy, cp, sp, f)
      if (!s || s[0] < 0 || s[0] > w || s[1] < 0 || s[1] > h) continue
      const [a, size] = shade(v, s[2])
      out.push({ x: box.left + s[0], y: box.top + s[1], a, size, rgb: v > 0.45 ? colors.hi : colors.lo })
    }
    return { points: out, dark: colors.dark }
  }
  function showNow() { canvas.classList.add('instant', 'ready'); if (!running && pts) draw(performance.now()) }
  function reveal(ms) { canvas.style.transition = `opacity ${ms}ms ease`; canvas.classList.add('ready') }
  let loaded
  const ready = new Promise((resolve) => { loaded = resolve })
  window.__terrain = { ready, screenPoints, showNow, reveal }

  load().then(() => {
    readColors(); resize()
    canvas.classList.add('ready')
    loaded(true)
    if (calm) { draw(performance.now()); return }
    const io = new IntersectionObserver(([e]) => { inView = e.isIntersecting; inView && !document.hidden ? start() : stop() })
    io.observe(canvas)
    document.addEventListener('visibilitychange', () => { !document.hidden && inView ? start() : stop() })
  }).catch(() => { /* no terrain: the photo alone still looks fine */ })
})()
