/*
 * Parser registration.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * The parser table lives in its own translation unit so that each parser only
 * has to replace one stub in parsers/tc_stubs.c. Putting the table in tc_api.c
 * would make every parser edit the same file, which is exactly the kind of
 * conflict that makes parallel work expensive.
 */
#include <stddef.h>
#include <stdlib.h>

#include "tc_internal.h"

/*
 * The table. Entries are grouped by format; the dispatcher rejects a NULL
 * callback for the entry point it was asked for with TC_E_UNSUPPORTED, which is
 * how the not-yet-written parsers report themselves.
 *
 * Terminated by an entry whose format is TC_FMT_AUTO and whose name is NULL.
 */
/* A file entry point for parsers that only implement the memory variant.
 *
 * Without this, `tc_parse_file(path, TC_FMT_OGM, ...)` fails even though the OGM
 * parser exists: the dispatcher looks for a parse_file callback and finds none.
 * Reading the whole file and delegating keeps the memory parser as the single
 * implementation. */
static tc_status tc_parse_file_via_mem(const tc_parser *self, const char *path, tc_data *d) {
    char *raw = NULL;
    size_t len = 0;
    tc_status st = tc_read_file(path, &raw, &len);
    if (st != TC_OK) {
        return st;
    }
    /* The path doubles as the name hint so parsers that key off the extension
     * (XPL derives its default title from the file name) behave the same. */
    st = self->parse_mem(raw, len, path, d);
    free(raw);
    return st;
}

/* Wraps a memory-only parser so the table can expose a file entry point. The
 * wrapper needs to know which parser it belongs to, hence the small trampolines
 * below: one per format, because a C function pointer cannot carry a closure. */
#define TC_FILE_SHIM(fmt, fn)                                     \
    static tc_status fn(const char *path, tc_data *d) {           \
        const tc_parser *self = tc_parser_for(fmt);               \
        return tc_parse_file_via_mem(self, path, d);              \
    }

TC_FILE_SHIM(TC_FMT_OGM, ogm_parse_file_shim)
TC_FILE_SHIM(TC_FMT_VTT, vtt_parse_file_shim)
TC_FILE_SHIM(TC_FMT_XML, xml_parse_file_shim)
TC_FILE_SHIM(TC_FMT_MATROSKA_XML, matroska_parse_file_shim)

static const tc_parser g_parsers[] = {
    /* Text formats parse from memory; the shim gives them a file entry point
     * too, so an explicit `tchapter info <file> ogm` works. CUE and MPLS read
     * the file themselves because their formats depend on the file size. */
    {TC_FMT_OGM,  "ogm",  ogm_parse_file_shim, tc_ogm_parse_mem},
    {TC_FMT_VTT,  "vtt",  vtt_parse_file_shim, tc_vtt_parse_mem},
    {TC_FMT_XML,  "xml",  xml_parse_file_shim, tc_xmlchapters_parse_mem},
    {TC_FMT_XPL,  "xpl",  tc_xpl_parse_file, tc_xpl_parse_mem},
    {TC_FMT_MATROSKA_XML, "matroska", matroska_parse_file_shim, tc_matroska_parse_mem},
    {TC_FMT_CUE,  "cue",  tc_cue_parse_file, tc_cue_parse_mem},
    {TC_FMT_MPLS, "mpls", tc_mpls_parse_file, tc_mpls_parse_mem},
    {TC_FMT_MP4,  "mp4",  tc_mp4_parse_file, NULL},
    {TC_FMT_FLAC, "flac", tc_flac_parse_file, NULL},
    {TC_FMT_TAK,  "tak",  tc_tak_parse_file, NULL},
    {TC_FMT_IFO,  "ifo",  tc_ifo_parse_file, NULL},
    {TC_FMT_BDMV, "bdmv", tc_bdmv_parse_file, NULL},
    {TC_FMT_AUTO, NULL, NULL, NULL},
};

const tc_parser *tc_parsers(void) { return g_parsers; }

const tc_parser *tc_parser_for(tc_format fmt) {
    for (const tc_parser *p = g_parsers; p->name != NULL; p++) {
        if (p->format == fmt) {
            return p;
        }
    }
    return NULL;
}

tc_status tc_not_implemented(tc_format fmt) {
    const char *ext = tc_format_extension(fmt);
    return tc_fail(TC_E_UNSUPPORTED, "the %s parser is not implemented yet",
                   ext[0] ? ext + 1 : "requested");
}
