/*
 * shell.c: CEF in the browser process, and the main loop.
 *
 * The loop sleeps in poll() on the X connection and an eventfd that CEF's
 * pump scheduling and mpv's callbacks write to, waking at least every 33 ms
 * for CEF's message loop (see the loop).
 */
#include "appshell.h"

#include "include/capi/cef_browser_process_handler_capi.h"
#include "include/capi/cef_display_handler_capi.h"
#include "include/capi/cef_life_span_handler_capi.h"
#include "include/capi/cef_process_message_capi.h"
#include "include/capi/cef_render_handler_capi.h"
#include "include/cef_api_hash.h"

#include <errno.h>
#include <pthread.h>
#include <signal.h>
#include <poll.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/eventfd.h>
#include <time.h>
#include <unistd.h>

const appshell_opts *as_opts;
cef_browser_t *as_browser;
int as_wake_fd = -1;

static int redraw = 1;
int as_debug; /* APPSHELL_DEBUG=1 */
#define DLOG AS_LOG
static atomic_llong pump_due = 0; /* ms when CEF's work is next due */
static int quitting, closed;
static atomic_int quit_requested; /* appshell_request_quit, any thread */
static volatile sig_atomic_t shot_requested; /* SIGUSR1 with APPSHELL_SHOT=file.ppm */
/* Copied before CEF starts: Chromium reuses the environment's memory for the
 * process title, so later getenv() results can read as garbage. */
static char *env_shot, *env_debug_port;
char as_home[4096], as_runtime_dir[4096];

static void on_sigusr1(int sig)
{
	(void)sig;
	shot_requested = 1;
	as_wake();
}

/* Writes the composited frame (page and video) as a PPM, even when the window
 * or display isn't visible: a debugging aid for automated tests. */
static void write_shot(const char *path)
{
	int w = as_win_w, h = as_win_h;
	unsigned char *px = malloc((size_t)w * h * 4);
	if (!px)
		return;
	glReadPixels(0, 0, w, h, GL_RGBA, GL_UNSIGNED_BYTE, px);
	char tmp[4096];
	snprintf(tmp, sizeof tmp, "%s.tmp", path);
	FILE *f = fopen(tmp, "wb");
	if (f) {
		fprintf(f, "P6\n%d %d\n255\n", w, h);
		for (int y = h - 1; y >= 0; y--)
			for (int x = 0; x < w; x++)
				fwrite(px + ((size_t)y * w + x) * 4, 1, 3, f);
		fclose(f);
		rename(tmp, path);
	}
	free(px);
}

double as_now_ms(void)
{
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return ts.tv_sec * 1e3 + ts.tv_nsec / 1e6;
}

void as_wake(void)
{
	uint64_t one = 1;
	ssize_t r = write(as_wake_fd, &one, sizeof one);
	(void)r;
}

void as_request_redraw(void) { redraw = 1; }

void appshell_request_quit(void)
{
	atomic_store(&quit_requested, 1);
	as_wake();
}

static void *quit_watchdog(void *arg)
{
	(void)arg;
	sleep(3);
	fprintf(stderr, "appshell: shutdown took over 3 s, exiting\n");
	_exit(0);
	return NULL;
}

void as_quit(void)
{
	if (quitting)
		return;
	quitting = 1;
	if (as_browser) {
		cef_browser_host_t *h = as_browser->get_host(as_browser);
		h->close_browser(h, 1);
		h->base.release(&h->base);
	} else {
		closed = 1;
	}
}

/* ---- reference counting: process-lifetime objects ---------------------- */

static void CEF_CALLBACK ref_add(cef_base_ref_counted_t *s) { (void)s; }
static int CEF_CALLBACK ref_release(cef_base_ref_counted_t *s) { (void)s; return 0; }
static int CEF_CALLBACK ref_true(cef_base_ref_counted_t *s) { (void)s; return 1; }

static void init_base(cef_base_ref_counted_t *b, size_t size)
{
	b->size = size;
	b->add_ref = ref_add;
	b->release = ref_release;
	b->has_one_ref = ref_true;
	b->has_at_least_one_ref = ref_true;
}

#define RELEASE(p) do { if (p) (p)->base.release(&(p)->base); } while (0)

/* A browser argument: CEF adds a reference for each callback; give it back.
 * as_browser holds its own reference from on_after_created. */
static void drop_browser(cef_browser_t *b)
{
	RELEASE(b);
}

static cef_string_t str(const char *s)
{
	cef_string_t out = {0};
	cef_string_utf8_to_utf16(s, strlen(s), &out);
	return out;
}

static char *utf8(const cef_string_t *s)
{
	cef_string_utf8_t o = {0};
	if (s)
		cef_string_utf16_to_utf8(s->str, s->length, &o);
	char *r = strdup(o.str ? o.str : "");
	cef_string_utf8_clear(&o);
	return r;
}

void as_page_send_raw(const char *json)
{
	if (!as_browser)
		return;
	cef_frame_t *f = as_browser->get_main_frame(as_browser);
	if (!f)
		return;
	size_t n = strlen(json) + 64;
	char *code = malloc(n);
	snprintf(code, n, "window.__appshellRecv&&window.__appshellRecv(%s)", json);
	cef_string_t c = str(code), u = str("appshell://bridge");
	f->execute_java_script(f, &c, &u, 0);
	cef_string_clear(&c);
	cef_string_clear(&u);
	free(code);
	RELEASE(f);
}

/* ---- render handler -------------------------------------------------- */

static double scale(void) { return as_scale; }

static void CEF_CALLBACK get_view_rect(cef_render_handler_t *self, cef_browser_t *b, cef_rect_t *r)
{
	(void)self;
	r->x = r->y = 0;
	r->width = (int)(as_win_w / scale());
	r->height = (int)(as_win_h / scale());
	if (r->width < 1) r->width = 1;
	if (r->height < 1) r->height = 1;
	drop_browser(b);
}

static int CEF_CALLBACK get_screen_info(cef_render_handler_t *self, cef_browser_t *b, cef_screen_info_t *si)
{
	(void)self;
	int sw = DisplayWidth(as_dpy, DefaultScreen(as_dpy));
	int sh = DisplayHeight(as_dpy, DefaultScreen(as_dpy));
	si->device_scale_factor = (float)scale();
	si->depth = 24;
	si->depth_per_component = 8;
	si->rect = (cef_rect_t){0, 0, (int)(sw / scale()), (int)(sh / scale())};
	si->available_rect = si->rect;
	drop_browser(b);
	return 1;
}

static int CEF_CALLBACK get_screen_point(cef_render_handler_t *self, cef_browser_t *b,
					 int vx, int vy, int *sx, int *sy)
{
	(void)self;
	int x = 0, y = 0;
	Window child;
	XTranslateCoordinates(as_dpy, as_win, DefaultRootWindow(as_dpy), 0, 0, &x, &y, &child);
	*sx = x + (int)(vx * scale());
	*sy = y + (int)(vy * scale());
	drop_browser(b);
	return 1;
}

static void CEF_CALLBACK on_popup_show(cef_render_handler_t *self, cef_browser_t *b, int show)
{
	(void)self;
	compositor_set_popup(show, 0, 0, 0, 0);
	drop_browser(b);
}

static void CEF_CALLBACK on_popup_size(cef_render_handler_t *self, cef_browser_t *b, const cef_rect_t *r)
{
	(void)self;
	compositor_set_popup(1, r->x, r->y, r->width, r->height);
	drop_browser(b);
}

static void CEF_CALLBACK on_paint(cef_render_handler_t *self, cef_browser_t *b,
				  cef_paint_element_type_t type, size_t ndirty,
				  cef_rect_t const *dirty, const void *buffer, int w, int h)
{
	(void)self; (void)ndirty; (void)dirty;
	static long n;
	if (n++ == 0)
		DLOG("first software page frame %dx%d\n", w, h);
	compositor_page_from_pixels(type == PET_POPUP, buffer, w, h);
	drop_browser(b);
}

static void CEF_CALLBACK on_accelerated_paint(cef_render_handler_t *self, cef_browser_t *b,
					      cef_paint_element_type_t type, size_t ndirty,
					      cef_rect_t const *dirty,
					      const cef_accelerated_paint_info_t *info)
{
	(void)self; (void)ndirty; (void)dirty;
	static int lw, lh;
	const cef_accelerated_paint_info_common_t *x = &info->extra;
	if (x->coded_size.width != lw || x->coded_size.height != lh) {
		lw = x->coded_size.width;
		lh = x->coded_size.height;
		DLOG("GPU page frame: coded %dx%d visible %d,%d %dx%d content %d,%d %dx%d source %dx%d(%d) stride %u view %dx%d\n",
		     lw, lh, x->visible_rect.x, x->visible_rect.y, x->visible_rect.width, x->visible_rect.height,
		     x->content_rect.x, x->content_rect.y, x->content_rect.width, x->content_rect.height,
		     x->source_size.width, x->source_size.height, x->has_source_size, info->planes[0].stride,
		     as_win_w, as_win_h);
	}
	compositor_page_from_dmabuf(type == PET_POPUP, info);
	if (as_debug) {
		static long paints;
		static double since;
		paints++;
		if (as_now_ms() - since > 5000) {
			if (since > 0)
				DLOG("page paints: %.1f/s\n", paints * 1000.0 / (as_now_ms() - since));
			since = as_now_ms();
			paints = 0;
		}
	}
	drop_browser(b);
}

static cef_render_handler_t render_handler;

/* ---- display handler ------------------------------------------------- */

static void CEF_CALLBACK on_title_change(cef_display_handler_t *self, cef_browser_t *b,
					 const cef_string_t *title)
{
	(void)self;
	char *t = utf8(title);
	window_set_title(*t ? t : (as_opts->title ? as_opts->title : "appshell"));
	free(t);
	drop_browser(b);
}

static void CEF_CALLBACK on_fullscreen_mode_change(cef_display_handler_t *self, cef_browser_t *b,
						   int fullscreen)
{
	(void)self;
	window_set_fullscreen(fullscreen);
	drop_browser(b);
}

static int CEF_CALLBACK on_cursor_change(cef_display_handler_t *self, cef_browser_t *b,
					 cef_cursor_handle_t cursor, cef_cursor_type_t type,
					 const cef_cursor_info_t *custom)
{
	(void)self; (void)cursor; (void)custom;
	window_set_cursor(type);
	drop_browser(b);
	return 1;
}

/* With APPSHELL_DEBUG, the page's console goes to stderr (repeats collapsed). */
static int CEF_CALLBACK on_console_message(cef_display_handler_t *self, cef_browser_t *b,
					   cef_log_severity_t level, const cef_string_t *message,
					   const cef_string_t *source, int line)
{
	(void)self; (void)level;
	if (as_debug) {
		static char last[512];
		static int repeats;
		char *m = utf8(message), *src = utf8(source);
		char cur[512];
		snprintf(cur, sizeof cur, "%.400s (%s:%d)", m, strrchr(src, '/') ? strrchr(src, '/') + 1 : src, line);
		if (!strcmp(cur, last)) {
			if (++repeats % 100 == 0)
				fprintf(stderr, "appshell: console: (repeated %d times) %s\n", repeats, cur);
		} else {
			if (repeats)
				fprintf(stderr, "appshell: console: (repeated %d times)\n", repeats);
			repeats = 0;
			snprintf(last, sizeof last, "%s", cur);
			fprintf(stderr, "appshell: console: %s\n", cur);
		}
		free(m);
		free(src);
	}
	drop_browser(b);
	return 0;
}

static cef_display_handler_t display_handler;

/* ---- life span ------------------------------------------------------- */

/* Links that open a new window (AniList, trailers, ...) go to Thorium. */
static void open_external(const char *url)
{
	if (!url || (strncmp(url, "http://", 7) && strncmp(url, "https://", 8)))
		return;
	pid_t pid = fork();
	if (pid == 0) {
		setsid();
		if (fork() == 0) {
			char path[4096];
			snprintf(path, sizeof path, "%s/.local/bin/thorium", as_home);
			execl(path, "thorium", url, (char *)NULL);
			_exit(127);
		}
		_exit(0);
	}
}

static int CEF_CALLBACK on_before_popup(cef_life_span_handler_t *self, cef_browser_t *b,
					cef_frame_t *frame, int popup_id, const cef_string_t *url,
					const cef_string_t *name, cef_window_open_disposition_t disp,
					int gesture, const cef_popup_features_t *features,
					cef_window_info_t *wi, cef_client_t **client,
					cef_browser_settings_t *settings, cef_dictionary_value_t **extra,
					int *no_js)
{
	(void)self; (void)popup_id; (void)name; (void)disp; (void)gesture; (void)features;
	(void)wi; (void)client; (void)settings; (void)extra; (void)no_js;
	char *u = utf8(url);
	open_external(u);
	free(u);
	RELEASE(frame);
	drop_browser(b);
	return 1; /* no popup window */
}

static void CEF_CALLBACK on_after_created(cef_life_span_handler_t *self, cef_browser_t *b)
{
	(void)self;
	as_browser = b; /* keeps this reference */
	DLOG("browser created\n");
	cef_browser_host_t *h = b->get_host(b);
	h->set_focus(h, 1);
	RELEASE(h);
}

static int CEF_CALLBACK do_close(cef_life_span_handler_t *self, cef_browser_t *b)
{
	(void)self;
	drop_browser(b);
	return 0;
}

static void CEF_CALLBACK on_before_close(cef_life_span_handler_t *self, cef_browser_t *b)
{
	(void)self;
	drop_browser(b);
	closed = 1;
	as_wake();
}

static cef_life_span_handler_t life_span;

/* ---- client ---------------------------------------------------------- */

static cef_render_handler_t *CEF_CALLBACK get_render_handler(cef_client_t *s) { (void)s; return &render_handler; }
static cef_display_handler_t *CEF_CALLBACK get_display_handler(cef_client_t *s) { (void)s; return &display_handler; }
static cef_life_span_handler_t *CEF_CALLBACK get_life_span_handler(cef_client_t *s) { (void)s; return &life_span; }

static int CEF_CALLBACK on_process_message_received(cef_client_t *self, cef_browser_t *b,
						    cef_frame_t *frame, cef_process_id_t src,
						    cef_process_message_t *msg)
{
	(void)self; (void)src;
	int handled = 0;
	cef_string_userfree_t name = msg->get_name(msg);
	char *n = utf8(name);
	if (!strcmp(n, "appshell") && frame && frame->is_main(frame)) {
		cef_list_value_t *args = msg->get_argument_list(msg);
		cef_string_userfree_t json = args->get_string(args, 0);
		char *j = utf8(json);
		bridge_handle_message(j);
		free(j);
		if (json)
			cef_string_userfree_free(json);
		RELEASE(args);
		handled = 1;
	}
	free(n);
	if (name)
		cef_string_userfree_free(name);
	RELEASE(msg);
	RELEASE(frame);
	drop_browser(b);
	return handled;
}

static cef_client_t client;

/* ---- app ----------------------------------------------------------- */

static void append_switch(cef_command_line_t *cl, const char *name, const char *value)
{
	cef_string_t n = str(name);
	if (value) {
		cef_string_t v = str(value);
		cl->append_switch_with_value(cl, &n, &v);
		cef_string_clear(&v);
	} else {
		cl->append_switch(cl, &n);
	}
	cef_string_clear(&n);
}

static void CEF_CALLBACK on_before_command_line_processing(cef_app_t *self, const cef_string_t *type,
							   cef_command_line_t *cl)
{
	(void)self;
	if (type && type->length)
		return;
	/* One renderer, no zygote, nothing in the background (plan §3.6). */
	append_switch(cl, "no-zygote", NULL);
	append_switch(cl, "renderer-process-limit", "1");
	append_switch(cl, "disable-site-isolation-trials", NULL);
	append_switch(cl, "disable-background-networking", NULL);
	append_switch(cl, "disable-component-update", NULL);
	append_switch(cl, "disable-breakpad", NULL);
	append_switch(cl, "disable-domain-reliability", NULL);
	append_switch(cl, "disable-extensions", NULL);
	append_switch(cl, "disable-spell-checking", NULL);
	append_switch(cl, "no-pings", NULL);
	append_switch(cl, "enable-gpu-rasterization", NULL);
	append_switch(cl, "enable-zero-copy", NULL);
	append_switch(cl, "ignore-gpu-blocklist", NULL);
	append_switch(cl, "disable-features",
		      "Translate,MediaRouter,OptimizationHints,AutofillServerCommunication,"
		      "InterestFeedContentSuggestions,BackForwardCache,SpareRendererForSitePerProcess");
	append_switch(cl, "autoplay-policy", "no-user-gesture-required");
	/* For automated tests only: Chrome DevTools Protocol on 127.0.0.1. */
	if (env_debug_port && *env_debug_port) {
		append_switch(cl, "remote-debugging-address", "127.0.0.1");
		append_switch(cl, "remote-debugging-port", env_debug_port);
		append_switch(cl, "remote-allow-origins", "*");
	}
}

static void CEF_CALLBACK on_schedule_message_pump_work(cef_browser_process_handler_t *self,
						       int64_t delay_ms)
{
	(void)self;
	atomic_store(&pump_due, (long long)(as_now_ms() + (delay_ms > 0 ? (double)delay_ms : 0)));
	as_wake();
}

static cef_browser_process_handler_t browser_process_handler;

static cef_browser_process_handler_t *CEF_CALLBACK get_browser_process_handler(cef_app_t *s)
{
	(void)s;
	return &browser_process_handler;
}

static cef_app_t app;

static void init_objects(void)
{
	init_base(&render_handler.base, sizeof render_handler);
	render_handler.get_view_rect = get_view_rect;
	render_handler.get_screen_info = get_screen_info;
	render_handler.get_screen_point = get_screen_point;
	render_handler.on_popup_show = on_popup_show;
	render_handler.on_popup_size = on_popup_size;
	render_handler.on_paint = on_paint;
	render_handler.on_accelerated_paint = on_accelerated_paint;

	init_base(&display_handler.base, sizeof display_handler);
	display_handler.on_title_change = on_title_change;
	display_handler.on_fullscreen_mode_change = on_fullscreen_mode_change;
	display_handler.on_cursor_change = on_cursor_change;
	display_handler.on_console_message = on_console_message;

	init_base(&life_span.base, sizeof life_span);
	life_span.on_before_popup = on_before_popup;
	life_span.on_after_created = on_after_created;
	life_span.do_close = do_close;
	life_span.on_before_close = on_before_close;

	init_base(&client.base, sizeof client);
	client.get_render_handler = get_render_handler;
	client.get_display_handler = get_display_handler;
	client.get_life_span_handler = get_life_span_handler;
	client.on_process_message_received = on_process_message_received;

	init_base(&browser_process_handler.base, sizeof browser_process_handler);
	browser_process_handler.on_schedule_message_pump_work = on_schedule_message_pump_work;

	init_base(&app.base, sizeof app);
	app.on_before_command_line_processing = on_before_command_line_processing;
	app.get_browser_process_handler = get_browser_process_handler;
}

/* ---- run ------------------------------------------------------------ */

int appshell_run(const appshell_opts *o)
{
	as_opts = o;
	as_debug = getenv("APPSHELL_DEBUG") != NULL;
	snprintf(as_home, sizeof as_home, "%s", getenv("HOME") ? getenv("HOME") : "");
	snprintf(as_runtime_dir, sizeof as_runtime_dir, "%s",
		 getenv("XDG_RUNTIME_DIR") ? getenv("XDG_RUNTIME_DIR") : "/tmp");
	cef_api_hash(CEF_API_VERSION, 0);
	init_objects();
	as_wake_fd = eventfd(0, EFD_NONBLOCK | EFD_CLOEXEC);
	if (getenv("APPSHELL_SHOT")) {
		env_shot = strdup(getenv("APPSHELL_SHOT"));
		signal(SIGUSR1, on_sigusr1);
	}
	if (getenv("APPSHELL_REMOTE_DEBUG"))
		env_debug_port = strdup(getenv("APPSHELL_REMOTE_DEBUG"));

	if (window_init() != 0 || compositor_init() != 0)
		return 1;

	/* No positional arguments: CEF would treat them as URLs to open. */
	static char arg0[] = "media-player";
	static char *argv[] = {arg0, NULL};
	cef_main_args_t args = {1, argv};

	cef_settings_t st = {0};
	st.size = sizeof st;
	st.no_sandbox = 1;
	st.windowless_rendering_enabled = 1;
	st.external_message_pump = 1;
	st.persist_session_cookies = 1;
	st.log_severity = getenv("APPSHELL_VERBOSE") ? LOGSEVERITY_INFO : LOGSEVERITY_ERROR;
	int software = getenv("APPSHELL_SOFTWARE") != NULL;
	st.background_color = 0;
	st.browser_subprocess_path = str(o->helper_path);
	st.resources_dir_path = str(o->resources_dir);
	st.locales_dir_path = str(o->locales_dir);
	st.root_cache_path = str(o->cache_dir);
	st.cache_path = str(o->cache_dir);
	if (!cef_initialize(&args, &st, &app, NULL)) {
		fprintf(stderr, "appshell: cef_initialize failed\n");
		return 1;
	}

	cef_window_info_t wi = {0};
	wi.size = sizeof wi;
	wi.windowless_rendering_enabled = 1;
	wi.shared_texture_enabled = !software;
	wi.parent_window = as_win;
	wi.runtime_style = CEF_RUNTIME_STYLE_ALLOY;
	cef_browser_settings_t bs = {0};
	bs.size = sizeof bs;
	bs.windowless_frame_rate = 120; /* the panel's rate; CEF paints only on change */
	bs.background_color = 0;        /* transparent where the page is */
	cef_string_t url = str(o->url);
	cef_browser_host_create_browser(&wi, &client, &url, &bs, NULL, NULL);
	cef_string_clear(&url);

	int xfd = ConnectionNumber(as_dpy);
	while (!closed) {
		if (atomic_exchange(&quit_requested, 0) && !quitting) {
			pthread_t t;
			if (pthread_create(&t, NULL, quit_watchdog, NULL) == 0)
				pthread_detach(t);
			as_quit();
		}
		window_handle_events();
		/* CEF announces most work through on_schedule_message_pump_work, but
		 * not all of it: like cefclient's external pump, run it at least
		 * every 33 ms (kMaxTimerDelay) whatever was scheduled. */
		long long due = atomic_load(&pump_due);
		double t = as_now_ms();
		if (due <= t) {
			atomic_store(&pump_due, (long long)(t + 33));
			cef_do_message_loop_work();
		}
		player_process_events();
		if (shot_requested && env_shot) {
			shot_requested = 0;
			window_sync_surface_size();
			compositor_draw();
			write_shot(env_shot);
			redraw = 1;
		}
		if (redraw && as_visible) {
			static long frames;
			if (frames++ % 600 == 0)
				DLOG("draw #%ld (%dx%d)\n", frames, as_win_w, as_win_h);
			compositor_draw();
			if (window_swap())
				player_report_swap();
			redraw = 0;
		}
		if (closed)
			break;
		due = atomic_load(&pump_due);
		double d = due - as_now_ms();
		int timeout = d > 0 ? (int)d + 1 : 0;
		if (timeout > 33)
			timeout = 33;
		if (XPending(as_dpy) || (redraw && as_visible))
			timeout = 0;
		struct pollfd p[2] = {{xfd, POLLIN, 0}, {as_wake_fd, POLLIN, 0}};
		if (poll(p, 2, timeout) > 0 && (p[1].revents & POLLIN)) {
			uint64_t v;
			ssize_t r = read(as_wake_fd, &v, sizeof v);
			(void)r;
		}
	}

	player_destroy_all();
	if (as_browser) {
		RELEASE(as_browser);
		as_browser = NULL;
	}
	cef_shutdown();
	compositor_destroy();
	window_destroy();
	return 0;
}
