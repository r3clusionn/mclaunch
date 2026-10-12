/* mclaunch has no video: the libmpv player of media-player's appshell is
 * replaced by these no-ops (the window, page compositing and bridge are the
 * same code). */
#include "appshell.h"
#include "bridge.h"

int player_handle_request(const char *ch, void *args, void *reply)
{
	(void)ch;
	(void)args;
	reply_error(reply, "no video player in mclaunch");
	return 0;
}

void player_process_events(void) {}
int player_any_frame_ready(void) { return 0; }
void player_draw_all(void) {}
void player_report_swap(void) {}
int player_any_playing(void) { return 0; }
void player_destroy_all(void) {}
