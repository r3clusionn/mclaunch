/*
 * A stand-in libcups.so.2 for the prebuilt CEF.
 *
 * libcef.so links against 68 CUPS functions for printing. CUPS is not
 * installed on the target system and this program never prints, so every
 * function here reports "no printers" or fails cleanly. Functions that return
 * strings return "" where Chromium may copy the result into a std::string, and
 * NULL where NULL means "not found".
 *
 * The list must match `nm -D --undefined-only libcef.so`; check-symbols.sh
 * compares them after every CEF update.
 */

#include <stddef.h>

#define EMPTY ""
#define IPP_INTERNAL_ERROR 0x0500 /* IPP_STATUS_ERROR_INTERNAL */
#define PPD_FILE_OPEN_ERROR 1      /* PPD_FILE_OPEN_ERROR */

#define RET_NULL(name) void *name(void) { return NULL; }
#define RET_ZERO(name) int name(void) { return 0; }
#define RET_VOID(name) void name(void) { }
#define RET_EMPTY(name) const char *name(void) { return EMPTY; }

/* Destinations: there are none. */
int cupsGetDests2(void *http, void **dests)
{
	(void)http;
	if (dests)
		*dests = NULL;
	return 0;
}

int cupsEnumDests(unsigned flags, int msec, int *cancel, unsigned type,
		  unsigned mask, void *cb, void *user_data)
{
	(void)flags; (void)msec; (void)cancel; (void)type; (void)mask;
	(void)cb; (void)user_data;
	return 1; /* finished, nothing enumerated */
}

int cupsLastError(void) { return IPP_INTERNAL_ERROR; }
int ppdLastError(int *line)
{
	if (line)
		*line = 0;
	return PPD_FILE_OPEN_ERROR;
}

RET_NULL(cupsGetDest)
RET_NULL(cupsGetNamedDest)
RET_NULL(cupsCopyDestInfo)
RET_NULL(cupsFindDestDefault)
RET_NULL(cupsFindDestSupported)
RET_NULL(cupsGetOption)
RET_NULL(cupsGetPPD)
RET_NULL(cupsGetPPD2)
RET_NULL(cupsDoRequest)
RET_NULL(cupsGetResponse)
RET_NULL(httpConnect2)
RET_NULL(httpConnectEncrypt)
RET_NULL(ippNew)
RET_NULL(ippNewRequest)
RET_NULL(ippFindAttribute)
RET_NULL(ippFirstAttribute)
RET_NULL(ippNextAttribute)
RET_NULL(ippCopyAttribute)
RET_NULL(ippGetCollection)
RET_NULL(ippGetName)
RET_NULL(ippGetString)
RET_NULL(ippAddBoolean)
RET_NULL(ippAddCollection)
RET_NULL(ippAddCollections)
RET_NULL(ippAddInteger)
RET_NULL(ippAddRange)
RET_NULL(ippAddString)
RET_NULL(ippAddStrings)
RET_NULL(ppdOpenFd)
RET_NULL(ppdFindAttr)
RET_NULL(ppdFindChoice)
RET_NULL(ppdFindMarkedChoice)
RET_NULL(ppdFindOption)

RET_EMPTY(cupsLastErrorString)
RET_EMPTY(cupsServer)
RET_EMPTY(cupsUser)
RET_EMPTY(cupsUserAgent)
RET_EMPTY(cupsLocalizeDestValue)
RET_EMPTY(ppdErrorString)

RET_ZERO(cupsCancelJob2)
RET_ZERO(cupsCheckDestSupported)
RET_ZERO(cupsCopyDest)
RET_ZERO(cupsMarkOptions)
RET_ZERO(cupsRemoveDest)
RET_ZERO(cupsSendRequest)
RET_ZERO(cupsWriteRequestData)
RET_ZERO(ippGetCount)
RET_ZERO(ippGetGroupTag)
RET_ZERO(ippGetInteger)
RET_ZERO(ippGetRange)
RET_ZERO(ippGetResolution)
RET_ZERO(ippGetStatusCode)
RET_ZERO(ippPort)
RET_ZERO(ippSetVersion)
RET_ZERO(ippValidateAttributes)
RET_ZERO(ippDeleteAttribute)
RET_ZERO(httpError)

RET_VOID(cupsFreeDestInfo)
RET_VOID(cupsFreeDests)
RET_VOID(httpClose)
RET_VOID(httpBlocking)
RET_VOID(ippDelete)
RET_VOID(ppdClose)
RET_VOID(ppdMarkDefaults)
