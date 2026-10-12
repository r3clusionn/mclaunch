// The launcher's JSON API (internal/server) and its event stream.
export async function api(path, { method = "GET", body, raw } = {}) {
    const res = await fetch("/api" + path, {
        method,
        headers: body !== undefined && !raw ? { "Content-Type": "application/json" } : undefined,
        body: raw ? body : body !== undefined ? JSON.stringify(body) : undefined,
    })
    const data = await res.json().catch(() => null)
    if (!res.ok) throw new Error(data?.error || res.statusText)
    return data
}

export function subscribe(fn) {
    let es
    let stopped = false
    const open = () => {
        es = new EventSource("/api/events")
        es.onmessage = e => { try { fn(JSON.parse(e.data)) } catch { } }
        es.onerror = () => { es.close(); if (!stopped) setTimeout(open, 1500) }
    }
    open()
    return () => { stopped = true; es?.close() }
}

export const loaderNames = { vanilla: "Vanilla", fabric: "Fabric", quilt: "Quilt", neoforge: "NeoForge", forge: "Forge" }

export function playTime(sec, last) {
    if (!sec) return last && !last.startsWith("0001") ? "Played briefly" : "Never played"
    const h = Math.floor(sec / 3600), m = Math.floor(sec / 60) % 60
    return h ? `${h} h ${m} min played` : `${m} min played`
}

export function ago(iso) {
    if (!iso || iso.startsWith("0001")) return "never"
    const s = (Date.now() - new Date(iso)) / 1000
    if (s < 60) return "just now"
    if (s < 3600) return `${Math.floor(s / 60)} min ago`
    if (s < 86400) return `${Math.floor(s / 3600)} h ago`
    return `${Math.floor(s / 86400)} days ago`
}

export function size(n) {
    if (n > 1e9) return (n / 1e9).toFixed(1) + " GB"
    if (n > 1e6) return (n / 1e6).toFixed(1) + " MB"
    return Math.max(1, Math.round(n / 1e3)) + " kB"
}
