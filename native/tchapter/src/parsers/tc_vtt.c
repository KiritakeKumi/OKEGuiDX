/*
 * WebVTT chapter parser (B11).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Ported from TChapter.Parsing.VTTParser.GetChapterInfo. The reference removes
 * every CR, splits the text on blank lines (Regex.Split(text, "\n\n"), which
 * keeps empty blocks), requires "WEBVTT" somewhere in the first block and then
 * reads one chapter per remaining block:
 *
 *   - the block is split into lines and everything before the first line that
 *     contains "-->" is discarded (this is where a cue identifier goes);
 *   - that line is split on every "-->" and each field is parsed as a time
 *     code, but only the first one - the cue's start - becomes the chapter
 *     time;
 *   - the line directly after the time line becomes the chapter name, verbatim.
 *
 * The cue's end time is therefore parsed but never used: the chapter time is the
 * cue's own start time, not its end time and not the next cue's start. A cue
 * without an end time still fails, because the reference parses the empty field
 * and TimeSpan.Parse rejects it.
 *
 * The reference always produces a single ChapterInfo, so one entry is returned
 * even when the file contains no cue at all.
 */
#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include "tc_internal.h"

/* ------------------------------------------------------------------ */
/* Text scanning                                                      */
/* ------------------------------------------------------------------ */

/* Finds the first occurrence of `needle` in [p, end). */
static const char *vtt_find(const char *p, const char *end, const char *needle,
                            size_t needle_len) {
    size_t len = (size_t)(end - p);
    if (needle_len == 0 || len < needle_len) {
        return NULL;
    }
    for (size_t i = 0; i + needle_len <= len; i++) {
        if (memcmp(p + i, needle, needle_len) == 0) {
            return p + i;
        }
    }
    return NULL;
}

typedef struct vtt_line {
    const char *p;
    const char *end;
} vtt_line;

/* Splits off the next '\n'-terminated line; the terminator is not part of it.
 * The last line of a block needs no terminator. */
static vtt_line vtt_next_line(const char **cursor, const char *end) {
    const char *p = *cursor;
    const char *nl = memchr(p, '\n', (size_t)(end - p));
    vtt_line line;
    line.p = p;
    line.end = nl ? nl : end;
    *cursor = nl ? nl + 1 : end;
    return line;
}

/* ------------------------------------------------------------------ */
/* Time codes                                                         */
/* ------------------------------------------------------------------ */

static int vtt_is_digit(char c) {
    return c >= '0' && c <= '9';
}

/* The reference parses each field with TimeSpan.Parse, which is stricter than
 * the regex-based path the other text formats use:
 *
 *   - the decimal separator is '.', never ',';
 *   - the fraction has at most seven digits (100 ns ticks);
 *   - the hour field must stay below 24 and the minute and second fields below
 *     60, otherwise TimeSpan.Parse overflows;
 *   - a leading '-' negates the value.
 *
 * The field's shape is validated first, so a malformed value cannot reach
 * tc_parse_timestamp's integer accumulation with an absurd digit run. The
 * day-based forms ("1.02:03:04") TimeSpan.Parse also accepts are not used by
 * WebVTT and are rejected here. */
static tc_status vtt_parse_time(const char *p, const char *end, int64_t *out_ns) {
    while (p < end && (*p == ' ' || *p == '\t')) {
        p++;
    }
    while (end > p && (end[-1] == ' ' || end[-1] == '\t')) {
        end--;
    }
    if (p == end) {
        return tc_fail(TC_E_FORMAT, "vtt: empty cue time");
    }

    int neg = 0;
    if (*p == '-') {
        neg = 1;
        p++;
        if (p == end) {
            return tc_fail(TC_E_FORMAT, "vtt: cue time is only a sign");
        }
    }

    char stack[64];
    size_t n = (size_t)(end - p);
    char *tmp = n < sizeof(stack) ? stack : tc_malloc(n + 1);
    if (!tmp) {
        return TC_E_NOMEM;
    }
    memcpy(tmp, p, n);
    tmp[n] = '\0';

    /* Hours. A run of more than two digits is fine as long as its value stays
     * below 24 ("000:00:26" is valid), so the cap is checked while scanning. */
    size_t i = 0;
    int64_t hours = 0;
    while (i < n && vtt_is_digit(tmp[i])) {
        hours = hours * 10 + (tmp[i] - '0');
        if (hours > 23) {
            goto bad;
        }
        i++;
    }
    if (i == 0 || i >= n || tmp[i] != ':') {
        goto bad;
    }

    /* Minutes and seconds; both must stay below 60, and the seconds may be
     * followed by the end of the field. */
    for (int field = 0; field < 2; field++) {
        i++;
        int64_t value = 0;
        size_t digits = 0;
        while (i < n && vtt_is_digit(tmp[i])) {
            value = value * 10 + (tmp[i] - '0');
            if (value > 59) {
                goto bad;
            }
            digits++;
            i++;
        }
        if (digits == 0) {
            goto bad;
        }
        if (field == 0 && (i >= n || tmp[i] != ':')) {
            goto bad;
        }
    }

    /* Optional fraction of at most seven digits. */
    if (i < n && tmp[i] == '.') {
        i++;
        size_t digits = 0;
        while (i < n && vtt_is_digit(tmp[i])) {
            digits++;
            i++;
        }
        if (digits == 0 || digits > 7) {
            goto bad;
        }
    }
    if (i != n) {
        goto bad;
    }

    tc_status st = tc_parse_timestamp(tmp, out_ns);
    if (st != TC_OK) {
        goto bad;
    }
    if (neg) {
        *out_ns = -*out_ns;
    }
    if (tmp != stack) {
        free(tmp);
    }
    return TC_OK;

bad:
    st = tc_fail(TC_E_FORMAT, "vtt: invalid cue time \"%s\"", tmp);
    if (tmp != stack) {
        free(tmp);
    }
    return st;
}

/* Reads the cue's start time. The reference splits the time line on every
 * "-->" and parses each field with TimeSpan.Parse, so a malformed end time
 * fails the file even though only the start is kept. */
static tc_status vtt_time_line(const char *p, const char *end, int64_t *out_ns) {
    const char *arrow = vtt_find(p, end, "-->", 3);
    int64_t start_ns = 0;
    tc_status st = vtt_parse_time(p, arrow ? arrow : end, &start_ns);
    if (st != TC_OK) {
        return st;
    }

    while (arrow) {
        const char *part = arrow + 3;
        arrow = vtt_find(part, end, "-->", 3);
        int64_t ignored = 0;
        st = vtt_parse_time(part, arrow ? arrow : end, &ignored);
        if (st != TC_OK) {
            return st;
        }
    }

    *out_ns = start_ns;
    return TC_OK;
}

/* ------------------------------------------------------------------ */
/* Cue blocks                                                         */
/* ------------------------------------------------------------------ */

/* Turns one blank-line-delimited block into a chapter. */
static tc_status vtt_add_block(const char *p, const char *end, tc_entry *e) {
    /* Everything before the first line containing "-->" is a cue identifier
     * and is discarded. */
    const char *cursor = p;
    const char *time_p = NULL;
    const char *time_end = NULL;
    while (cursor < end) {
        vtt_line line = vtt_next_line(&cursor, end);
        if (vtt_find(line.p, line.end, "-->", 3)) {
            time_p = line.p;
            time_end = line.end;
            break;
        }
    }

    /* The reference requires the time line plus at least one more line. */
    if (!time_p) {
        return tc_fail(TC_E_FORMAT, "vtt: block without a cue time line");
    }
    if (time_end == end) {
        return tc_fail(TC_E_FORMAT, "vtt: cue without a name line");
    }

    int64_t ns = 0;
    tc_status st = vtt_time_line(time_p, time_end, &ns);
    if (st != TC_OK) {
        return st;
    }

    /* The name is the next line, verbatim: the reference does not trim it. */
    const char *name_p = time_end + 1;
    const char *nl = memchr(name_p, '\n', (size_t)(end - name_p));
    const char *name_end = nl ? nl : end;
    char *name = tc_strndup(name_p, (size_t)(name_end - name_p));
    if (!name) {
        return TC_E_NOMEM;
    }
    return tc_entry_add_chapter(e, name, ns, -1);
}

/* ------------------------------------------------------------------ */
/* Parser                                                             */
/* ------------------------------------------------------------------ */

tc_status tc_vtt_parse_mem(const void *buf, size_t len, const char *hint, tc_data *d) {
    (void)hint;
    if (!buf || !d) {
        return tc_fail(TC_E_INVALID, "vtt: null argument");
    }

    const char *src = (const char *)buf;
    size_t src_len = len;

    /* StreamReader consumes a UTF-8 BOM before ReadToEnd. */
    if (src_len >= 3 && (unsigned char)src[0] == 0xEF && (unsigned char)src[1] == 0xBB &&
        (unsigned char)src[2] == 0xBF) {
        src += 3;
        src_len -= 3;
    }

    /* text.Replace("\r", ""): every CR disappears, including the CR of a CRLF
     * pair and any CR inside a name. */
    char *text = tc_malloc(src_len + 1);
    if (!text) {
        return TC_E_NOMEM;
    }
    size_t n = 0;
    for (size_t i = 0; i < src_len; i++) {
        if (src[i] != '\r') {
            text[n++] = src[i];
        }
    }
    text[n] = '\0';
    const char *end = text + n;

    /* The first block must mention WEBVTT; the reference searches the block
     * before the first blank line, not the whole file. */
    const char *first_sep = vtt_find(text, end, "\n\n", 2);
    const char *first_end = first_sep ? first_sep : end;
    if (!vtt_find(text, first_end, "WEBVTT", 6)) {
        free(text);
        return tc_fail(TC_E_FORMAT, "vtt: empty or invalid file type");
    }

    /* The reference's SingleChapterData always holds one ChapterInfo. */
    tc_entry *e = tc_data_add_entry(d);
    if (!e) {
        free(text);
        return TC_E_NOMEM;
    }

    if (first_sep) {
        const char *block = first_sep + 2;
        for (;;) {
            const char *sep = vtt_find(block, end, "\n\n", 2);
            const char *block_end = sep ? sep : end;
            tc_status st = vtt_add_block(block, block_end, e);
            if (st != TC_OK) {
                free(text);
                return st;
            }
            if (!sep) {
                break;
            }
            block = sep + 2;
        }
    }

    free(text);
    return TC_OK;
}
