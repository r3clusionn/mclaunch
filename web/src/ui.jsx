// Small components in Seanime's style (its buttons, badges, cards, drawers).
import { X } from "lucide-react"
import { useEffect } from "react"

export function cn(...c) {
    return c.filter(Boolean).join(" ")
}

const intents = {
    primary: "bg-brand-500 hover:bg-brand-600 text-white shadow-sm",
    "primary-subtle": "bg-brand-500/15 hover:bg-brand-500/25 text-brand-200",
    gray: "bg-gray-800 hover:bg-gray-700 text-gray-100 ring-1 ring-white/5",
    "gray-subtle": "bg-white/5 hover:bg-white/10 text-gray-200",
    "gray-basic": "hover:bg-white/5 text-gray-300",
    alert: "bg-red-500/15 hover:bg-red-500/25 text-red-300",
    success: "bg-emerald-500/15 hover:bg-emerald-500/25 text-emerald-300",
}

export function Button({ intent = "gray", size = "md", icon: Icon, loading, className, children, ...p }) {
    const sizes = { sm: "h-8 px-3 text-sm gap-1.5", md: "h-10 px-4 text-sm gap-2", lg: "h-12 px-6 text-base gap-2" }
    return (
        <button
            className={cn("inline-flex items-center justify-center rounded-lg font-semibold transition disabled:opacity-50 disabled:pointer-events-none select-none",
                intents[intent], sizes[size], className)}
            disabled={loading || p.disabled}
            {...p}
        >
            {loading ? <span className="size-4 animate-spin rounded-full border-2 border-current border-r-transparent" /> : Icon && <Icon className="size-4" />}
            {children}
        </button>
    )
}

export function IconButton({ icon: Icon, intent = "gray-basic", className, size = "md", ...p }) {
    const s = size === "sm" ? "size-8" : "size-10"
    return (
        <button className={cn("inline-flex items-center justify-center rounded-lg transition", intents[intent], s, className)} {...p}>
            <Icon className="size-[18px]" />
        </button>
    )
}

export function Badge({ intent = "gray", className, children }) {
    const c = {
        gray: "bg-white/5 text-gray-300", brand: "bg-brand-500/20 text-brand-200", success: "bg-emerald-500/15 text-emerald-300",
        warning: "bg-amber-500/15 text-amber-300", alert: "bg-red-500/15 text-red-300",
    }[intent]
    return <span className={cn("inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-xs font-semibold", c, className)}>{children}</span>
}

export function Card({ className, children, ...p }) {
    return <div className={cn("rounded-xl bg-gray-950 ring-1 ring-white/5", className)} {...p}>{children}</div>
}

export function Input({ label, className, help, ...p }) {
    return (
        <label className="block space-y-1.5">
            {label && <span className="text-sm font-medium text-gray-300">{label}</span>}
            <input className={cn("h-10 w-full rounded-lg bg-gray-900 px-3 text-sm text-gray-100 ring-1 ring-white/10 outline-none transition placeholder:text-gray-500 focus:ring-brand-500", className)} {...p} />
            {help && <span className="block text-xs text-gray-500">{help}</span>}
        </label>
    )
}

export function Select({ label, options, className, ...p }) {
    return (
        <label className="block space-y-1.5">
            {label && <span className="text-sm font-medium text-gray-300">{label}</span>}
            <select className={cn("h-10 w-full rounded-lg bg-gray-900 px-3 text-sm text-gray-100 ring-1 ring-white/10 outline-none focus:ring-brand-500", className)} {...p}>
                {options.map(o => typeof o === "string" ? <option key={o} value={o}>{o}</option> : <option key={o.value} value={o.value}>{o.label}</option>)}
            </select>
        </label>
    )
}

export function Switch({ label, help, checked, onChange }) {
    return (
        <button type="button" onClick={() => onChange(!checked)} className={cn("flex items-start gap-3 text-left", label && "w-full")}>
            <span className={cn("mt-0.5 flex h-6 w-11 flex-none items-center rounded-full p-0.5 transition", checked ? "bg-brand-500" : "bg-gray-700")}>
                <span className={cn("size-5 rounded-full bg-white shadow transition", checked && "translate-x-5")} />
            </span>
            {label && <span>
                <span className="block text-sm font-medium text-gray-200">{label}</span>
                {help && <span className="block text-xs text-gray-500">{help}</span>}
            </span>}
        </button>
    )
}

export function Tabs({ tabs, value, onChange }) {
    return (
        <div className="flex gap-1 border-b border-white/5">
            {tabs.map(t => (
                <button key={t.value} onClick={() => onChange(t.value)}
                    className={cn("relative px-4 py-2.5 text-sm font-semibold transition", value === t.value ? "text-white" : "text-gray-400 hover:text-gray-200")}>
                    {t.label}
                    {t.count !== undefined && <span className="ml-1.5 text-xs text-gray-500">{t.count}</span>}
                    {value === t.value && <span className="absolute inset-x-2 -bottom-px h-0.5 rounded-full bg-brand-400" />}
                </button>
            ))}
        </div>
    )
}

export function Modal({ open, onClose, title, children, wide }) {
    useEffect(() => {
        if (!open) return
        const k = e => e.key === "Escape" && onClose()
        window.addEventListener("keydown", k)
        return () => window.removeEventListener("keydown", k)
    }, [open, onClose])
    if (!open) return null
    return (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-6 backdrop-blur-sm" onMouseDown={onClose}>
            <Card className={cn("max-h-[90vh] w-full overflow-auto p-6 shadow-2xl", wide ? "max-w-3xl" : "max-w-lg")} onMouseDown={e => e.stopPropagation()}>
                <div className="mb-5 flex items-center justify-between">
                    <h2 className="text-xl font-bold">{title}</h2>
                    <IconButton icon={X} size="sm" onClick={onClose} />
                </div>
                {children}
            </Card>
        </div>
    )
}

export function Spinner({ className }) {
    return <span className={cn("inline-block size-5 animate-spin rounded-full border-2 border-brand-400 border-r-transparent", className)} />
}

export function Empty({ icon: Icon, title, children }) {
    return (
        <div className="flex flex-col items-center justify-center gap-3 py-16 text-center">
            {Icon && <Icon className="size-10 text-gray-600" />}
            <p className="text-lg font-semibold text-gray-300">{title}</p>
            {children && <div className="text-sm text-gray-500">{children}</div>}
        </div>
    )
}
