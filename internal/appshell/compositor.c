/*
 * compositor.c: draws one frame of the window.
 *
 * Order: clear, the videos (each into its rectangle), the page on top with
 * premultiplied alpha (transparent where the video is), then a popup (a native
 * <select> list) if one is open. CEF's GPU buffers are only valid during its
 * paint callback, so each page frame is copied into a texture of our own
 * there, and drawn whenever anything changes.
 */
#include "appshell.h"

#include <EGL/eglext.h>
#include <GLES2/gl2ext.h>
#include <drm_fourcc.h>

#include <stdio.h>
#include <string.h>

static PFNEGLCREATEIMAGEKHRPROC create_image;
static PFNEGLDESTROYIMAGEKHRPROC destroy_image;
static PFNGLEGLIMAGETARGETTEXTURE2DOESPROC image_target_texture;

static GLuint prog, vao, u_rect, u_tex, import_tex, import_fbo;

typedef struct layer {
	as_target t;
	int valid;
} layer;
static layer page, popup;
static int popup_show, popup_x, popup_y, popup_w, popup_h; /* DIP */

static const char *vs_src =
	"#version 300 es\n"
	"uniform vec4 rect;\n" /* x, y, w, h in NDC, y up */
	"out vec2 uv;\n"
	"void main() {\n"
	"  vec2 p = vec2(float(gl_VertexID & 1), float(gl_VertexID >> 1));\n"
	"  uv = vec2(p.x, 1.0 - p.y);\n" /* texture row 0 is the top of the image */
	"  gl_Position = vec4(rect.xy + p * rect.zw, 0.0, 1.0);\n"
	"}\n";

static const char *fs_src =
	"#version 300 es\n"
	"precision mediump float;\n"
	"in vec2 uv; out vec4 color;\n"
	"uniform sampler2D tex;\n"
	"void main() { color = texture(tex, uv); }\n";

static GLuint compile(GLenum type, const char *src)
{
	GLuint s = glCreateShader(type);
	glShaderSource(s, 1, &src, NULL);
	glCompileShader(s);
	GLint ok;
	glGetShaderiv(s, GL_COMPILE_STATUS, &ok);
	if (!ok) {
		char log[1024];
		glGetShaderInfoLog(s, sizeof log, NULL, log);
		fprintf(stderr, "appshell: shader: %s\n", log);
	}
	return s;
}

int compositor_init(void)
{
	create_image = (PFNEGLCREATEIMAGEKHRPROC)eglGetProcAddress("eglCreateImageKHR");
	destroy_image = (PFNEGLDESTROYIMAGEKHRPROC)eglGetProcAddress("eglDestroyImageKHR");
	image_target_texture = (PFNGLEGLIMAGETARGETTEXTURE2DOESPROC)
		eglGetProcAddress("glEGLImageTargetTexture2DOES");
	if (!create_image || !destroy_image || !image_target_texture) {
		fprintf(stderr, "appshell: EGL image import is not available\n");
		return -1;
	}
	prog = glCreateProgram();
	glAttachShader(prog, compile(GL_VERTEX_SHADER, vs_src));
	glAttachShader(prog, compile(GL_FRAGMENT_SHADER, fs_src));
	glLinkProgram(prog);
	u_rect = (GLuint)glGetUniformLocation(prog, "rect");
	u_tex = (GLuint)glGetUniformLocation(prog, "tex");
	glGenVertexArrays(1, &vao);
	glGenTextures(1, &import_tex);
	glGenFramebuffers(1, &import_fbo);
	return 0;
}

void compositor_target_size(as_target *t, int w, int h)
{
	if (w < 1) w = 1;
	if (h < 1) h = 1;
	if (t->tex && t->w == w && t->h == h)
		return;
	if (!t->tex) {
		glGenTextures(1, &t->tex);
		glGenFramebuffers(1, &t->fbo);
	}
	glBindTexture(GL_TEXTURE_2D, t->tex);
	glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA8, w, h, 0, GL_RGBA, GL_UNSIGNED_BYTE, NULL);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_LINEAR);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_LINEAR);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_S, GL_CLAMP_TO_EDGE);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_T, GL_CLAMP_TO_EDGE);
	glBindFramebuffer(GL_FRAMEBUFFER, t->fbo);
	glFramebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, t->tex, 0);
	glBindFramebuffer(GL_FRAMEBUFFER, 0);
	t->w = w;
	t->h = h;
}

static void free_target(as_target *t)
{
	if (t->tex) {
		glDeleteTextures(1, &t->tex);
		glDeleteFramebuffers(1, &t->fbo);
	}
	memset(t, 0, sizeof *t);
}

/* Draws texture tex over the pixel rectangle (top-left origin). */
static void draw_tex(GLuint tex, int x, int y, int w, int h)
{
	float W = (float)as_win_w, H = (float)as_win_h;
	glUseProgram(prog);
	glBindVertexArray(vao);
	glActiveTexture(GL_TEXTURE0);
	glBindTexture(GL_TEXTURE_2D, tex);
	glUniform1i((GLint)u_tex, 0);
	glUniform4f((GLint)u_rect, 2.0f * x / W - 1.0f, 1.0f - 2.0f * (y + h) / H,
		    2.0f * w / W, 2.0f * h / H);
	glDrawArrays(GL_TRIANGLE_STRIP, 0, 4);
}

void compositor_blit_target(const as_target *t, int x, int y, int w, int h)
{
	glDisable(GL_BLEND);
	draw_tex(t->tex, x, y, w, h);
}

void compositor_page_from_dmabuf(int is_popup, const void *paint_info)
{
	const cef_accelerated_paint_info_t *info = paint_info;
	int w = info->extra.coded_size.width, h = info->extra.coded_size.height;
	if (info->plane_count < 1 || w < 1 || h < 1)
		return;
	uint32_t fourcc = info->format == CEF_COLOR_TYPE_BGRA_8888 ? DRM_FORMAT_ARGB8888
								     : DRM_FORMAT_ABGR8888;
	static const EGLint fd_k[] = {EGL_DMA_BUF_PLANE0_FD_EXT, EGL_DMA_BUF_PLANE1_FD_EXT,
				      EGL_DMA_BUF_PLANE2_FD_EXT, EGL_DMA_BUF_PLANE3_FD_EXT};
	static const EGLint off_k[] = {EGL_DMA_BUF_PLANE0_OFFSET_EXT, EGL_DMA_BUF_PLANE1_OFFSET_EXT,
				       EGL_DMA_BUF_PLANE2_OFFSET_EXT, EGL_DMA_BUF_PLANE3_OFFSET_EXT};
	static const EGLint pitch_k[] = {EGL_DMA_BUF_PLANE0_PITCH_EXT, EGL_DMA_BUF_PLANE1_PITCH_EXT,
					 EGL_DMA_BUF_PLANE2_PITCH_EXT, EGL_DMA_BUF_PLANE3_PITCH_EXT};
	static const EGLint lo_k[] = {EGL_DMA_BUF_PLANE0_MODIFIER_LO_EXT, EGL_DMA_BUF_PLANE1_MODIFIER_LO_EXT,
				      EGL_DMA_BUF_PLANE2_MODIFIER_LO_EXT, EGL_DMA_BUF_PLANE3_MODIFIER_LO_EXT};
	static const EGLint hi_k[] = {EGL_DMA_BUF_PLANE0_MODIFIER_HI_EXT, EGL_DMA_BUF_PLANE1_MODIFIER_HI_EXT,
				      EGL_DMA_BUF_PLANE2_MODIFIER_HI_EXT, EGL_DMA_BUF_PLANE3_MODIFIER_HI_EXT};
	EGLint a[64];
	int n = 0;
	a[n++] = EGL_WIDTH; a[n++] = w;
	a[n++] = EGL_HEIGHT; a[n++] = h;
	a[n++] = EGL_LINUX_DRM_FOURCC_EXT; a[n++] = (EGLint)fourcc;
	for (int i = 0; i < info->plane_count && i < 4; i++) {
		a[n++] = fd_k[i]; a[n++] = info->planes[i].fd;
		a[n++] = off_k[i]; a[n++] = (EGLint)info->planes[i].offset;
		a[n++] = pitch_k[i]; a[n++] = (EGLint)info->planes[i].stride;
		if (info->modifier != DRM_FORMAT_MOD_INVALID) {
			a[n++] = lo_k[i]; a[n++] = (EGLint)(info->modifier & 0xffffffff);
			a[n++] = hi_k[i]; a[n++] = (EGLint)(info->modifier >> 32);
		}
	}
	a[n++] = EGL_NONE;
	EGLImageKHR img = create_image(as_egl_dpy, EGL_NO_CONTEXT, EGL_LINUX_DMA_BUF_EXT, NULL, a);
	if (img == EGL_NO_IMAGE_KHR) {
		fprintf(stderr, "appshell: importing a page buffer failed: 0x%x\n", eglGetError());
		return;
	}
	layer *l = is_popup ? &popup : &page;
	compositor_target_size(&l->t, w, h);
	glBindTexture(GL_TEXTURE_2D, import_tex);
	image_target_texture(GL_TEXTURE_2D, img);
	glBindFramebuffer(GL_READ_FRAMEBUFFER, import_fbo);
	glFramebufferTexture2D(GL_READ_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, import_tex, 0);
	glBindFramebuffer(GL_DRAW_FRAMEBUFFER, l->t.fbo);
	glBlitFramebuffer(0, 0, w, h, 0, 0, w, h, GL_COLOR_BUFFER_BIT, GL_NEAREST);
	glFramebufferTexture2D(GL_READ_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, 0, 0);
	glBindFramebuffer(GL_FRAMEBUFFER, 0);
	destroy_image(as_egl_dpy, img);
	glBindTexture(GL_TEXTURE_2D, l->t.tex); /* undo a software frame's swizzle */
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_SWIZZLE_R, GL_RED);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_SWIZZLE_B, GL_BLUE);
	l->valid = 1;
	as_request_redraw();
}

void compositor_page_from_pixels(int is_popup, const void *bgra, int w, int h)
{
	layer *l = is_popup ? &popup : &page;
	compositor_target_size(&l->t, w, h);
	glBindTexture(GL_TEXTURE_2D, l->t.tex);
	/* GLES has no BGRA upload in core: upload as RGBA and swap in the sampler. */
	glTexSubImage2D(GL_TEXTURE_2D, 0, 0, 0, w, h, GL_RGBA, GL_UNSIGNED_BYTE, bgra);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_SWIZZLE_R, GL_BLUE);
	glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_SWIZZLE_B, GL_RED);
	l->valid = 1;
	as_request_redraw();
}

void compositor_set_popup(int show, int x, int y, int w, int h)
{
	popup_show = show;
	if (w > 0 && h > 0) {
		popup_x = x; popup_y = y; popup_w = w; popup_h = h;
	}
	if (!show)
		popup.valid = 0;
	as_request_redraw();
}

void compositor_draw(void)
{
	glBindFramebuffer(GL_FRAMEBUFFER, 0);
	glViewport(0, 0, as_win_w, as_win_h);
	glDisable(GL_BLEND);
	glClearColor(0, 0, 0, 1);
	glClear(GL_COLOR_BUFFER_BIT);

	player_draw_all();

	glBindFramebuffer(GL_FRAMEBUFFER, 0);
	glViewport(0, 0, as_win_w, as_win_h);
	glEnable(GL_BLEND);
	glBlendFunc(GL_ONE, GL_ONE_MINUS_SRC_ALPHA); /* CEF's pixels are premultiplied */
	if (page.valid)
		draw_tex(page.t.tex, 0, 0, page.t.w, page.t.h);
	if (popup_show && popup.valid) {
		double s = as_scale;
		draw_tex(popup.t.tex, (int)(popup_x * s), (int)(popup_y * s), popup.t.w, popup.t.h);
	}
	glDisable(GL_BLEND);
}

void compositor_destroy(void)
{
	free_target(&page.t);
	free_target(&popup.t);
	if (import_tex)
		glDeleteTextures(1, &import_tex);
	if (import_fbo)
		glDeleteFramebuffers(1, &import_fbo);
	if (prog)
		glDeleteProgram(prog);
}
