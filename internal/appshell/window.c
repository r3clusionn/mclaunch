/*
 * window.c: the X11 window, its EGL/GLES context, and input for CEF.
 *
 * CEF renders off-screen, so it never sees X events: keys, mouse and focus are
 * translated here and sent to the browser host. Coordinates are in DIPs
 * (pixels divided by the scale factor).
 */
#include "appshell.h"

#include <EGL/eglext.h>
#include <X11/Xatom.h>
#include <X11/Xutil.h>
#include <X11/XKBlib.h>
#include <X11/cursorfont.h>
#include <X11/extensions/XInput2.h>
#include <math.h>
#include <X11/extensions/dpms.h>
#include <X11/extensions/scrnsaver.h>
#include <X11/keysym.h>
#include <X11/XF86keysym.h>

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

Display *as_dpy;
Window as_win;
EGLDisplay as_egl_dpy;
static EGLSurface egl_surf;
static EGLContext egl_ctx;
int as_win_w, as_win_h;
int as_visible;
int as_fullscreen;
double as_scale = 1.0;

static int mapped, obscured;
static Atom wm_delete, net_wm_state, net_wm_state_fs, net_wm_name, utf8_string;
static XIM xim;
static XIC xic;
static Cursor cursors[80]; /* by X cursor font shape / 2 */
static Cursor blank_cursor;
static int current_cursor = -1;

static double scale(void) { return as_scale; }

/* Xft.dpi from the X resources (192 on this laptop -> 2.0). */
static double xft_scale(void)
{
	char *rm = XResourceManagerString(as_dpy);
	char *p = rm ? strstr(rm, "Xft.dpi:") : NULL;
	if (!p)
		return 1.0;
	double dpi = atof(p + strlen("Xft.dpi:"));
	return dpi >= 48 ? dpi / 96.0 : 1.0;
}

/* ---- setup ------------------------------------------------------------ */

static void xi_init(void);

int window_init(void)
{
	as_dpy = XOpenDisplay(NULL);
	if (!as_dpy) {
		fprintf(stderr, "appshell: cannot open the X display\n");
		return -1;
	}
	XkbSetDetectableAutoRepeat(as_dpy, True, NULL); /* repeat = presses only */
	as_scale = as_opts->scale > 0 ? as_opts->scale : xft_scale();

	as_egl_dpy = eglGetPlatformDisplay(EGL_PLATFORM_X11_KHR, as_dpy, NULL);
	if (!eglInitialize(as_egl_dpy, NULL, NULL)) {
		fprintf(stderr, "appshell: eglInitialize failed\n");
		return -1;
	}
	eglBindAPI(EGL_OPENGL_ES_API);
	EGLint cfg_attr[] = {EGL_SURFACE_TYPE, EGL_WINDOW_BIT, EGL_RENDERABLE_TYPE,
			     EGL_OPENGL_ES3_BIT, EGL_RED_SIZE, 8, EGL_GREEN_SIZE, 8,
			     EGL_BLUE_SIZE, 8, EGL_NONE};
	EGLConfig cfg;
	EGLint ncfg = 0, vid;
	if (!eglChooseConfig(as_egl_dpy, cfg_attr, &cfg, 1, &ncfg) || ncfg < 1) {
		fprintf(stderr, "appshell: no suitable EGL config\n");
		return -1;
	}
	eglGetConfigAttrib(as_egl_dpy, cfg, EGL_NATIVE_VISUAL_ID, &vid);
	XVisualInfo tmpl = {.visualid = (VisualID)vid}, *vi;
	int nvi;
	vi = XGetVisualInfo(as_dpy, VisualIDMask, &tmpl, &nvi);
	if (!vi)
		return -1;

	as_win_w = as_opts->width > 0 ? as_opts->width : 1600;
	as_win_h = as_opts->height > 0 ? as_opts->height : 1000;
	XSetWindowAttributes swa = {
		.colormap = XCreateColormap(as_dpy, DefaultRootWindow(as_dpy), vi->visual, AllocNone),
		.background_pixel = 0,
		.event_mask = StructureNotifyMask | ExposureMask | VisibilityChangeMask |
			      KeyPressMask | KeyReleaseMask | ButtonPressMask | ButtonReleaseMask |
			      PointerMotionMask | EnterWindowMask | LeaveWindowMask | FocusChangeMask,
	};
	as_win = XCreateWindow(as_dpy, DefaultRootWindow(as_dpy), 0, 0, as_win_w, as_win_h, 0,
			       vi->depth, InputOutput, vi->visual,
			       CWColormap | CWEventMask | CWBackPixel, &swa);
	XFree(vi);

	char *cls = strdup(as_opts->wm_class ? as_opts->wm_class : "appshell");
	XClassHint ch = {.res_name = cls, .res_class = cls};
	XSetClassHint(as_dpy, as_win, &ch);
	free(cls);
	xi_init();
	wm_delete = XInternAtom(as_dpy, "WM_DELETE_WINDOW", False);
	net_wm_state = XInternAtom(as_dpy, "_NET_WM_STATE", False);
	net_wm_state_fs = XInternAtom(as_dpy, "_NET_WM_STATE_FULLSCREEN", False);
	net_wm_name = XInternAtom(as_dpy, "_NET_WM_NAME", False);
	utf8_string = XInternAtom(as_dpy, "UTF8_STRING", False);
	XSetWMProtocols(as_dpy, as_win, &wm_delete, 1);
	window_set_title(as_opts->title ? as_opts->title : "appshell");

	xim = XOpenIM(as_dpy, NULL, NULL, NULL);
	if (xim)
		xic = XCreateIC(xim, XNInputStyle, XIMPreeditNothing | XIMStatusNothing,
				XNClientWindow, as_win, XNFocusWindow, as_win, NULL);

	static char none[8] = {0};
	Pixmap p = XCreateBitmapFromData(as_dpy, as_win, none, 8, 8);
	XColor black = {0};
	blank_cursor = XCreatePixmapCursor(as_dpy, p, p, &black, &black, 0, 0);
	XFreePixmap(as_dpy, p);

	XMapWindow(as_dpy, as_win);

	EGLint ctx_attr[] = {EGL_CONTEXT_MAJOR_VERSION, 3, EGL_NONE};
	egl_ctx = eglCreateContext(as_egl_dpy, cfg, EGL_NO_CONTEXT, ctx_attr);
	egl_surf = eglCreatePlatformWindowSurface(as_egl_dpy, cfg, &as_win, NULL);
	if (egl_ctx == EGL_NO_CONTEXT || egl_surf == EGL_NO_SURFACE ||
	    !eglMakeCurrent(as_egl_dpy, egl_surf, egl_surf, egl_ctx)) {
		fprintf(stderr, "appshell: EGL context failed: 0x%x\n", eglGetError());
		return -1;
	}
	eglSwapInterval(as_egl_dpy, 1);
	return 0;
}

void window_destroy(void)
{
	window_inhibit_screensaver(0);
	if (as_egl_dpy) {
		eglMakeCurrent(as_egl_dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, EGL_NO_CONTEXT);
		eglDestroySurface(as_egl_dpy, egl_surf);
		eglDestroyContext(as_egl_dpy, egl_ctx);
		eglTerminate(as_egl_dpy);
	}
	if (xic)
		XDestroyIC(xic);
	if (xim)
		XCloseIM(xim);
	if (as_dpy) {
		XDestroyWindow(as_dpy, as_win);
		XCloseDisplay(as_dpy);
	}
}

/* ---- window state ----------------------------------------------------- */

void window_set_title(const char *utf8)
{
	XChangeProperty(as_dpy, as_win, net_wm_name, utf8_string, 8, PropModeReplace,
			(const unsigned char *)utf8, (int)strlen(utf8));
	XStoreName(as_dpy, as_win, utf8);
	XFlush(as_dpy);
}

void window_set_fullscreen(int on)
{
	XEvent ev = {0};
	ev.type = ClientMessage;
	ev.xclient.window = as_win;
	ev.xclient.message_type = net_wm_state;
	ev.xclient.format = 32;
	ev.xclient.data.l[0] = on ? 1 : 0; /* _NET_WM_STATE_ADD / REMOVE */
	ev.xclient.data.l[1] = (long)net_wm_state_fs;
	ev.xclient.data.l[3] = 1;
	XSendEvent(as_dpy, DefaultRootWindow(as_dpy), False,
		   SubstructureRedirectMask | SubstructureNotifyMask, &ev);
	XFlush(as_dpy);
	as_fullscreen = on;
	bridge_send_event("window:fullscreen", on ? "[true]" : "[false]");
}

void window_minimize(void)
{
	XIconifyWindow(as_dpy, as_win, DefaultScreen(as_dpy));
	XFlush(as_dpy);
}

void window_inhibit_screensaver(int on)
{
	static int inhibited;
	if (on == inhibited || !as_dpy)
		return;
	/* Also suspends DPMS blanking while a video plays. */
	XScreenSaverSuspend(as_dpy, on ? True : False);
	XFlush(as_dpy);
	inhibited = on;
}

void window_set_cursor(int type)
{
	if (type == current_cursor)
		return;
	current_cursor = type;
	Cursor c;
	if (type == CT_NONE) {
		c = blank_cursor;
	} else {
		unsigned shape;
		switch (type) {
		case CT_HAND: shape = XC_hand2; break;
		case CT_IBEAM: shape = XC_xterm; break;
		case CT_WAIT: shape = XC_watch; break;
		case CT_PROGRESS: shape = XC_watch; break;
		case CT_CROSS: shape = XC_crosshair; break;
		case CT_HELP: shape = XC_question_arrow; break;
		case CT_EASTWESTRESIZE: case CT_COLUMNRESIZE: shape = XC_sb_h_double_arrow; break;
		case CT_NORTHSOUTHRESIZE: case CT_ROWRESIZE: shape = XC_sb_v_double_arrow; break;
		case CT_MOVE: shape = XC_fleur; break;
		case CT_NOTALLOWED: shape = XC_X_cursor; break;
		case CT_GRAB: case CT_GRABBING: shape = XC_hand1; break;
		default: shape = XC_left_ptr; break;
		}
		int slot = (int)(shape / 2) % 80;
		if (!cursors[slot])
			cursors[slot] = XCreateFontCursor(as_dpy, shape);
		c = cursors[slot];
	}
	XDefineCursor(as_dpy, as_win, c);
	XFlush(as_dpy);
}

/* DPMS state, checked at most every 500 ms (one round trip). */
int window_display_on(void)
{
	static double last;
	static int on = 1;
	double t = as_now_ms();
	if (t - last > 500) {
		last = t;
		int dummy;
		CARD16 level;
		BOOL enabled;
		if (DPMSQueryExtension(as_dpy, &dummy, &dummy) && DPMSInfo(as_dpy, &level, &enabled))
			on = !enabled || level == DPMSModeOn;
	}
	return on;
}

static void update_visible(void)
{
	int v = mapped && !obscured;
	if (v == as_visible)
		return;
	as_visible = v;
	if (as_browser) {
		cef_browser_host_t *h = as_browser->get_host(as_browser);
		h->was_hidden(h, !v); /* lets Chromium stop rendering while hidden */
		h->base.release(&h->base);
	}
	if (v)
		as_request_redraw();
	bridge_send_event(v ? "window:shown" : "window:hidden", NULL);
}

/*
 * With the window unmapped (another dwm tag) or the display off, Xorg paces
 * swaps with a 1 Hz fallback timer and a vsynced swap blocks for a second.
 * Skip the swap instead.
 */
int window_swap(void)
{
	if (!as_visible || !window_display_on())
		return 0;
	eglSwapBuffers(as_egl_dpy, egl_surf);
	return 1;
}

/* Mesa resizes a window surface only when it is swapped. Before a debug
 * screenshot with the display off (no swaps happen), swap once if the
 * surface still has the old size; that swap may wait up to a second. */
void window_sync_surface_size(void)
{
	EGLint w = 0, h = 0;
	eglQuerySurface(as_egl_dpy, egl_surf, EGL_WIDTH, &w);
	eglQuerySurface(as_egl_dpy, egl_surf, EGL_HEIGHT, &h);
	if (w != as_win_w || h != as_win_h)
		eglSwapBuffers(as_egl_dpy, egl_surf);
}

/* ---- input ------------------------------------------------------------ */

static uint32_t mods_from_state(unsigned state)
{
	uint32_t m = 0;
	if (state & ShiftMask) m |= EVENTFLAG_SHIFT_DOWN;
	if (state & ControlMask) m |= EVENTFLAG_CONTROL_DOWN;
	if (state & Mod1Mask) m |= EVENTFLAG_ALT_DOWN;
	if (state & Mod4Mask) m |= EVENTFLAG_COMMAND_DOWN;
	if (state & LockMask) m |= EVENTFLAG_CAPS_LOCK_ON;
	if (state & Mod2Mask) m |= EVENTFLAG_NUM_LOCK_ON;
	if (state & Button1Mask) m |= EVENTFLAG_LEFT_MOUSE_BUTTON;
	if (state & Button2Mask) m |= EVENTFLAG_MIDDLE_MOUSE_BUTTON;
	if (state & Button3Mask) m |= EVENTFLAG_RIGHT_MOUSE_BUTTON;
	return m;
}

/* X keysym -> Windows virtual key code, which Chromium's key events use. */
static int vkey_from_keysym(KeySym k)
{
	if (k >= XK_a && k <= XK_z) return 0x41 + (int)(k - XK_a);
	if (k >= XK_A && k <= XK_Z) return 0x41 + (int)(k - XK_A);
	if (k >= XK_0 && k <= XK_9) return 0x30 + (int)(k - XK_0);
	if (k >= XK_KP_0 && k <= XK_KP_9) return 0x60 + (int)(k - XK_KP_0);
	if (k >= XK_F1 && k <= XK_F24) return 0x70 + (int)(k - XK_F1);
	switch (k) {
	case XK_BackSpace: return 0x08;
	case XK_Tab: case XK_ISO_Left_Tab: return 0x09;
	case XK_Return: case XK_KP_Enter: return 0x0D;
	case XK_Shift_L: case XK_Shift_R: return 0x10;
	case XK_Control_L: case XK_Control_R: return 0x11;
	case XK_Alt_L: case XK_Alt_R: return 0x12;
	case XK_Pause: return 0x13;
	case XK_Caps_Lock: return 0x14;
	case XK_Escape: return 0x1B;
	case XK_space: return 0x20;
	case XK_Prior: case XK_KP_Prior: return 0x21;
	case XK_Next: case XK_KP_Next: return 0x22;
	case XK_End: case XK_KP_End: return 0x23;
	case XK_Home: case XK_KP_Home: return 0x24;
	case XK_Left: case XK_KP_Left: return 0x25;
	case XK_Up: case XK_KP_Up: return 0x26;
	case XK_Right: case XK_KP_Right: return 0x27;
	case XK_Down: case XK_KP_Down: return 0x28;
	case XK_Insert: case XK_KP_Insert: return 0x2D;
	case XK_Delete: case XK_KP_Delete: return 0x2E;
	case XK_Super_L: return 0x5B;
	case XK_Super_R: return 0x5C;
	case XK_Menu: return 0x5D;
	case XK_KP_Multiply: return 0x6A;
	case XK_KP_Add: return 0x6B;
	case XK_KP_Subtract: return 0x6D;
	case XK_KP_Decimal: return 0x6E;
	case XK_KP_Divide: return 0x6F;
	case XK_Num_Lock: return 0x90;
	case XK_Scroll_Lock: return 0x91;
	case XK_semicolon: case XK_colon: return 0xBA;
	case XK_equal: case XK_plus: return 0xBB;
	case XK_comma: case XK_less: return 0xBC;
	case XK_minus: case XK_underscore: return 0xBD;
	case XK_period: case XK_greater: return 0xBE;
	case XK_slash: case XK_question: return 0xBF;
	case XK_grave: case XK_asciitilde: return 0xC0;
	case XK_bracketleft: case XK_braceleft: return 0xDB;
	case XK_backslash: case XK_bar: return 0xDC;
	case XK_bracketright: case XK_braceright: return 0xDD;
	case XK_apostrophe: case XK_quotedbl: return 0xDE;
	case XF86XK_AudioMute: return 0xAD;
	case XF86XK_AudioLowerVolume: return 0xAE;
	case XF86XK_AudioRaiseVolume: return 0xAF;
	case XF86XK_AudioNext: return 0xB0;
	case XF86XK_AudioPrev: return 0xB1;
	case XF86XK_AudioStop: return 0xB2;
	case XF86XK_AudioPlay: case XF86XK_AudioPause: return 0xB3;
	}
	return 0;
}

static cef_browser_host_t *host(void)
{
	return as_browser ? as_browser->get_host(as_browser) : NULL;
}

static void release_host(cef_browser_host_t *h)
{
	if (h)
		h->base.release(&h->base);
}

/* UTF-8 (one character) -> UTF-16 code unit, 0 if not in the BMP. */
static char16_t utf16_of(const char *s, int n)
{
	const unsigned char *u = (const unsigned char *)s;
	if (n >= 1 && u[0] < 0x80) return u[0];
	if (n >= 2 && (u[0] & 0xE0) == 0xC0) return (char16_t)(((u[0] & 0x1F) << 6) | (u[1] & 0x3F));
	if (n >= 3 && (u[0] & 0xF0) == 0xE0)
		return (char16_t)(((u[0] & 0x0F) << 12) | ((u[1] & 0x3F) << 6) | (u[2] & 0x3F));
	return 0;
}

static void handle_key(XKeyEvent *ev, int press)
{
	cef_browser_host_t *h = host();
	if (!h)
		return;
	KeySym base_sym = XLookupKeysym(ev, 0);
	char text[16] = {0};
	KeySym sym = NoSymbol;
	int n = 0;
	if (press) {
		Status st;
		n = xic ? Xutf8LookupString(xic, ev, text, sizeof text - 1, &sym, &st)
			: XLookupString(ev, text, sizeof text - 1, &sym, NULL);
		if (n < 0)
			n = 0;
	}
	cef_key_event_t ke = {0};
	ke.size = sizeof ke;
	ke.modifiers = mods_from_state(ev->state);
	ke.windows_key_code = vkey_from_keysym(base_sym);
	ke.native_key_code = (int)ev->keycode;
	if (base_sym >= XK_KP_Space && base_sym <= XK_KP_9)
		ke.modifiers |= EVENTFLAG_IS_KEY_PAD;
	char16_t ch = n > 0 ? utf16_of(text, n) : 0;
	ke.character = ch;
	ke.unmodified_character = (base_sym < 0x100) ? (char16_t)base_sym : ch;

	if (press) {
		ke.type = KEYEVENT_RAWKEYDOWN;
		h->send_key_event(h, &ke);
		/* Text: a CHAR event, unless Ctrl/Alt make it a shortcut. */
		if (ch >= 0x20 && ch != 0x7F && !(ev->state & (ControlMask | Mod1Mask))) {
			ke.type = KEYEVENT_CHAR;
			ke.windows_key_code = ch;
			h->send_key_event(h, &ke);
		} else if (ch == '\r' || ke.windows_key_code == 0x0D) {
			ke.type = KEYEVENT_CHAR;
			ke.windows_key_code = '\r';
			ke.character = ke.unmodified_character = '\r';
			h->send_key_event(h, &ke);
		}
	} else {
		ke.type = KEYEVENT_KEYUP;
		h->send_key_event(h, &ke);
	}
	release_host(h);
}

static void mouse_event(cef_mouse_event_t *me, int x, int y, unsigned state)
{
	me->x = (int)(x / scale());
	me->y = (int)(y / scale());
	me->modifiers = mods_from_state(state);
}

/* ---- smooth scrolling (XInput 2.1 scroll valuators) -------------------
 * Touchpads and many mice report scrolling as fine-grained valuator changes;
 * the core protocol only gets emulated wheel "clicks". Forward the fine
 * deltas like a browser does, and drop the emulated clicks. */

static int xi_opcode = -1;
static Time last_smooth;

typedef struct {
	int dev, number, vertical;
	double increment, last;
	int have_last;
} scroll_axis;
static scroll_axis axes[32];
static int naxes;

static void load_scroll_axes(int dev)
{
	for (int i = 0; i < naxes;)
		if (axes[i].dev == dev) axes[i] = axes[--naxes];
		else i++;
	int n;
	XIDeviceInfo *info = XIQueryDevice(as_dpy, dev, &n);
	if (!info)
		return;
	for (int c = 0; c < info->num_classes && naxes < 32; c++) {
		if (info->classes[c]->type != XIScrollClass)
			continue;
		XIScrollClassInfo *sc = (XIScrollClassInfo *)info->classes[c];
		if (sc->increment == 0)
			continue;
		axes[naxes++] = (scroll_axis){dev, sc->number, sc->scroll_type == XIScrollTypeVertical,
					      sc->increment, 0, 0};
	}
	XIFreeDeviceInfo(info);
}

static void forget_scroll_positions(void)
{
	for (int i = 0; i < naxes; i++)
		axes[i].have_last = 0;
}

static unsigned xi_state(const XIDeviceEvent *e)
{
	unsigned st = (unsigned)e->mods.effective;
	for (int b = 1; b <= 3; b++)
		if (b < e->buttons.mask_len * 8 && XIMaskIsSet(e->buttons.mask, b))
			st |= Button1Mask << (b - 1);
	return st;
}

static void handle_xi_motion(XIDeviceEvent *e)
{
	cef_browser_host_t *h = host();
	if (!h)
		return;
	cef_mouse_event_t me;
	mouse_event(&me, (int)e->event_x, (int)e->event_y, xi_state(e));

	int seen = 0;
	for (int i = 0; i < naxes; i++)
		if (axes[i].dev == e->sourceid)
			seen = 1;
	if (!seen)
		load_scroll_axes(e->sourceid);

	double dx = 0, dy = 0;
	int scrolled = 0;
	double *val = e->valuators.values;
	for (int v = 0; v < e->valuators.mask_len * 8; v++) {
		if (!XIMaskIsSet(e->valuators.mask, v))
			continue;
		double value = *val++;
		for (int i = 0; i < naxes; i++) {
			scroll_axis *a = &axes[i];
			if (a->dev != e->sourceid || a->number != v)
				continue;
			if (a->have_last) {
				/* One increment is one wheel click: 120 like the core path. */
				double d = (value - a->last) / a->increment * 120.0;
				if (a->vertical) dy -= d;
				else dx -= d;
				scrolled = 1;
			}
			a->last = value;
			a->have_last = 1;
		}
	}
	if (scrolled) {
		if (me.modifiers & EVENTFLAG_SHIFT_DOWN && dx == 0) { dx = dy; dy = 0; }
		int ix = (int)lround(dx), iy = (int)lround(dy);
		if (ix || iy)
			h->send_mouse_wheel_event(h, &me, ix, iy);
		last_smooth = e->time;
	} else {
		h->send_mouse_move_event(h, &me, 0);
	}
	release_host(h);
}

static void xi_init(void)
{
	int ev, err, major = 2, minor = 2;
	if (!XQueryExtension(as_dpy, "XInputExtension", &xi_opcode, &ev, &err) ||
	    XIQueryVersion(as_dpy, &major, &minor) != Success || major < 2 || (major == 2 && minor < 1)) {
		xi_opcode = -1;
		return;
	}
	unsigned char bits[XIMaskLen(XI_LASTEVENT)] = {0};
	XISetMask(bits, XI_Motion);
	XISetMask(bits, XI_Enter);
	XISetMask(bits, XI_DeviceChanged);
	XIEventMask m = {.deviceid = XIAllMasterDevices, .mask_len = sizeof bits, .mask = bits};
	XISelectEvents(as_dpy, as_win, &m, 1);
}

static void handle_xi(XGenericEventCookie *c)
{
	switch (c->evtype) {
	case XI_Motion:
		handle_xi_motion(c->data);
		break;
	case XI_Enter:
		forget_scroll_positions();
		break;
	case XI_DeviceChanged:
		load_scroll_axes(((XIDeviceChangedEvent *)c->data)->sourceid);
		break;
	}
}

static void handle_button(XButtonEvent *ev, int press)
{
	static Time last_time;
	static int last_button, last_x, last_y, click_count;
	cef_browser_host_t *h = host();
	if (!h)
		return;
	cef_mouse_event_t me;
	mouse_event(&me, ev->x, ev->y, ev->state);
	if (ev->button >= 4 && ev->button <= 7) {
		/* Emulated from a smooth-scrolling device we already handled. */
		int emulated = last_smooth && ev->time - last_smooth < 250;
		if (press && !emulated) {
			int dx = 0, dy = 0;
			switch (ev->button) {
			case 4: dy = 120; break;
			case 5: dy = -120; break;
			case 6: dx = 120; break;
			case 7: dx = -120; break;
			}
			if (ev->state & ShiftMask && dx == 0) { dx = dy; dy = 0; }
			h->send_mouse_wheel_event(h, &me, dx, dy);
		}
		release_host(h);
		return;
	}
	cef_mouse_button_type_t bt;
	switch (ev->button) {
	case 1: bt = MBT_LEFT; break;
	case 2: bt = MBT_MIDDLE; break;
	case 3: bt = MBT_RIGHT; break;
	case 8: case 9:
		/* Back/forward buttons: history navigation, as in a browser. */
		if (press) {
			if (ev->button == 8) as_browser->go_back(as_browser);
			else as_browser->go_forward(as_browser);
		}
		release_host(h);
		return;
	default:
		release_host(h);
		return;
	}
	if (press) {
		if ((int)ev->button == last_button && ev->time - last_time < 400 &&
		    abs(ev->x - last_x) < 6 && abs(ev->y - last_y) < 6)
			click_count++;
		else
			click_count = 1;
		last_button = (int)ev->button;
		last_time = ev->time;
		last_x = ev->x;
		last_y = ev->y;
	}
	h->send_mouse_click_event(h, &me, bt, !press, click_count);
	release_host(h);
}

void window_handle_events(void)
{
	while (XPending(as_dpy)) {
		XEvent ev;
		XNextEvent(as_dpy, &ev);
		if (XFilterEvent(&ev, None))
			continue;
		switch (ev.type) {
		case ConfigureNotify:
			if (ev.xconfigure.width != as_win_w || ev.xconfigure.height != as_win_h) {
				as_win_w = ev.xconfigure.width;
				as_win_h = ev.xconfigure.height;
				cef_browser_host_t *h = host();
				if (h) {
					h->was_resized(h);
					release_host(h);
				}
				as_request_redraw();
			}
			break;
		case MapNotify:
			mapped = 1;
			update_visible();
			break;
		case UnmapNotify:
			mapped = 0;
			update_visible();
			break;
		case VisibilityNotify:
			obscured = ev.xvisibility.state == VisibilityFullyObscured;
			update_visible();
			break;
		case Expose:
			as_request_redraw();
			break;
		case FocusIn:
		case FocusOut: {
			cef_browser_host_t *h = host();
			if (h) {
				h->set_focus(h, ev.type == FocusIn);
				release_host(h);
			}
			if (xic) {
				if (ev.type == FocusIn) XSetICFocus(xic);
				else XUnsetICFocus(xic);
			}
			break;
		}
		case KeyPress:
		case KeyRelease:
			handle_key(&ev.xkey, ev.type == KeyPress);
			break;
		case ButtonPress:
		case ButtonRelease:
			handle_button(&ev.xbutton, ev.type == ButtonPress);
			break;
		case MotionNotify: {
			/* Only the newest motion matters. */
			while (XCheckTypedWindowEvent(as_dpy, as_win, MotionNotify, &ev))
				;
			cef_browser_host_t *h = host();
			if (h) {
				cef_mouse_event_t me;
				mouse_event(&me, ev.xmotion.x, ev.xmotion.y, ev.xmotion.state);
				h->send_mouse_move_event(h, &me, 0);
				release_host(h);
			}
			break;
		}
		case LeaveNotify: {
			cef_browser_host_t *h = host();
			if (h) {
				cef_mouse_event_t me;
				mouse_event(&me, ev.xcrossing.x, ev.xcrossing.y, ev.xcrossing.state);
				h->send_mouse_move_event(h, &me, 1);
				release_host(h);
			}
			break;
		}
		case ClientMessage:
			if ((Atom)ev.xclient.data.l[0] == wm_delete)
				as_quit();
			break;
		case GenericEvent:
			if (ev.xcookie.extension == xi_opcode && XGetEventData(as_dpy, &ev.xcookie)) {
				handle_xi(&ev.xcookie);
				XFreeEventData(as_dpy, &ev.xcookie);
			}
			break;
		}
	}
}
