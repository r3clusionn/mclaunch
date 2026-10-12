/*
 * bridge.c: requests from the page (window.electron and window.mpvPrism in
 * the shim) and replies to them. Messages are JSON: {"id", "ch", "args"} in,
 * {"id", "value"} or {"id", "error"} out; id 0 means no reply is wanted.
 * Events go out as {"event", "args"}.
 *
 * The handlers mirror Seanime Denshi's Electron main process (index.ts,
 * mpv-core.ts) closely enough for the web UI; window.mpvPrism goes to
 * player.c.
 */
#include "appshell.h"
#include "bridge.h"

#include <dirent.h>
#include <errno.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

#define MAX_SUBTITLE_BYTES (20 * 1024 * 1024)
#define MAX_CONFIG_BYTES (1024 * 1024)
#define MAX_SHADERS 512

static int power_blocks; /* open powerSaveBlocker ids */
static int next_power_id = 1;

/* ---- small file helpers ---------------------------------------------- */

static void mkdirs(const char *path)
{
	char tmp[PATH_MAX];
	snprintf(tmp, sizeof tmp, "%s", path);
	for (char *p = tmp + 1; *p; p++) {
		if (*p == '/') {
			*p = 0;
			mkdir(tmp, 0755);
			*p = '/';
		}
	}
	mkdir(tmp, 0755);
}

static void data_path(char *out, size_t n, const char *rel)
{
	snprintf(out, n, "%s/%s", as_opts->data_dir, rel);
}

static int write_file(const char *path, const void *data, size_t len)
{
	FILE *f = fopen(path, "wb");
	if (!f)
		return -1;
	size_t w = fwrite(data, 1, len, f);
	return (fclose(f) == 0 && w == len) ? 0 : -1;
}

static char *read_file(const char *path, size_t *len)
{
	FILE *f = fopen(path, "rb");
	if (!f)
		return NULL;
	fseek(f, 0, SEEK_END);
	long n = ftell(f);
	fseek(f, 0, SEEK_SET);
	char *buf = malloc((size_t)n + 1);
	if (!buf || fread(buf, 1, (size_t)n, f) != (size_t)n) {
		free(buf);
		fclose(f);
		return NULL;
	}
	buf[n] = 0;
	fclose(f);
	if (len)
		*len = (size_t)n;
	return buf;
}

/* A file name the page sent, reduced to a safe base name. */
static void safe_name(char *out, size_t n, const char *in, const char *fallback)
{
	const char *base = in ? strrchr(in, '/') : NULL;
	base = base ? base + 1 : (in ? in : "");
	size_t j = 0;
	for (size_t i = 0; base[i] && j + 1 < n; i++) {
		char c = base[i];
		out[j++] = (c == '/' || c == '\\' || c < 0x20) ? '_' : c;
	}
	out[j] = 0;
	if (!j || !strcmp(out, ".") || !strcmp(out, ".."))
		snprintf(out, n, "%s", fallback);
}

/* ---- Denshi settings (denshi-settings.json in the data directory) ------ */

static const char *settings_defaults =
	"{\"minimizeToTray\":false,\"openInBackground\":false,\"openAtLaunch\":false,"
	"\"updateChannel\":\"none\",\"windowBounds\":null,\"windowMaximized\":true,"
	"\"mpvPrismLogging\":false}";

static yyjson_mut_val *settings_load(yyjson_mut_doc *doc)
{
	yyjson_doc *def = yyjson_read(settings_defaults, strlen(settings_defaults), 0);
	yyjson_mut_val *out = yyjson_val_mut_copy(doc, yyjson_doc_get_root(def));
	yyjson_doc_free(def);
	char path[PATH_MAX];
	data_path(path, sizeof path, "denshi-settings.json");
	size_t len;
	char *saved = read_file(path, &len);
	if (saved) {
		yyjson_doc *sd = yyjson_read(saved, len, 0);
		yyjson_val *root = yyjson_doc_get_root(sd);
		size_t idx, max;
		yyjson_val *k, *v;
		if (yyjson_is_obj(root)) {
			yyjson_obj_foreach(root, idx, max, k, v) {
				yyjson_mut_obj_put(out, yyjson_mut_strcpy(doc, yyjson_get_str(k)),
						   yyjson_val_mut_copy(doc, v));
			}
		}
		yyjson_doc_free(sd);
		free(saved);
	}
	return out;
}

static void settings_set(reply_t *r, yyjson_val *changes)
{
	yyjson_mut_val *cur = settings_load(r->doc);
	size_t idx, max;
	yyjson_val *k, *v;
	if (yyjson_is_obj(changes)) {
		yyjson_obj_foreach(changes, idx, max, k, v) {
			yyjson_mut_obj_put(cur, yyjson_mut_strcpy(r->doc, yyjson_get_str(k)),
					   yyjson_val_mut_copy(r->doc, v));
		}
	}
	char *json = yyjson_mut_val_write(cur, 0, NULL);
	char path[PATH_MAX];
	data_path(path, sizeof path, "denshi-settings.json");
	if (json) {
		write_file(path, json, strlen(json));
		free(json);
	}
	r->value = cur;
}

int bridge_logging_enabled(void)
{
	yyjson_mut_doc *d = yyjson_mut_doc_new(NULL);
	yyjson_mut_val *v = yyjson_mut_obj_get(settings_load(d), "mpvPrismLogging");
	int on = yyjson_mut_is_true(v);
	yyjson_mut_doc_free(d);
	return on;
}

void bridge_log_path(char *out, size_t n)
{
	data_path(out, n, "mpv.log");
}

/* ---- mpvcore helpers --------------------------------------------------- */

typedef struct shader_entry {
	char name[512];
	char path[PATH_MAX];
} shader_entry;

static void walk_shaders(const char *root, const char *dir, int depth,
			 shader_entry *out, int *n)
{
	if (depth > 8 || *n >= MAX_SHADERS)
		return;
	DIR *d = opendir(dir);
	if (!d)
		return;
	struct dirent *e;
	while ((e = readdir(d)) && *n < MAX_SHADERS) {
		if (e->d_name[0] == '.')
			continue;
		char full[PATH_MAX];
		snprintf(full, sizeof full, "%s/%s", dir, e->d_name);
		struct stat st;
		if (stat(full, &st) != 0)
			continue;
		if (S_ISDIR(st.st_mode)) {
			walk_shaders(root, full, depth + 1, out, n);
			continue;
		}
		const char *ext = strrchr(e->d_name, '.');
		if (!S_ISREG(st.st_mode) || !ext || (strcasecmp(ext, ".glsl") && strcasecmp(ext, ".hook")))
			continue;
		snprintf(out[*n].path, sizeof out[*n].path, "%s", full);
		snprintf(out[*n].name, sizeof out[*n].name, "%s", full + strlen(root) + 1);
		(*n)++;
	}
	closedir(d);
}

static int cmp_shader(const void *a, const void *b)
{
	return strcmp(((const shader_entry *)a)->name, ((const shader_entry *)b)->name);
}

static void scan_anime4k(reply_t *r, const char *dir)
{
	char resolved[PATH_MAX];
	if (!dir || !*dir)
		dir = as_opts->anime4k_dir;
	if (!realpath(dir, resolved)) {
		reply_error(r, "Shader path must be a directory");
		return;
	}
	struct stat st;
	if (stat(resolved, &st) != 0 || !S_ISDIR(st.st_mode)) {
		reply_error(r, "Shader path must be a directory");
		return;
	}
	shader_entry *list = calloc(MAX_SHADERS, sizeof *list);
	int n = 0;
	walk_shaders(resolved, resolved, 0, list, &n);
	qsort(list, (size_t)n, sizeof *list, cmp_shader);
	yyjson_mut_val *obj = yyjson_mut_obj(r->doc);
	yyjson_mut_obj_add_strcpy(r->doc, obj, "directory", resolved);
	yyjson_mut_val *arr = yyjson_mut_obj_add_arr(r->doc, obj, "shaders");
	for (int i = 0; i < n; i++) {
		yyjson_mut_val *s = yyjson_mut_arr_add_obj(r->doc, arr);
		yyjson_mut_obj_add_strcpy(r->doc, s, "name", list[i].name);
		yyjson_mut_obj_add_strcpy(r->doc, s, "path", list[i].path);
	}
	free(list);
	r->value = obj;
}

static const unsigned char b64[256] = {
#define B(c, v) [c] = v + 1
	B('A', 0), B('B', 1), B('C', 2), B('D', 3), B('E', 4), B('F', 5), B('G', 6), B('H', 7),
	B('I', 8), B('J', 9), B('K', 10), B('L', 11), B('M', 12), B('N', 13), B('O', 14), B('P', 15),
	B('Q', 16), B('R', 17), B('S', 18), B('T', 19), B('U', 20), B('V', 21), B('W', 22), B('X', 23),
	B('Y', 24), B('Z', 25), B('a', 26), B('b', 27), B('c', 28), B('d', 29), B('e', 30), B('f', 31),
	B('g', 32), B('h', 33), B('i', 34), B('j', 35), B('k', 36), B('l', 37), B('m', 38), B('n', 39),
	B('o', 40), B('p', 41), B('q', 42), B('r', 43), B('s', 44), B('t', 45), B('u', 46), B('v', 47),
	B('w', 48), B('x', 49), B('y', 50), B('z', 51), B('0', 52), B('1', 53), B('2', 54), B('3', 55),
	B('4', 56), B('5', 57), B('6', 58), B('7', 59), B('8', 60), B('9', 61), B('+', 62), B('/', 63),
#undef B
};

static size_t b64_decode(const char *in, unsigned char *out)
{
	const char *comma = strchr(in, ','); /* data:image/png;base64,... */
	if (comma && !strncmp(in, "data:", 5))
		in = comma + 1;
	size_t n = 0;
	unsigned acc = 0;
	int bits = 0;
	for (; *in; in++) {
		unsigned v = b64[(unsigned char)*in];
		if (!v)
			continue;
		acc = (acc << 6) | (v - 1);
		bits += 6;
		if (bits >= 8) {
			bits -= 8;
			out[n++] = (unsigned char)(acc >> bits);
		}
	}
	return n;
}

static void handle_mpvcore(const char *ch, yyjson_val *args, reply_t *r)
{
	char path[PATH_MAX];
	if (!strcmp(ch, "mpvcore:create-temp-subtitle")) {
		const char *content = arg_str(args, 1);
		if (!content) {
			reply_error(r, "Subtitle content must be text");
			return;
		}
		if (strlen(content) > MAX_SUBTITLE_BYTES) {
			reply_error(r, "Subtitle file exceeds the 20 MiB limit");
			return;
		}
		char name[256], dir[PATH_MAX];
		safe_name(name, sizeof name, arg_str(args, 0), "subtitle.ass");
		snprintf(dir, sizeof dir, "%s/media-player-mpvcore", as_runtime_dir);
		mkdirs(dir);
		snprintf(path, sizeof path, "%s/%ld-%s", dir, (long)time(NULL), name);
		if (write_file(path, content, strlen(content)) != 0) {
			reply_error(r, strerror(errno));
			return;
		}
		r->value = yyjson_mut_strcpy(r->doc, path);
	} else if (!strcmp(ch, "mpvcore:write-config-file")) {
		const char *content = arg_str(args, 0);
		if (!content) {
			reply_error(r, "MPV config must be text");
			return;
		}
		char dir[PATH_MAX];
		data_path(dir, sizeof dir, "mpvcore");
		snprintf(path, sizeof path, "%s/mpv.conf", dir);
		const char *p = content;
		while (*p == ' ' || *p == '\n' || *p == '\t' || *p == '\r')
			p++;
		if (!*p) {
			unlink(path);
			r->value = yyjson_mut_null(r->doc);
			return;
		}
		if (strlen(content) > MAX_CONFIG_BYTES) {
			reply_error(r, "MPV config exceeds the 1 MiB limit");
			return;
		}
		mkdirs(dir);
		if (write_file(path, content, strlen(content)) != 0) {
			reply_error(r, strerror(errno));
			return;
		}
		r->value = yyjson_mut_strcpy(r->doc, path);
	} else if (!strcmp(ch, "mpvcore:create-screenshot-path")) {
		char dir[PATH_MAX];
		data_path(dir, sizeof dir, "screenshots");
		mkdirs(dir);
		struct timespec ts;
		clock_gettime(CLOCK_REALTIME, &ts);
		snprintf(path, sizeof path, "%s/seanime-%ld-%03ld.png", dir, (long)ts.tv_sec,
			 ts.tv_nsec / 1000000);
		r->value = yyjson_mut_strcpy(r->doc, path);
	} else if (!strcmp(ch, "mpvcore:save-screenshot")) {
		const char *target = arg_str(args, 0), *data = arg_str(args, 1);
		char dir[PATH_MAX], real_dir[PATH_MAX], real_target_dir[PATH_MAX];
		data_path(dir, sizeof dir, "screenshots");
		mkdirs(dir);
		/* Only into the screenshots folder. */
		snprintf(path, sizeof path, "%s", target ? target : "");
		char *slash = strrchr(path, '/');
		if (!target || !data || !slash || !realpath(dir, real_dir)) {
			reply_error(r, "invalid screenshot");
			return;
		}
		*slash = 0;
		if (!realpath(path, real_target_dir) || strcmp(real_dir, real_target_dir)) {
			reply_error(r, "screenshots are saved in the screenshots folder only");
			return;
		}
		unsigned char *bytes = malloc(strlen(data));
		size_t n = b64_decode(data, bytes);
		int ok = write_file(target, bytes, n) == 0;
		free(bytes);
		if (!ok) {
			reply_error(r, strerror(errno));
			return;
		}
		r->value = yyjson_mut_true(r->doc);
	} else if (!strcmp(ch, "mpvcore:setLoggingEnabled")) {
		const char *js = arg_bool(args, 0) ? "{\"mpvPrismLogging\":true}" : "{\"mpvPrismLogging\":false}";
		yyjson_doc *cd = yyjson_read(js, strlen(js), 0);
		reply_t s = {.doc = r->doc};
		settings_set(&s, yyjson_doc_get_root(cd));
		yyjson_doc_free(cd);
		r->value = yyjson_mut_bool(r->doc, arg_bool(args, 0));
	} else if (!strcmp(ch, "mpvcore:export-logs")) {
		data_path(path, sizeof path, "mpv.log");
		r->value = access(path, R_OK) == 0 ? yyjson_mut_strcpy(r->doc, path) : yyjson_mut_null(r->doc);
	} else if (!strcmp(ch, "mpvcore:get-anime4k-directory")) {
		scan_anime4k(r, NULL);
	} else if (!strcmp(ch, "mpvcore:scan-anime4k-directory")) {
		scan_anime4k(r, arg_str(args, 0));
	} else if (!strcmp(ch, "mpvcore:open-anime4k-directory")) {
		/* No file manager on this system: nothing to open. */
		r->value = yyjson_mut_false(r->doc);
	} else {
		reply_error(r, "unknown mpvcore call");
	}
}

/* ---- clipboard: xclip (already installed) owns the selection ----------- */

static int clipboard_write(const char *text)
{
	int fds[2];
	if (!text || pipe(fds) != 0)
		return 0;
	pid_t pid = fork();
	if (pid < 0) {
		close(fds[0]);
		close(fds[1]);
		return 0;
	}
	if (pid == 0) {
		dup2(fds[0], 0);
		close(fds[0]);
		close(fds[1]);
		execlp("xclip", "xclip", "-selection", "clipboard", "-silent", (char *)NULL);
		_exit(127);
	}
	close(fds[0]);
	ssize_t w = write(fds[1], text, strlen(text));
	close(fds[1]);
	/* xclip forks to serve the selection; the first process exits at once. */
	int status;
	waitpid(pid, &status, 0);
	return w == (ssize_t)strlen(text) && WIFEXITED(status) && WEXITSTATUS(status) == 0;
}

/* ---- dispatch ------------------------------------------------------- */

static void handle(const char *ch, yyjson_val *args, reply_t *r)
{
	if (!strncmp(ch, "mpv-prism:", 10)) {
		player_handle_request(ch, args, r);
	} else if (!strncmp(ch, "mpvcore:", 8)) {
		handle_mpvcore(ch, args, r);
	} else if (!strcmp(ch, "window:minimize") || !strcmp(ch, "window:hide")) {
		window_minimize();
	} else if (!strcmp(ch, "window:close") || !strcmp(ch, "quit-app") || !strcmp(ch, "restart-app")) {
		as_quit();
	} else if (!strcmp(ch, "window:maximize") || !strcmp(ch, "window:toggleMaximize") ||
		   !strcmp(ch, "window:show") || !strcmp(ch, "startup:renderer-ready")) {
		/* dwm manages the window's size; nothing to do. */
	} else if (!strcmp(ch, "window:isMaximized")) {
		r->value = yyjson_mut_bool(r->doc, !as_fullscreen);
	} else if (!strcmp(ch, "window:isFullscreen")) {
		r->value = yyjson_mut_bool(r->doc, as_fullscreen);
	} else if (!strcmp(ch, "window:setFullscreen")) {
		window_set_fullscreen(arg_bool(args, 0));
	} else if (!strcmp(ch, "window:isVisible")) {
		r->value = yyjson_mut_bool(r->doc, as_visible);
	} else if (!strcmp(ch, "get-local-server-port")) {
		r->value = yyjson_mut_int(r->doc, as_opts->server_port);
	} else if (!strcmp(ch, "kill-server")) {
		r->value = yyjson_mut_true(r->doc);
		as_quit();
	} else if (!strcmp(ch, "clipboard:writeText")) {
		r->value = yyjson_mut_bool(r->doc, clipboard_write(arg_str(args, 0)));
	} else if (!strcmp(ch, "denshi:getSettings")) {
		r->value = settings_load(r->doc);
	} else if (!strcmp(ch, "denshi:setSettings")) {
		settings_set(r, yyjson_arr_get(args, 0));
	} else if (!strcmp(ch, "power-save-blocker:start")) {
		power_blocks++;
		window_inhibit_screensaver(1);
		r->value = yyjson_mut_int(r->doc, next_power_id++);
	} else if (!strcmp(ch, "power-save-blocker:stop")) {
		if (power_blocks > 0 && --power_blocks == 0 && !player_any_playing())
			window_inhibit_screensaver(0);
		r->value = yyjson_mut_true(r->doc);
	} else if (!strcmp(ch, "restart-server") || !strcmp(ch, "macos-activation-policy-accessory") ||
		   !strcmp(ch, "macos-activation-policy-regular")) {
		/* not applicable */
	} else {
		reply_error(r, "not supported by appshell");
	}
}

void bridge_handle_message(const char *json)
{
	yyjson_doc *doc = yyjson_read(json, strlen(json), 0);
	yyjson_val *root = yyjson_doc_get_root(doc);
	if (!yyjson_is_obj(root)) {
		yyjson_doc_free(doc);
		return;
	}
	int64_t id = yyjson_get_sint(yyjson_obj_get(root, "id"));
	const char *ch = yyjson_get_str(yyjson_obj_get(root, "ch"));
	yyjson_val *args = yyjson_obj_get(root, "args");
	reply_t r = {.doc = yyjson_mut_doc_new(NULL), .id = id};
	if (ch)
		handle(ch, args, &r);
	else
		reply_error(&r, "missing channel");

	if (id > 0 && !r.deferred) {
		yyjson_mut_val *out = yyjson_mut_obj(r.doc);
		yyjson_mut_obj_add_int(r.doc, out, "id", id);
		if (r.error[0])
			yyjson_mut_obj_add_str(r.doc, out, "error", r.error);
		else if (r.value)
			yyjson_mut_obj_add_val(r.doc, out, "value", r.value);
		yyjson_mut_doc_set_root(r.doc, out);
		char *s = yyjson_mut_write(r.doc, 0, NULL);
		if (s) {
			as_page_send_raw(s);
			free(s);
		}
	} else if (r.error[0] && ch) {
		fprintf(stderr, "appshell: %s: %s\n", ch, r.error);
	}
	yyjson_mut_doc_free(r.doc);
	yyjson_doc_free(doc);
}

void bridge_send_reply(int64_t id, const char *value_json, const char *error)
{
	if (id <= 0)
		return;
	yyjson_mut_doc *d = yyjson_mut_doc_new(NULL);
	yyjson_mut_val *o = yyjson_mut_obj(d);
	yyjson_mut_obj_add_int(d, o, "id", id);
	if (error) {
		yyjson_mut_obj_add_strcpy(d, o, "error", error);
	} else if (value_json) {
		yyjson_doc *v = yyjson_read(value_json, strlen(value_json), 0);
		if (v)
			yyjson_mut_obj_add_val(d, o, "value", yyjson_val_mut_copy(d, yyjson_doc_get_root(v)));
		yyjson_doc_free(v);
	}
	yyjson_mut_doc_set_root(d, o);
	char *s = yyjson_mut_write(d, 0, NULL);
	if (s) {
		as_page_send_raw(s);
		free(s);
	}
	yyjson_mut_doc_free(d);
}

void bridge_send_event(const char *channel, const char *args_json)
{
	yyjson_mut_doc *d = yyjson_mut_doc_new(NULL);
	yyjson_mut_val *o = yyjson_mut_obj(d);
	yyjson_mut_obj_add_str(d, o, "event", channel);
	yyjson_doc *a = args_json ? yyjson_read(args_json, strlen(args_json), 0) : NULL;
	if (a)
		yyjson_mut_obj_add_val(d, o, "args", yyjson_val_mut_copy(d, yyjson_doc_get_root(a)));
	yyjson_mut_doc_set_root(d, o);
	char *s = yyjson_mut_write(d, 0, NULL);
	if (s) {
		as_page_send_raw(s);
		free(s);
	}
	yyjson_doc_free(a);
	yyjson_mut_doc_free(d);
}
