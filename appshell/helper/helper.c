/*
 * appshell-helper: the program CEF starts for its child processes (renderer,
 * GPU, network, ...). Kept apart from the Go binary so those processes don't
 * carry the Go runtime or the Seanime backend.
 *
 * In the renderer it adds one native function to the main frame,
 * __appshellPost(json), which forwards the string to the browser process as
 * an "appshell" process message, and then runs shim.js (window.electron and
 * window.mpvPrism) before any page script.
 */

#define CEF_API_VERSION 15400

#include "include/capi/cef_app_capi.h"
#include "include/capi/cef_frame_capi.h"
#include "include/capi/cef_process_message_capi.h"
#include "include/capi/cef_render_process_handler_capi.h"
#include "include/capi/cef_v8_capi.h"
#include "include/cef_api_hash.h"

#include <string.h>

/* shim.js, embedded at build time (SHIM_JS_PATH comes from the build). */
__asm__(".section .rodata\n"
	".global appshell_shim_js\n"
	"appshell_shim_js:\n"
	".incbin \"" SHIM_JS_PATH "\"\n"
	".byte 0\n"
	".previous\n");
extern const char appshell_shim_js[];

/* Objects that live for the whole process: reference counting is a no-op. */
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

static cef_string_t str(const char *s)
{
	cef_string_t out = {0};
	cef_string_utf8_to_utf16(s, strlen(s), &out);
	return out;
}

/* __appshellPost(json): forward to the browser process. */
static int CEF_CALLBACK post_execute(cef_v8_handler_t *self, const cef_string_t *name,
				     cef_v8_value_t *object, size_t argc,
				     cef_v8_value_t *const *argv, cef_v8_value_t **retval,
				     cef_string_t *exception)
{
	(void)self; (void)name; (void)object; (void)retval;
	if (argc < 1 || !argv[0]->is_string(argv[0])) {
		cef_string_t e = str("__appshellPost expects a string");
		*exception = e;
		return 1;
	}
	cef_string_userfree_t json = argv[0]->get_string_value(argv[0]);
	cef_v8_context_t *ctx = cef_v8_context_get_current_context();
	cef_frame_t *frame = ctx ? ctx->get_frame(ctx) : NULL;
	if (frame) {
		cef_string_t msg_name = str("appshell");
		cef_process_message_t *msg = cef_process_message_create(&msg_name);
		cef_string_clear(&msg_name);
		cef_list_value_t *args = msg->get_argument_list(msg);
		args->set_string(args, 0, json);
		RELEASE(args);
		frame->send_process_message(frame, PID_BROWSER, msg); /* takes the message */
		RELEASE(frame);
	}
	RELEASE(ctx);
	if (json)
		cef_string_userfree_free(json);
	return 1;
}

static cef_v8_handler_t post_handler;

static void CEF_CALLBACK on_context_created(cef_render_process_handler_t *self,
					    cef_browser_t *browser, cef_frame_t *frame,
					    cef_v8_context_t *context)
{
	(void)self;
	if (frame->is_main(frame)) {
		cef_v8_value_t *global = context->get_global(context);
		cef_string_t fname = str("__appshellPost");
		cef_v8_value_t *fn = cef_v8_value_create_function(&fname, &post_handler);
		global->set_value_bykey(global, &fname, fn,
					V8_PROPERTY_ATTRIBUTE_READONLY | V8_PROPERTY_ATTRIBUTE_DONTENUM);
		cef_string_clear(&fname);
		RELEASE(global);

		cef_string_t code = str(appshell_shim_js);
		cef_string_t url = str("appshell://shim.js");
		cef_v8_value_t *ret = NULL;
		cef_v8_exception_t *exc = NULL;
		context->eval(context, &code, &url, 1, &ret, &exc);
		RELEASE(ret);
		RELEASE(exc);
		cef_string_clear(&code);
		cef_string_clear(&url);
	}
	RELEASE(browser);
	RELEASE(frame);
	RELEASE(context);
}

static cef_render_process_handler_t render_handler;

static cef_render_process_handler_t *CEF_CALLBACK get_render_process_handler(cef_app_t *self)
{
	(void)self;
	return &render_handler;
}

static cef_app_t app;

int main(int argc, char **argv)
{
	cef_api_hash(CEF_API_VERSION, 0);
	init_base(&post_handler.base, sizeof post_handler);
	post_handler.execute = post_execute;
	init_base(&render_handler.base, sizeof render_handler);
	render_handler.on_context_created = on_context_created;
	init_base(&app.base, sizeof app);
	app.get_render_process_handler = get_render_process_handler;

	cef_main_args_t args = {argc, argv};
	return cef_execute_process(&args, &app, NULL);
}
