/* bridge.h: replies to page requests, shared by bridge.c and player.c. */
#ifndef APPSHELL_BRIDGE_H
#define APPSHELL_BRIDGE_H

#include <stdint.h>
#include "yyjson.h"

typedef struct reply_t {
	yyjson_mut_doc *doc;  /* owns value */
	yyjson_mut_val *value; /* NULL means undefined */
	char error[256];       /* set => the page's promise rejects */
	int64_t id;            /* the request's id (0: no reply wanted) */
	int deferred;          /* the handler replies later with bridge_send_reply */
} reply_t;

/* A late reply: value_json is a JSON value (NULL = undefined), or error. */
void bridge_send_reply(int64_t id, const char *value_json, const char *error);

static inline void reply_error(reply_t *r, const char *msg)
{
	snprintf(r->error, sizeof r->error, "%s", msg);
}

/* args[i] helpers: NULL/0/default when missing or of another type. */
static inline const char *arg_str(yyjson_val *args, size_t i)
{
	return yyjson_get_str(yyjson_arr_get(args, i));
}
static inline double arg_num(yyjson_val *args, size_t i, double def)
{
	yyjson_val *v = yyjson_arr_get(args, i);
	return yyjson_is_num(v) ? yyjson_get_num(v) : def;
}
static inline int arg_bool(yyjson_val *args, size_t i)
{
	yyjson_val *v = yyjson_arr_get(args, i);
	return yyjson_is_bool(v) ? yyjson_get_bool(v) : (yyjson_is_num(v) ? yyjson_get_num(v) != 0 : 0);
}

#endif
