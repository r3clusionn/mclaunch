// appshell page shim: stands in for Seanime Denshi's Electron preload
// (window.electron) and mpv-prism's preload (window.mpvPrism), so the web UI
// runs unchanged in appshell's CEF window.
//
// Calls go to the native side through __appshellPost(json), injected by the
// helper process; replies and events come back through __appshellRecv(msg).
//
// Video: mpv-prism normally sends frames into the page. Here the native side
// draws the video underneath the page instead, so this shim tells it where
// the presenter's <video> element is and makes that element and everything
// behind it transparent.
(() => {
    "use strict"
    if (window.__appshell) return
    const post = window.__appshellPost
    if (typeof post !== "function") return

    let nextId = 1
    const pending = new Map()
    const listeners = new Map() // electron channel -> Set(callback)
    const prismListeners = new Set()

    function call(ch, ...args) {
        return new Promise((resolve, reject) => {
            const id = nextId++
            pending.set(id, { resolve, reject })
            post(JSON.stringify({ id, ch, args }))
        })
    }

    function send(ch, ...args) {
        post(JSON.stringify({ id: 0, ch, args }))
    }

    window.__appshellRecv = (msg) => {
        if (msg.id) {
            const p = pending.get(msg.id)
            if (!p) return
            pending.delete(msg.id)
            if (msg.error) p.reject(new Error(msg.error))
            else p.resolve(msg.value)
            return
        }
        if (msg.event === "mpv-prism:events") {
            // A batch from one player: property changes, then maybe a state event.
            for (const event of msg.events) {
                const data = { playerId: msg.playerId, event }
                for (const l of prismListeners) {
                    try { l(data) } catch (e) { console.error(e) }
                }
            }
            return
        }
        if (msg.event === "mpv-prism:event") {
            for (const l of prismListeners) {
                try { l(msg.data) } catch (e) { console.error(e) }
            }
            return
        }
        const set = listeners.get(msg.event)
        if (set) {
            for (const l of set) {
                try { l(...(msg.args || [])) } catch (e) { console.error(e) }
            }
        }
    }

    const unsupported = (value) => () => Promise.resolve(value)

    window.electron = {
        window: {
            minimize: () => send("window:minimize"),
            maximize: () => send("window:maximize"),
            close: () => send("window:close"),
            isMaximized: () => call("window:isMaximized"),
            isMinimizable: () => Promise.resolve(true),
            isMaximizable: () => Promise.resolve(true),
            isClosable: () => Promise.resolve(true),
            isFullscreen: () => call("window:isFullscreen"),
            setFullscreen: (fullscreen) => send("window:setFullscreen", !!fullscreen),
            toggleMaximize: () => send("window:toggleMaximize"),
            hide: () => send("window:hide"),
            show: () => send("window:show"),
            isVisible: () => call("window:isVisible"),
            setTitleBarStyle: () => {},
            getCurrentWindow: () => Promise.resolve("main"),
            isMainWindow: () => {},
        },
        localServer: {
            getPort: () => call("get-local-server-port"),
            allowWebviewOrigin: () => Promise.resolve(true),
        },
        startup: {
            ready: () => send("startup:renderer-ready"),
        },
        on: (channel, callback) => {
            if (!listeners.has(channel)) listeners.set(channel, new Set())
            listeners.get(channel).add(callback)
            return () => listeners.get(channel)?.delete(callback)
        },
        emit: (channel, data) => send(channel, data),
        send: (channel, ...args) => send(channel, ...args),
        platform: "linux",
        clipboard: {
            writeText: (text) => call("clipboard:writeText", String(text)),
        },
        // Updates come from rebuilding the source, not from a feed.
        checkForUpdates: unsupported(null),
        installUpdate: unsupported(false),
        killServer: () => call("kill-server"),
        denshiSettings: {
            get: () => call("denshi:getSettings"),
            set: (settings) => call("denshi:setSettings", settings),
        },
        mpvCore: {
            createTempSubtitle: (filename, content) => call("mpvcore:create-temp-subtitle", filename, content),
            writeConfigFile: (content) => call("mpvcore:write-config-file", content),
            createScreenshotPath: () => call("mpvcore:create-screenshot-path"),
            saveScreenshot: (filePath, base64Data) => call("mpvcore:save-screenshot", filePath, base64Data),
            setLoggingEnabled: (enabled) => call("mpvcore:setLoggingEnabled", !!enabled),
            exportLogs: () => call("mpvcore:export-logs"),
            getAnime4KDirectory: () => call("mpvcore:get-anime4k-directory"),
            scanAnime4KDirectory: (directory) => call("mpvcore:scan-anime4k-directory", directory),
            openAnime4KDirectory: (directory) => call("mpvcore:open-anime4k-directory", directory),
        },
        powerSaveBlocker: {
            start: () => call("power-save-blocker:start"),
            stop: (id) => call("power-save-blocker:stop", id),
        },
        // Chromecast is not implemented yet: report no devices.
        cast: {
            discover: unsupported(false),
            stopDiscovery: unsupported(undefined),
            getDevices: unsupported([]),
            connect: unsupported(false),
            disconnect: unsupported(undefined),
            getStatus: unsupported(null),
            loadMedia: unsupported(false),
            play: unsupported(undefined),
            pause: unsupported(undefined),
            seek: unsupported(undefined),
            stop: unsupported(undefined),
            setVolume: unsupported(undefined),
            setMuted: unsupported(undefined),
            sendSubtitleEvents: unsupported(undefined),
            sendSubtitleTracks: unsupported(undefined),
            switchSubtitleTrack: unsupported(undefined),
            sendFonts: unsupported(undefined),
            sendSubtitleHeader: unsupported(undefined),
            disableSubtitles: unsupported(undefined),
            getLanIP: unsupported(null),
        },
    }
    window.__isElectronDesktop__ = true

    // ---- video presenters: where the native side draws the video --------

    const presenters = new Map() // presenterId -> state
    let styleEl = null

    function ensureStyle() {
        if (styleEl) return
        styleEl = document.createElement("style")
        styleEl.id = "appshell-video"
        styleEl.textContent =
            // display:none, not opacity:0: a transparent canvas or <video> still
            // makes Chromium composite at the display rate (120 paints/s vs ~3).
            "[data-mpv-prism-video],[data-mpv-prism-video-canvas]{display:none!important}" +
            "[data-mpv-prism-video-host]{background:transparent!important}" +
            ".appshell-see-through{background:transparent!important;background-color:transparent!important}" +
            // Every ancestor of a presented video, by selector rather than by a
            // class on the element: React rewrites className when the player
            // re-renders (pause, fullscreen), which dropped the class and left
            // the player's bg-black box over the video.
            "html[data-appshell-presenting] :has([data-mpv-prism-video][data-appshell-presented])," +
            "html[data-appshell-presenting]:has([data-mpv-prism-video][data-appshell-presented])" +
            "{background:transparent!important;background-color:transparent!important}"
        document.documentElement.appendChild(styleEl)
    }

    function pickVideoElement() {
        const taken = new Set([...presenters.values()].map(p => p.el))
        const all = [...document.querySelectorAll("[data-mpv-prism-video]")]
        return all.reverse().find(el => !taken.has(el)) || all[0] || null
    }

    function seeThrough(el, on) {
        el.toggleAttribute("data-appshell-presented", on)
        if (presenters.size || on) document.documentElement.setAttribute("data-appshell-presenting", "")
        else document.documentElement.removeAttribute("data-appshell-presenting")
        for (let n = el.parentElement; n; n = n.parentElement) {
            n.classList.toggle("appshell-see-through", on)
        }
    }

    // Everything painted under the video must let it through, not only the
    // player's ancestors: each element next to the player's ancestor chain
    // that overlaps the video gets a CSS mask with a hole of the video's shape.
    // The player's own children (its controls) are untouched.
    const savedMask = new WeakMap()
    const lastMask = new WeakMap()

    function setHole(el, x, y, w, h) {
        if (!savedMask.has(el)) {
            savedMask.set(el, [el.style.getPropertyValue("-webkit-mask"), el.style.getPropertyValue("-webkit-mask-composite"),
                el.style.getPropertyValue("mask-composite")])
        }
        // Unchanged: leave the style alone (a write restyles and repaints the
        // whole element, which is most of the page).
        const mask = `linear-gradient(#000 0 0), linear-gradient(#000 0 0) ${Math.round(x)}px ${Math.round(y)}px / ${Math.round(w)}px ${Math.round(h)}px no-repeat`
        if (lastMask.get(el) === mask) return
        lastMask.set(el, mask)
        el.style.setProperty("-webkit-mask", mask, "important")
        el.style.setProperty("-webkit-mask-composite", "xor", "important")
        el.style.setProperty("mask-composite", "exclude", "important")
    }

    function clearHole(el) {
        const saved = savedMask.get(el)
        if (!saved) return
        savedMask.delete(el)
        lastMask.delete(el)
        el.style.setProperty("-webkit-mask", saved[0])
        el.style.setProperty("-webkit-mask-composite", saved[1])
        el.style.setProperty("mask-composite", saved[2])
    }

    function overlaps(a, b) {
        return a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom && a.width > 0 && a.height > 0
    }

    function updateHoles(p) {
        const hr = p.box.getBoundingClientRect()
        const holed = new Set()
        const host = p.el.closest("[data-mpv-prism-video-host]") || p.el.parentElement
        for (let n = host; n && n.parentElement && n !== document.documentElement; n = n.parentElement) {
            for (const s of n.parentElement.children) {
                if (s === n || s.tagName === "SCRIPT" || s.tagName === "STYLE" || s.tagName === "LINK") continue
                const sr = s.getBoundingClientRect()
                if (!overlaps(sr, hr)) continue
                setHole(s, hr.left - sr.left, hr.top - sr.top, hr.width, hr.height)
                holed.add(s)
            }
        }
        for (const s of p.holed || []) if (!holed.has(s)) clearHole(s)
        p.holed = holed
    }

    function reportRect(p) {
        const r = p.box.getBoundingClientRect()
        const visible = r.width > 1 && r.height > 1 && document.visibilityState === "visible"
        const key = `${Math.round(r.left)},${Math.round(r.top)},${Math.round(r.width)},${Math.round(r.height)},${visible}`
        if (key === p.lastKey) return
        p.lastKey = key
        updateHoles(p)
        send("mpv-prism:presenter-rect", p.playerId, p.presenterId, {
            left: r.left, top: r.top, width: r.width, height: r.height,
            scaleFactor: window.devicePixelRatio || 1, visible,
        })
    }

    function startPresenter(playerId, presenterId) {
        stopPresenter(presenterId)
        const el = pickVideoElement()
        if (!el) return
        ensureStyle()
        seeThrough(el, true)
        // The video element itself is display:none; its host has the same box.
        const box = el.closest("[data-mpv-prism-video-host]") || el.parentElement || el
        const p = { playerId, presenterId, el, box, lastKey: "", ticks: 0, holed: new Set() }
        p.observer = new ResizeObserver(() => reportRect(p))
        p.observer.observe(box)
        // Position changes (scrolling, transforms, the mini player moving)
        // don't fire ResizeObserver; a cheap check every 100 ms catches them.
        p.timer = setInterval(() => {
            if (!p.el.isConnected) {
                stopPresenter(presenterId)
                return
            }
            reportRect(p)
            // New page content can appear under the video without it moving.
            if (++p.ticks % 5 === 0) {
                if (!p.el.hasAttribute("data-appshell-presented")) seeThrough(p.el, true)
                updateHoles(p)
            }
        }, 100)
        presenters.set(presenterId, p)
        reportRect(p)
    }

    function stopPresenter(presenterId) {
        const p = presenters.get(presenterId)
        if (!p) return
        presenters.delete(presenterId)
        p.observer.disconnect()
        clearInterval(p.timer)
        seeThrough(p.el, false)
        for (const s of p.holed || []) clearHole(s)
        send("mpv-prism:presenter-rect", p.playerId, p.presenterId, null)
    }

    document.addEventListener("visibilitychange", () => presenters.forEach(p => { p.lastKey = ""; reportRect(p) }))

    window.mpvPrism = {
        create: (playerId, options, ownerId) => call("mpv-prism:create", playerId, options || null, ownerId || null),
        cancelCreate: (playerId) => call("mpv-prism:cancel-create", playerId),
        destroy: (playerId) => call("mpv-prism:destroy", playerId),
        load: (playerId, uri) => call("mpv-prism:load", playerId, uri),
        play: (playerId) => call("mpv-prism:play", playerId),
        pause: (playerId) => call("mpv-prism:pause", playerId),
        stop: (playerId) => call("mpv-prism:stop", playerId),
        seek: (playerId, seconds, mode) => call("mpv-prism:seek", playerId, seconds, mode || "absolute"),
        setSpeed: (playerId, speed) => call("mpv-prism:set-speed", playerId, speed),
        selectTrack: (playerId, kind, id) => call("mpv-prism:select-track", playerId, kind, id === undefined ? null : id),
        setProperty: (playerId, name, value) => call("mpv-prism:set-property", playerId, name, value),
        setShaders: (playerId, paths) => call("mpv-prism:set-shaders", playerId, paths || []),
        getProperty: (playerId, name) => call("mpv-prism:get-property", playerId, name),
        observeProperty: (playerId, name) => call("mpv-prism:observe-property", playerId, name),
        command: (playerId, args) => call("mpv-prism:command", playerId, args),
        isVideoFrameReceiverReady: () => Promise.resolve(true),
        startVideoPresenter: (playerId, presenterId, size) => {
            startPresenter(playerId, presenterId)
            return call("mpv-prism:start-video-presenter", playerId, presenterId, size)
        },
        resizeVideoPresenter: (playerId, presenterId) => {
            const p = presenters.get(presenterId)
            if (p) { p.lastKey = ""; reportRect(p) }
            return Promise.resolve()
        },
        suspendVideoPresenter: (playerId, presenterId) => call("mpv-prism:suspend-video-presenter", playerId, presenterId),
        resumeVideoPresenter: (playerId, presenterId) => call("mpv-prism:resume-video-presenter", playerId, presenterId),
        stopVideoPresenter: (playerId, presenterId) => {
            stopPresenter(presenterId)
            return call("mpv-prism:stop-video-presenter", playerId, presenterId)
        },
        onEvent: (listener) => {
            prismListeners.add(listener)
            return () => prismListeners.delete(listener)
        },
        getWin32BackendDiagnostics: () => Promise.resolve({ angleAvailable: false }),
        getLinuxBackendDiagnostics: () => call("mpv-prism:get-linux-backend-diagnostics"),
    }
    window.__mpvPrismVideoDebug = false

    window.__appshell = { call, send }
})()
