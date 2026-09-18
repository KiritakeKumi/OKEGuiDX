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

#include "tc_internal.h"

/*
 * The table. Entries are grouped by format; the dispatcher rejects a NULL
 * callback for the entry point it was asked for with TC_E_UNSUPPORTED, which is
 * how the not-yet-written parsers report themselves.
 *
 * Terminated by an entry whose format is TC_FMT_AUTO and whose name is NULL.
 */
static const tc_parser g_parsers[] = {
    /* Text formats can all parse from memory; only CUE and MPLS need a file
     * handle for the size-dependent parts of their formats. */
    {TC_FMT_OGM,  "ogm",  NULL, tc_ogm_parse_mem},
    {TC_FMT_VTT,  "vtt",  NULL, tc_vtt_parse_mem},
    {TC_FMT_XML,  "xml",  NULL, tc_xmlchapters_parse_mem},
    {TC_FMT_XPL,  "xpl",  NULL, tc_xpl_parse_mem},
    {TC_FMT_MATROSKA_XML, "matroska", NULL, tc_matroska_parse_mem},
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
