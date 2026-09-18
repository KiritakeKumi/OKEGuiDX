/*
 * Not-yet-implemented parser stubs.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Every parser that has not been written yet lives here as a single stub, so the
 * library links and the CLI reports a clear "not implemented" error instead of
 * failing to build. Each parser package replaces exactly one function below with
 * a real implementation in its own file, which keeps parallel work free of
 * merge conflicts.
 *
 * When a parser lands, delete its stub from this file and add the new
 * translation unit to the Makefile's source list (the wildcard picks up
 * the parser sources automatically).
 */
#include <stddef.h>

#include "tc_internal.h"

/* --- B2: Blu-ray playlist ------------------------------------------------ */

tc_status tc_mpls_parse_file(const char *path, tc_data *d) {
    (void)path;
    (void)d;
    return tc_not_implemented(TC_FMT_MPLS);
}

tc_status tc_mpls_parse_mem(const void *buf, size_t len, const char *hint, tc_data *d) {
    (void)buf;
    (void)len;
    (void)hint;
    (void)d;
    return tc_not_implemented(TC_FMT_MPLS);
}

/* --- B3: Blu-ray disc structure ------------------------------------------ */

tc_status tc_bdmv_parse_file(const char *path, tc_data *d) {
    (void)path;
    (void)d;
    return tc_not_implemented(TC_FMT_BDMV);
}

/* --- B4: CUE sheet ------------------------------------------------------- */

tc_status tc_cue_parse_file(const char *path, tc_data *d) {
    (void)path;
    (void)d;
    return tc_not_implemented(TC_FMT_CUE);
}

tc_status tc_cue_parse_mem(const void *buf, size_t len, const char *hint, tc_data *d) {
    (void)buf;
    (void)len;
    (void)hint;
    (void)d;
    return tc_not_implemented(TC_FMT_CUE);
}

/* --- B5: FLAC Vorbis comment --------------------------------------------- */

tc_status tc_flac_parse_file(const char *path, tc_data *d) {
    (void)path;
    (void)d;
    return tc_not_implemented(TC_FMT_FLAC);
}

/* --- B6: TAK APE tag ----------------------------------------------------- */

tc_status tc_tak_parse_file(const char *path, tc_data *d) {
    (void)path;
    (void)d;
    return tc_not_implemented(TC_FMT_TAK);
}

/* --- B7: DVD IFO --------------------------------------------------------- */

tc_status tc_ifo_parse_file(const char *path, tc_data *d) {
    (void)path;
    (void)d;
    return tc_not_implemented(TC_FMT_IFO);
}

/* --- B8: MP4 chapter boxes ----------------------------------------------- */

tc_status tc_mp4_parse_file(const char *path, tc_data *d) {
    (void)path;
    (void)d;
    return tc_not_implemented(TC_FMT_MP4);
}

/* --- B9: Matroska chapter XML -------------------------------------------- */

tc_status tc_matroska_parse_mem(const void *buf, size_t len, const char *hint, tc_data *d) {
    (void)buf;
    (void)len;
    (void)hint;
    (void)d;
    return tc_not_implemented(TC_FMT_MATROSKA_XML);
}

/* --- B10: OGM chapter text ----------------------------------------------- */

tc_status tc_ogm_parse_mem(const void *buf, size_t len, const char *hint, tc_data *d) {
    (void)buf;
    (void)len;
    (void)hint;
    (void)d;
    return tc_not_implemented(TC_FMT_OGM);
}

/* --- B11: WebVTT and generic chapter XML --------------------------------- */

tc_status tc_vtt_parse_mem(const void *buf, size_t len, const char *hint, tc_data *d) {
    (void)buf;
    (void)len;
    (void)hint;
    (void)d;
    return tc_not_implemented(TC_FMT_VTT);
}

tc_status tc_xmlchapters_parse_mem(const void *buf, size_t len, const char *hint, tc_data *d) {
    (void)buf;
    (void)len;
    (void)hint;
    (void)d;
    return tc_not_implemented(TC_FMT_XML);
}
