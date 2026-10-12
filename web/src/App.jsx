import { ArrowLeft, Boxes, Check, Cpu, Download, FileArchive, FolderInput, Gamepad2, Globe, LayoutGrid, LogIn, Package, Play, Plus, RefreshCw, Save, ScrollText, Search, Settings as SettingsIcon, ShieldAlert, Sparkles, Trash2, User, Users, Zap } from "lucide-react"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { ago, api, loaderNames, playTime, size, subscribe } from "./api.js"
import { Badge, Button, Card, cn, Empty, IconButton, Input, Modal, Select, Spinner, Switch, Tabs } from "./ui.jsx"

// ---- routing (location.hash) ------------------------------------------------------

function useRoute() {
    const [hash, setHash] = useState(location.hash.slice(1))
    useEffect(() => {
        const f = () => setHash(location.hash.slice(1))
        window.addEventListener("hashchange", f)
        return () => window.removeEventListener("hashchange", f)
    }, [])
    return hash.split("/")
}

const go = path => { location.hash = path }

// ---- shared state ---------------------------------------------------------------------

function useData(path, deps = []) {
    const [data, setData] = useState(null)
    const [error, setError] = useState(null)
    const load = useCallback(() => {
        if (!path) return
        api(path).then(d => { setData(d); setError(null) }).catch(e => setError(e.message))
    }, [path])
    useEffect(load, [load, ...deps])
    return [data, load, error]
}

function useToasts() {
    const [toasts, setToasts] = useState([])
    const push = useCallback((kind, text) => {
        const id = Math.random()
        setToasts(t => [...t, { id, kind, text }])
        setTimeout(() => setToasts(t => t.filter(x => x.id !== id)), kind === "error" ? 9000 : 4000)
    }, [])
    return [toasts, push]
}

const loaderColor = {
    vanilla: "from-emerald-700/70 to-emerald-950", fabric: "from-amber-700/60 to-stone-950",
    quilt: "from-fuchsia-700/60 to-purple-950", neoforge: "from-orange-700/60 to-red-950", forge: "from-sky-800/60 to-slate-950",
}

// A screenshot or world icon from the game, else the modpack's icon, else the
// loader's colors with the first letter.
function Cover({ inst, className, big }) {
    const [coverFailed, setCoverFailed] = useState(false)
    return (
        <div className={cn("overflow-hidden bg-linear-to-br", loaderColor[inst.loader] || loaderColor.vanilla, className)}>
            {!coverFailed ? (
                <img src={`/api/instances/${inst.id}/cover?t=${inst.lastPlayed}`} alt="" onError={() => setCoverFailed(true)}
                    className="absolute inset-0 size-full object-cover [image-rendering:pixelated]" />
            ) : inst.icon ? (
                <img src={inst.icon} alt="" className="absolute inset-0 size-full object-cover" />
            ) : (
                <div className="absolute inset-0 flex items-center justify-center">
                    <span className={cn("font-black text-white/15 select-none", big ? "text-[10rem]" : "text-7xl")}>{inst.name.slice(0, 1).toUpperCase()}</span>
                </div>
            )}
            <div className="absolute inset-0 bg-linear-to-t from-black/80 via-black/10 to-transparent" />
        </div>
    )
}

// ---- app ------------------------------------------------------------------------------

export default function App() {
    const route = useRoute()
    const [instances, reloadInstances] = useData("/instances")
    const [accounts, reloadAccounts] = useData("/accounts")
    const [progress, setProgress] = useState({})
    const [toasts, toast] = useToasts()

    useEffect(() => subscribe(e => {
        if (e.type === "progress") {
            setProgress(p => ({ ...p, [e.instance]: { ...p[e.instance], ...(e.step ? { step: e.step } : {}), ...(e.totalFiles ? { files: e.files, totalFiles: e.totalFiles, done: e.done, total: e.total } : {}) } }))
        } else if (e.type === "done") {
            setProgress(p => { const n = { ...p }; delete n[e.instance]; return n })
            reloadInstances()
        } else if (e.type === "error") {
            toast("error", e.message)
        } else if (e.type === "launched") {
            toast("success", "Game started")
            reloadInstances()
        } else if (e.type === "exited") {
            reloadInstances()
            if (e.crash) go(`instance/${e.instance}/log`)
        } else if (e.type === "accounts") {
            reloadAccounts()
        }
    }), [reloadInstances, reloadAccounts, toast])

    const ctx = { instances: instances || [], reloadInstances, accounts: accounts || [], reloadAccounts, progress, toast }
    const page = route[0] || "home"
    const defaultAccount = (accounts || []).find(a => a.default)

    return (
        <div className="flex h-full">
            <nav className="flex w-[72px] flex-none flex-col items-center gap-2 border-r border-white/5 bg-gray-950/60 py-4">
                <div className="mb-3 flex size-11 items-center justify-center rounded-xl bg-brand-500 text-white shadow-lg shadow-brand-500/20">
                    <Boxes className="size-6" />
                </div>
                <NavItem icon={LayoutGrid} label="Library" active={page === "home" || page === "instance"} onClick={() => go("")} />
                <NavItem icon={Search} label="Browse Modrinth" active={page === "browse"} onClick={() => go("browse")} />
                <NavItem icon={Users} label="Accounts" active={page === "accounts"} onClick={() => go("accounts")} />
                <div className="flex-1" />
                <NavItem icon={SettingsIcon} label="Settings" active={page === "settings"} onClick={() => go("settings")} />
                <button onClick={() => go("accounts")} title={defaultAccount ? defaultAccount.name : "No account"}
                    className="mt-1 flex size-10 items-center justify-center rounded-full bg-gray-800 text-sm font-bold text-gray-300 ring-1 ring-white/10">
                    {defaultAccount ? defaultAccount.name.slice(0, 1).toUpperCase() : <User className="size-5" />}
                </button>
            </nav>
            <main className="min-w-0 flex-1 overflow-y-auto">
                {page === "home" && <Home {...ctx} />}
                {page === "instance" && <InstancePage id={route[1]} tab={route[2] || "mods"} {...ctx} />}
                {page === "browse" && <Browse {...ctx} preset={route[1]} />}
                {page === "accounts" && <Accounts {...ctx} />}
                {page === "settings" && <SettingsPage {...ctx} />}
            </main>
            <div className="pointer-events-none fixed bottom-5 right-5 z-[60] flex w-96 flex-col gap-2">
                {toasts.map(t => (
                    <div key={t.id} className={cn("pointer-events-auto rounded-lg px-4 py-3 text-sm shadow-xl ring-1 backdrop-blur",
                        t.kind === "error" ? "bg-red-950/90 text-red-200 ring-red-500/20" : "bg-gray-900/95 text-gray-100 ring-white/10")}>
                        {t.text}
                    </div>
                ))}
            </div>
        </div>
    )
}

function NavItem({ icon: Icon, label, active, onClick }) {
    return (
        <button onClick={onClick} title={label}
            className={cn("group relative flex size-11 items-center justify-center rounded-xl transition", active ? "bg-white/10 text-white" : "text-gray-400 hover:bg-white/5 hover:text-gray-100")}>
            <Icon className="size-[22px]" />
            <span className="pointer-events-none absolute left-14 z-50 whitespace-nowrap rounded-md bg-gray-800 px-2 py-1 text-xs font-medium text-gray-100 opacity-0 shadow-lg ring-1 ring-white/10 transition group-hover:opacity-100">{label}</span>
        </button>
    )
}

// ---- launching ------------------------------------------------------------------------

function usePlay({ toast, accounts }) {
    return async inst => {
        if (!accounts.length) {
            toast("error", "Add an account first (Accounts): an offline account plays singleplayer.")
            go("accounts")
            return
        }
        try {
            await api(`/instances/${inst.id}/launch`, { method: "POST", body: {} })
        } catch (e) { toast("error", e.message) }
    }
}

function ProgressLine({ p }) {
    if (!p) return null
    const pct = p.totalFiles ? Math.round(p.files * 100 / p.totalFiles) : null
    return (
        <div className="space-y-1.5">
            <div className="flex justify-between text-xs text-gray-300">
                <span className="truncate">{p.step}</span>
                {pct !== null && <span>{p.total ? `${size(p.done)} / ${size(p.total)}` : `${p.files}/${p.totalFiles}`}</span>}
            </div>
            <div className="h-1 overflow-hidden rounded-full bg-white/10">
                <div className={cn("h-full rounded-full bg-brand-400 transition-all", pct === null && "w-1/3 animate-pulse")} style={pct !== null ? { width: pct + "%" } : undefined} />
            </div>
        </div>
    )
}

// ---- library ----------------------------------------------------------------------------

function Home(ctx) {
    const { instances, progress } = ctx
    const [creating, setCreating] = useState(false)
    const play = usePlay(ctx)
    const hero = instances[0]
    return (
        <div className="pb-10">
            {hero ? (
                <div className="relative h-[340px] overflow-hidden">
                    <Cover inst={hero} big className="absolute inset-0" />
                    <div className="absolute inset-0 bg-linear-to-r from-[#070707] via-[#070707]/70 to-transparent" />
                    <div className="absolute inset-0 bg-linear-to-t from-[#070707] to-transparent" />
                    <div className="relative flex h-full max-w-3xl flex-col justify-end gap-4 p-10">
                        <p className="text-sm font-semibold uppercase tracking-wider text-brand-300">{hero.lastPlayed && !hero.lastPlayed.startsWith("0001") ? "Continue playing" : "Newest instance"}</p>
                        <h1 className="text-5xl font-extrabold tracking-tight">{hero.name}</h1>
                        <div className="flex flex-wrap items-center gap-2 text-sm text-gray-300">
                            <Badge intent="brand">{loaderNames[hero.loader]} {hero.loaderVersion}</Badge>
                            <Badge>Minecraft {hero.game}</Badge>
                            {hero.mods > 0 && <Badge>{hero.mods} mods</Badge>}
                            <span className="text-gray-400">{playTime(hero.playSeconds, hero.lastPlayed)} · last played {ago(hero.lastPlayed)}</span>
                        </div>
                        <div className="flex items-center gap-3">
                            <Button intent="primary" size="lg" icon={Play} loading={!!progress[hero.id]} onClick={() => play(hero)}>Play</Button>
                            <Button intent="gray-subtle" size="lg" onClick={() => go(`instance/${hero.id}`)}>Open</Button>
                        </div>
                        <div className="max-w-md"><ProgressLine p={progress[hero.id]} /></div>
                    </div>
                </div>
            ) : (
                <div className="h-16" />
            )}
            <section className="space-y-4 px-10">
                <div className="flex items-center justify-between">
                    <h2 className="text-2xl font-bold">Instances</h2>
                    <Button intent="primary-subtle" icon={Plus} onClick={() => setCreating(true)}>New instance</Button>
                </div>
                {!instances.length ? (
                    <Empty icon={Gamepad2} title="No instances yet">
                        Create one, import from Prism Launcher (Settings) or install a modpack from Browse.
                    </Empty>
                ) : (
                    <div className="grid grid-cols-[repeat(auto-fill,minmax(230px,1fr))] gap-5">
                        {instances.map(i => <InstanceCard key={i.id} inst={i} p={progress[i.id]} onPlay={() => play(i)} />)}
                    </div>
                )}
            </section>
            <CreateInstance open={creating} onClose={() => setCreating(false)} {...ctx} />
        </div>
    )
}

function InstanceCard({ inst, p, onPlay }) {
    return (
        <div className="group cursor-pointer space-y-2.5" onClick={() => go(`instance/${inst.id}`)}>
            <div className="relative aspect-[4/3] overflow-hidden rounded-xl ring-1 ring-white/5 transition duration-200 group-hover:-translate-y-1 group-hover:ring-white/20">
                <Cover inst={inst} className="absolute inset-0 transition duration-300 group-hover:scale-[1.04]" />
                <div className="absolute left-3 top-3 flex gap-1.5">
                    <Badge intent="brand" className="bg-black/50 backdrop-blur">{loaderNames[inst.loader]}</Badge>
                    {inst.running && <Badge intent="success" className="bg-black/50 backdrop-blur">Playing</Badge>}
                </div>
                <button onClick={e => { e.stopPropagation(); onPlay() }}
                    className="absolute bottom-3 right-3 flex size-12 items-center justify-center rounded-full bg-brand-500 text-white opacity-0 shadow-lg transition group-hover:opacity-100 hover:scale-105">
                    {p ? <Spinner className="border-white border-r-transparent" /> : <Play className="size-5 fill-white" />}
                </button>
                {p && <div className="absolute inset-x-3 bottom-3 right-16"><ProgressLine p={p} /></div>}
            </div>
            <div className="px-0.5">
                <p className="truncate font-semibold">{inst.name}</p>
                <p className="text-xs text-gray-400">{inst.game}{inst.mods ? ` · ${inst.mods} mods` : ""} · {playTime(inst.playSeconds, inst.lastPlayed)}</p>
            </div>
        </div>
    )
}

function CreateInstance({ open, onClose, toast, reloadInstances }) {
    const [versions] = useData(open ? "/versions" : null)
    const [name, setName] = useState("")
    const [game, setGame] = useState("")
    const [loader, setLoader] = useState("fabric")
    const [lv, setLv] = useState([])
    const [loaderVersion, setLoaderVersion] = useState("")
    const [perf, setPerf] = useState(true)
    const [busy, setBusy] = useState(false)
    useEffect(() => { if (versions?.length && !game) setGame(versions[0].ID) }, [versions, game])
    useEffect(() => {
        setLv([]); setLoaderVersion("")
        if (!game || loader === "vanilla") return
        api(`/loaders/${loader}/${game}`).then(v => { setLv(v); setLoaderVersion((v.find(x => x.stable) || v[0])?.version || "") }).catch(() => setLv(null))
    }, [game, loader])
    const create = async () => {
        setBusy(true)
        try {
            const inst = await api("/instances", { method: "POST", body: { Name: name || `${loaderNames[loader]} ${game}`, Game: game, Loader: loader, LoaderVersion: loaderVersion, PerfMods: perf && loader === "fabric" } })
            reloadInstances()
            onClose()
            go(`instance/${inst.id}`)
        } catch (e) { toast("error", e.message) }
        setBusy(false)
    }
    return (
        <Modal open={open} onClose={onClose} title="New instance">
            <div className="space-y-4">
                <Input label="Name" placeholder={`${loaderNames[loader]} ${game}`} value={name} onChange={e => setName(e.target.value)} autoFocus />
                <div className="grid grid-cols-2 gap-3">
                    <Select label="Minecraft version" value={game} onChange={e => setGame(e.target.value)}
                        options={(versions || []).map(v => ({ value: v.ID, label: v.ID }))} />
                    <Select label="Mod loader" value={loader} onChange={e => setLoader(e.target.value)}
                        options={Object.entries(loaderNames).map(([value, label]) => ({ value, label }))} />
                </div>
                {loader !== "vanilla" && (
                    lv === null ? <p className="text-sm text-amber-300">{loaderNames[loader]} has no version for Minecraft {game}.</p> :
                        <Select label={`${loaderNames[loader]} version`} value={loaderVersion} onChange={e => setLoaderVersion(e.target.value)}
                            options={lv.map(v => ({ value: v.version, label: v.version + (v.stable ? "" : " (beta)") }))} />
                )}
                {loader === "fabric" && (
                    <Switch checked={perf} onChange={setPerf} label="Add the performance mods"
                        help="Sodium, Lithium, FerriteCore, ModernFix, ImmediatelyFast and Entity Culling, from Modrinth" />
                )}
                <div className="flex justify-end gap-2 pt-2">
                    <Button intent="gray-basic" onClick={onClose}>Cancel</Button>
                    <Button intent="primary" icon={Plus} loading={busy} disabled={!game || (loader !== "vanilla" && !loaderVersion)} onClick={create}>Create</Button>
                </div>
            </div>
        </Modal>
    )
}

// ---- an instance ---------------------------------------------------------------------------

function InstancePage({ id, tab, ...ctx }) {
    const { progress, toast } = ctx
    const [inst, reload] = useData(`/instances/${id}`, [ctx.instances])
    const play = usePlay(ctx)
    if (!inst) return <div className="p-10"><Spinner /></div>
    const p = progress[inst.id]
    const setTab = t => go(`instance/${id}/${t}`)
    return (
        <div className="pb-10">
            <div className="relative h-[260px] overflow-hidden">
                <Cover inst={inst} big className="absolute inset-0" />
                <div className="absolute inset-0 bg-linear-to-t from-[#070707] via-[#070707]/60 to-transparent" />
                <div className="relative flex h-full flex-col justify-between p-8">
                    <IconButton icon={ArrowLeft} intent="gray-subtle" onClick={() => go("")} className="bg-black/40 backdrop-blur" />
                    <div className="flex items-end justify-between gap-6">
                        <div className="space-y-3">
                            <h1 className="text-4xl font-extrabold tracking-tight">{inst.name}</h1>
                            <div className="flex flex-wrap items-center gap-2 text-sm">
                                <Badge intent="brand">{loaderNames[inst.loader]} {inst.loaderVersion}</Badge>
                                <Badge>Minecraft {inst.game}</Badge>
                                <Badge>{(inst.memoryMB / 1024).toFixed(inst.memoryMB % 1024 ? 1 : 0)} GB · {inst.gc === "zgc" ? "ZGC" : "G1"}</Badge>
                                <span className="text-gray-400">{playTime(inst.playSeconds, inst.lastPlayed)} · last played {ago(inst.lastPlayed)}</span>
                            </div>
                        </div>
                        <div className="w-72 space-y-3">
                            <Button intent="primary" size="lg" icon={Play} className="w-full" loading={!!p} onClick={() => play(inst)}>
                                {inst.running ? "Playing" : "Play"}
                            </Button>
                            <ProgressLine p={p} />
                        </div>
                    </div>
                </div>
            </div>
            <div className="px-8">
                <Tabs value={tab} onChange={setTab} tabs={[
                    { value: "mods", label: "Mods", count: inst.mods }, { value: "worlds", label: "Worlds" },
                    { value: "settings", label: "Settings" }, { value: "log", label: "Log" }]} />
                <div className="pt-6">
                    {tab === "mods" && <ModsTab inst={inst} toast={toast} busy={!!p} />}
                    {tab === "worlds" && <WorldsTab inst={inst} toast={toast} />}
                    {tab === "settings" && <InstanceSettings inst={inst} reload={reload} {...ctx} />}
                    {tab === "log" && <LogTab inst={inst} running={inst.running} />}
                </div>
            </div>
        </div>
    )
}

function ModsTab({ inst, toast, busy }) {
    const [mods, reload] = useData(`/instances/${inst.id}/mods`, [busy])
    const [filter, setFilter] = useState("")
    const action = async (path, body, msg) => {
        try { await api(`/instances/${inst.id}/${path}`, { method: "POST", body }); msg && toast("success", msg); reload() } catch (e) { toast("error", e.message) }
    }
    if (inst.loader === "vanilla") return <Empty icon={Package} title="Vanilla has no mods">Create a Fabric, Quilt, NeoForge or Forge instance to add mods.</Empty>
    if (!mods) return <Spinner />
    const shown = mods.filter(m => !filter || (m.title + m.file).toLowerCase().includes(filter.toLowerCase()))
    return (
        <div className="space-y-4">
            <div className="flex flex-wrap items-center gap-2">
                <div className="w-72"><Input placeholder="Filter mods" value={filter} onChange={e => setFilter(e.target.value)} /></div>
                <div className="flex-1" />
                <Button intent="primary-subtle" icon={Plus} onClick={() => go(`browse/${inst.id}`)}>Add mods</Button>
                <Button intent="gray-subtle" icon={RefreshCw} disabled={busy} onClick={() => action("mods/update", {}, "Looking for updates…")}>Update all</Button>
                {inst.loader === "fabric" && <Button intent="gray-subtle" icon={Zap} disabled={busy} onClick={() => action("perf", {}, "Adding the performance mods…")}>Performance mods</Button>}
            </div>
            {!mods.length ? <Empty icon={Package} title="No mods yet" /> : (
                <Card className="divide-y divide-white/5">
                    {shown.map(m => (
                        <div key={m.file} className={cn("flex items-center gap-3 px-4 py-2.5", !m.enabled && "opacity-50")}>
                            {m.icon ? <img src={m.icon} alt="" className="size-9 rounded-lg bg-gray-800 object-cover" /> : <div className="flex size-9 items-center justify-center rounded-lg bg-gray-800"><Package className="size-4 text-gray-500" /></div>}
                            <div className="min-w-0 flex-1">
                                <p className="truncate text-sm font-semibold">{m.title || m.file}</p>
                                <p className="truncate text-xs text-gray-500">{m.versionNumber ? `${m.versionNumber} · ` : ""}{m.file}</p>
                            </div>
                            {!m.project && <Badge>not on Modrinth</Badge>}
                            <Switch checked={m.enabled} onChange={() => action("mods/toggle", { File: m.file })} />
                            <IconButton icon={Trash2} size="sm" onClick={() => confirm(`Remove ${m.title || m.file}?`) && action("mods/remove", { File: m.file })} />
                        </div>
                    ))}
                </Card>
            )}
        </div>
    )
}

function WorldsTab({ inst, toast }) {
    const [worlds] = useData(`/instances/${inst.id}/worlds`)
    if (!worlds) return <Spinner />
    if (!worlds.length) return <Empty icon={Globe} title="No worlds yet">Worlds appear here after you create one in the game.</Empty>
    return (
        <Card className="divide-y divide-white/5">
            {worlds.map(w => (
                <div key={w.name} className="flex items-center gap-4 px-4 py-3">
                    <Globe className="size-5 text-emerald-400" />
                    <div className="flex-1">
                        <p className="font-semibold">{w.name}</p>
                        <p className="text-xs text-gray-500">{size(w.size)} · played {ago(w.modified)}</p>
                    </div>
                    <Button intent="gray-subtle" size="sm" icon={Save} onClick={async () => {
                        try { const p = await api(`/instances/${inst.id}/worlds/backup`, { method: "POST", body: { Name: w.name } }); toast("success", "Backed up to " + p) } catch (e) { toast("error", e.message) }
                    }}>Back up</Button>
                </div>
            ))}
        </Card>
    )
}

function InstanceSettings({ inst, reload, toast, reloadInstances }) {
    const [f, setF] = useState(inst)
    const set = (k, v) => setF(x => ({ ...x, [k]: v }))
    const save = async () => {
        try {
            await api(`/instances/${inst.id}`, { method: "PATCH", body: { name: f.name, memoryMB: +f.memoryMB, gc: f.gc, jvmArgs: f.jvmArgs, java: f.java, width: +f.width, height: +f.height, fpsOnAC: +f.fpsOnAC, fpsOnBattery: +f.fpsOnBattery } })
            toast("success", "Saved"); reload(); reloadInstances()
        } catch (e) { toast("error", e.message) }
    }
    return (
        <div className="grid max-w-4xl gap-6 md:grid-cols-2">
            <Card className="space-y-4 p-5">
                <h3 className="flex items-center gap-2 font-bold"><Cpu className="size-4 text-brand-300" /> Java</h3>
                <label className="block space-y-2">
                    <span className="flex justify-between text-sm font-medium text-gray-300"><span>Memory</span><span>{(f.memoryMB / 1024).toFixed(1)} GB</span></span>
                    <input type="range" min={2048} max={12288} step={512} value={f.memoryMB} onChange={e => set("memoryMB", +e.target.value)} className="w-full" />
                    <span className="block text-xs text-gray-500">The heap is fixed (start = maximum). 4 GB suits vanilla and light mods, 6 to 8 GB big packs; more makes garbage collection slower.</span>
                </label>
                <Select label="Garbage collector" value={f.gc} onChange={e => set("gc", e.target.value)} options={[
                    { value: "zgc", label: "Generational ZGC (pauses under 3 ms, default)" }, { value: "g1", label: "G1 (less CPU, pauses up to ~25 ms)" }]} />
                <Input label="Extra JVM arguments" value={f.jvmArgs} onChange={e => set("jvmArgs", e.target.value)} placeholder="-Dsomething=value" />
                <Input label="Java" value={f.java} onChange={e => set("java", e.target.value)} placeholder="Mojang's runtime for this version" help="Leave empty to use the Java Mojang ships for this version." />
            </Card>
            <Card className="space-y-4 p-5">
                <h3 className="flex items-center gap-2 font-bold"><Gamepad2 className="size-4 text-brand-300" /> Game</h3>
                <Input label="Name" value={f.name} onChange={e => set("name", e.target.value)} />
                <div className="grid grid-cols-2 gap-3">
                    <Input label="Window width" type="number" value={f.width} onChange={e => set("width", e.target.value)} />
                    <Input label="Window height" type="number" value={f.height} onChange={e => set("height", e.target.value)} />
                </div>
                <div className="grid grid-cols-2 gap-3">
                    <Input label="Frame cap plugged in" type="number" value={f.fpsOnAC} onChange={e => set("fpsOnAC", e.target.value)} />
                    <Input label="Frame cap on battery" type="number" value={f.fpsOnBattery} onChange={e => set("fpsOnBattery", e.target.value)} />
                </div>
                <p className="text-xs text-gray-500">Written to the game's maxFps before each launch, by power source (0: leave the game's own setting).</p>
            </Card>
            <div className="flex items-center gap-2 md:col-span-2">
                <Button intent="primary" icon={Check} onClick={save}>Save</Button>
                <Button intent="gray-subtle" icon={FileArchive} onClick={async () => {
                    try { const p = await api(`/instances/${inst.id}/export`, { method: "POST" }); toast("success", "Exported to " + p) } catch (e) { toast("error", e.message) }
                }}>Export .mrpack</Button>
                <div className="flex-1" />
                <Button intent="alert" icon={Trash2} onClick={async () => {
                    if (!confirm(`Delete ${inst.name} with its worlds and mods?`)) return
                    await api(`/instances/${inst.id}`, { method: "DELETE" }); reloadInstances(); go("")
                }}>Delete instance</Button>
            </div>
        </div>
    )
}

function LogTab({ inst, running }) {
    const [data, reload] = useData(`/instances/${inst.id}/log`)
    const [q, setQ] = useState("")
    const box = useRef(null)
    useEffect(() => {
        if (!running) return
        const t = setInterval(reload, 1500)
        return () => clearInterval(t)
    }, [running, reload])
    useEffect(() => { if (box.current && !q) box.current.scrollTop = box.current.scrollHeight }, [data, q])
    const lines = useMemo(() => (data?.log || "").replace(/\x1b\[[0-9;]*m/g, "").split("\n"), [data])
    const shown = q ? lines.filter(l => l.toLowerCase().includes(q.toLowerCase())) : lines
    return (
        <div className="space-y-4">
            {data?.crash && (
                <Card className="space-y-3 border-l-4 border-red-500 p-5">
                    <h3 className="flex items-center gap-2 font-bold text-red-300"><ShieldAlert className="size-5" /> The game crashed</h3>
                    {data.suspect && <p className="text-sm">Likely cause: <span className="font-semibold text-white">{data.suspect}</span> appears in the crash report. Try disabling it on the Mods tab.</p>}
                    <pre className="max-h-64 overflow-auto rounded-lg bg-black/50 p-3 text-xs text-gray-300">{data.crash.split("\n").slice(0, 60).join("\n")}</pre>
                    <p className="text-xs text-gray-500">{data.crashFile}</p>
                </Card>
            )}
            <div className="flex items-center gap-2">
                <div className="w-80"><Input placeholder="Search the log" value={q} onChange={e => setQ(e.target.value)} /></div>
                <Button intent="gray-subtle" icon={RefreshCw} onClick={reload}>Refresh</Button>
                {running && <Badge intent="success">live</Badge>}
            </div>
            <pre ref={box} className="h-[55vh] overflow-auto rounded-xl bg-black/60 p-4 font-mono text-xs leading-relaxed ring-1 ring-white/5">
                {shown.map((l, i) => (
                    <div key={i} className={cn(/\b(error|fatal|exception)\b/i.test(l) ? "text-red-300" : /\bWARN\b/.test(l) ? "text-amber-300/90" : "text-gray-400")}>{l || " "}</div>
                ))}
            </pre>
        </div>
    )
}

// ---- Modrinth -----------------------------------------------------------------------------

function Browse({ instances, toast, preset }) {
    const modable = instances.filter(i => i.loader !== "vanilla")
    const [kind, setKind] = useState(preset ? "mod" : "modpack")
    const [target, setTarget] = useState(preset || modable[0]?.id || "")
    const [q, setQ] = useState("")
    const [res, setRes] = useState(null)
    const [installing, setInstalling] = useState({})
    useEffect(() => { if (!target && modable[0]) setTarget(modable[0].id) }, [modable, target])
    useEffect(() => {
        const t = setTimeout(() => {
            const inst = kind === "mod" ? `&instance=${target}` : ""
            api(`/modrinth/search?type=${kind}&q=${encodeURIComponent(q)}${inst}`).then(setRes).catch(e => toast("error", e.message))
        }, 250)
        return () => clearTimeout(t)
    }, [q, kind, target, toast])
    const install = async h => {
        setInstalling(x => ({ ...x, [h.project_id]: true }))
        try {
            if (kind === "modpack") {
                await api("/modrinth/modpack", { method: "POST", body: { Project: h.project_id, Title: h.title, Icon: h.icon_url } })
                toast("success", `Installing ${h.title}… it appears in the library when ready.`)
            } else {
                await api(`/instances/${target}/mods`, { method: "POST", body: { Project: h.project_id } })
                toast("success", `Installing ${h.title} with its dependencies`)
            }
        } catch (e) { toast("error", e.message) }
    }
    const targetInst = instances.find(i => i.id === target)
    return (
        <div className="space-y-6 p-10">
            <div className="flex flex-wrap items-end justify-between gap-4">
                <div>
                    <h1 className="text-3xl font-bold">Browse Modrinth</h1>
                    <p className="text-gray-400">{kind === "mod" && targetInst ? `Mods for ${targetInst.name} (${loaderNames[targetInst.loader]} ${targetInst.game})` : "Modpacks become new instances"}</p>
                </div>
                <div className="flex items-end gap-3">
                    <div className="flex rounded-lg bg-white/5 p-1">
                        {["modpack", "mod"].map(k => (
                            <button key={k} onClick={() => setKind(k)} className={cn("rounded-md px-4 py-1.5 text-sm font-semibold", kind === k ? "bg-brand-500 text-white" : "text-gray-300")}>
                                {k === "mod" ? "Mods" : "Modpacks"}
                            </button>
                        ))}
                    </div>
                    {kind === "mod" && (modable.length ? <div className="w-64"><Select value={target} onChange={e => setTarget(e.target.value)} options={modable.map(i => ({ value: i.id, label: i.name }))} /></div>
                        : <span className="text-sm text-amber-300">Create a modded instance first</span>)}
                </div>
            </div>
            <Input placeholder={kind === "mod" ? "Search mods (Sodium, Iris, JEI…)" : "Search modpacks"} value={q} onChange={e => setQ(e.target.value)} autoFocus />
            {!res ? <Spinner /> : !res.hits.length ? <Empty icon={Search} title="Nothing found" /> : (
                <div className="grid gap-3 lg:grid-cols-2">
                    {res.hits.map(h => (
                        <Card key={h.project_id} className="flex gap-4 p-4">
                            <img src={h.icon_url || ""} alt="" className="size-16 flex-none rounded-xl bg-gray-800 object-cover" />
                            <div className="min-w-0 flex-1 space-y-1">
                                <div className="flex items-center gap-2">
                                    <p className="truncate font-bold">{h.title}</p>
                                    <span className="truncate text-xs text-gray-500">by {h.author}</span>
                                </div>
                                <p className="line-clamp-2 text-sm text-gray-400">{h.description}</p>
                                <div className="flex items-center gap-2 pt-1 text-xs text-gray-500">
                                    <Download className="size-3.5" /> {h.downloads.toLocaleString()}
                                    {h.categories.slice(0, 3).map(c => <Badge key={c}>{c}</Badge>)}
                                </div>
                            </div>
                            <Button intent={installing[h.project_id] ? "success" : "primary-subtle"} size="sm" icon={installing[h.project_id] ? Check : Download}
                                disabled={installing[h.project_id] || (kind === "mod" && !target)} onClick={() => install(h)} className="self-center">
                                {installing[h.project_id] ? "Added" : "Install"}
                            </Button>
                        </Card>
                    ))}
                </div>
            )}
        </div>
    )
}

// ---- accounts and settings -------------------------------------------------------------------

function Accounts({ accounts, reloadAccounts, toast }) {
    const [name, setName] = useState("")
    const [flow, setFlow] = useState(null)
    useEffect(() => {
        if (flow?.status !== "waiting") return
        const t = setInterval(() => api("/accounts/microsoft").then(f => { setFlow(f); if (f?.status !== "waiting") reloadAccounts() }), 2000)
        return () => clearInterval(t)
    }, [flow, reloadAccounts])
    const addOffline = async () => {
        try { await api("/accounts/offline", { method: "POST", body: { Name: name } }); setName(""); reloadAccounts() } catch (e) { toast("error", e.message) }
    }
    const signIn = async () => {
        try { setFlow(await api("/accounts/microsoft", { method: "POST" })) } catch (e) { toast("error", e.message) }
    }
    return (
        <div className="max-w-3xl space-y-8 p-10">
            <h1 className="text-3xl font-bold">Accounts</h1>
            <Card className="divide-y divide-white/5">
                {!accounts.length && <Empty icon={Users} title="No accounts" />}
                {accounts.map(a => (
                    <div key={a.id} className="flex items-center gap-4 px-5 py-3">
                        <div className="flex size-10 items-center justify-center rounded-full bg-gray-800 font-bold">{a.name.slice(0, 1).toUpperCase()}</div>
                        <div className="flex-1">
                            <p className="font-semibold">{a.name} {a.default && <Badge intent="brand" className="ml-1">default</Badge>}</p>
                            <p className="text-xs text-gray-500">{a.type === "msa" ? "Microsoft account" : "Offline: singleplayer and LAN"}</p>
                        </div>
                        {!a.default && <Button size="sm" intent="gray-subtle" onClick={async () => { await api(`/accounts/${a.id}/default`, { method: "POST" }); reloadAccounts() }}>Make default</Button>}
                        <IconButton icon={Trash2} size="sm" onClick={async () => { await api(`/accounts/${a.id}`, { method: "DELETE" }); reloadAccounts() }} />
                    </div>
                ))}
            </Card>
            <div className="grid gap-5 md:grid-cols-2">
                <Card className="space-y-3 p-5">
                    <h3 className="flex items-center gap-2 font-bold"><LogIn className="size-4 text-brand-300" /> Microsoft account</h3>
                    {flow?.status === "waiting" ? (
                        <div className="space-y-3 text-sm">
                            <p>Open <button className="font-semibold text-brand-300 underline" onClick={() => window.open(flow.code.verification_uri)}>{flow.code.verification_uri}</button> on any device and enter:</p>
                            <p className="rounded-lg bg-black/50 py-3 text-center font-mono text-2xl font-bold tracking-[0.3em]">{flow.code.user_code}</p>
                            <p className="flex items-center gap-2 text-gray-400"><Spinner className="size-4" /> Waiting for you to sign in…</p>
                        </div>
                    ) : (
                        <>
                            <p className="text-sm text-gray-400">Signs in with a code on any device; no password is typed here.</p>
                            {flow?.status === "error" && <p className="text-sm text-red-300">{flow.error}</p>}
                            {flow?.status === "done" && <p className="text-sm text-emerald-300">Signed in as {flow.name}</p>}
                            <Button intent="primary" icon={LogIn} onClick={signIn}>Sign in</Button>
                        </>
                    )}
                </Card>
                <Card className="space-y-3 p-5">
                    <h3 className="flex items-center gap-2 font-bold"><User className="size-4 text-brand-300" /> Offline account</h3>
                    <p className="text-sm text-gray-400">A name for singleplayer and LAN; no online servers.</p>
                    <Input placeholder="Name (3 to 16 letters, digits or _)" value={name} onChange={e => setName(e.target.value)} onKeyDown={e => e.key === "Enter" && addOffline()} />
                    <Button intent="gray" icon={Plus} onClick={addOffline} disabled={name.length < 3}>Add</Button>
                </Card>
            </div>
        </div>
    )
}

function SettingsPage({ toast, reloadInstances }) {
    const [data, reload] = useData("/settings")
    const [prismInfo] = useData("/import/prism")
    const [s, setS] = useState(null)
    const file = useRef(null)
    useEffect(() => { if (data) setS(data.settings) }, [data])
    if (!s) return <div className="p-10"><Spinner /></div>
    const save = async next => {
        setS(next)
        try { await api("/settings", { method: "PUT", body: next }); reload() } catch (e) { toast("error", e.message) }
    }
    return (
        <div className="max-w-3xl space-y-8 p-10">
            <h1 className="text-3xl font-bold">Settings</h1>
            <Card className="space-y-5 p-5">
                <h3 className="flex items-center gap-2 font-bold"><Sparkles className="size-4 text-brand-300" /> Launching</h3>
                <Switch checked={s.closeOnLaunch} onChange={v => save({ ...s, closeOnLaunch: v })} label="Close the launcher while playing"
                    help="Its window and processes exit when the game starts (a small supervisor waits for the game, then restores the power profile). On a crash it comes back on the log." />
                <p className="text-sm text-gray-400">Power: {data.onAC ? "plugged in" : "on battery"}. Each instance's frame cap follows the power source.</p>
            </Card>
            <Card className="space-y-4 p-5">
                <h3 className="flex items-center gap-2 font-bold"><LogIn className="size-4 text-brand-300" /> Microsoft sign-in</h3>
                <Input label="Azure app (client) ID" value={s.msaClientId} onChange={e => setS({ ...s, msaClientId: e.target.value })} onBlur={() => save(s)}
                    placeholder="Empty: the official Minecraft launcher's ID"
                    help="Empty signs in as the official launcher does (its client ID, login.live.com), like many small launchers; not sanctioned, and Microsoft can stop it. Or enter your own app registration approved by Mojang." />
            </Card>
            <Card className="space-y-4 p-5">
                <h3 className="flex items-center gap-2 font-bold"><FolderInput className="size-4 text-brand-300" /> Import</h3>
                <div className="flex items-center justify-between gap-4">
                    <div>
                        <p className="text-sm font-medium">Prism Launcher</p>
                        <p className="text-xs text-gray-500">{prismInfo?.instances?.length ? `${prismInfo.instances.length} instances in ${prismInfo.dir}` : `No instances in ${prismInfo?.dir || "Prism's folder"}`}</p>
                    </div>
                    <Button intent="gray-subtle" icon={FolderInput} disabled={!prismInfo?.instances?.length} onClick={async () => {
                        await api("/import/prism", { method: "POST", body: {} }); toast("success", "Importing… instances appear in the library"); setTimeout(reloadInstances, 3000)
                    }}>Import all</Button>
                </div>
                <div className="flex items-center justify-between gap-4">
                    <div>
                        <p className="text-sm font-medium">Modrinth modpack file</p>
                        <p className="text-xs text-gray-500">A .mrpack becomes a new instance</p>
                    </div>
                    <input ref={file} type="file" accept=".mrpack" hidden onChange={async e => {
                        const f = e.target.files[0]
                        if (!f) return
                        await api("/import/mrpack?name=" + encodeURIComponent(f.name.replace(/\.mrpack$/, "")), { method: "POST", body: f, raw: true })
                        toast("success", "Importing " + f.name)
                    }} />
                    <Button intent="gray-subtle" icon={FileArchive} onClick={() => file.current.click()}>Choose file</Button>
                </div>
            </Card>
            <p className="text-sm text-gray-500"><ScrollText className="mr-1 inline size-4" />Data: {data.dataDir}</p>
        </div>
    )
}
