/*
 * OGM chapter text parser (B10).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Ported from TChapter.Parsing.OGMParser. The input is a text file of
 * CHAPTERnn / CHAPTERnnNAME line pairs and the reference state machine is kept
 * as it is:
 *
 *     LTimeCode --time line--> LName --name line--> LTimeCode
 *
 * The first line must be a time line. Afterwards a line that does not fit the
 * state stops the scan and keeps the chapters parsed so far, unless none were
 * parsed at all, which is a format error. Chapter times are rebased on the
 * first chapter, so the first chapter always starts at zero.
 *
 * The reference splits the text on '\n' only, so a line may still carry the
 * '\r' of a CRLF pair; that '\r' is stripped from chapter names, exactly like
 * the reference's Trim('\r').
 */
#include <stddef.h>
#include <stdint.h>
#include <string.h>

#include "tc_internal.h"

/* ------------------------------------------------------------------ */
/* Line scanning                                                      */
/* ------------------------------------------------------------------ */

typedef struct ogm_line {
    const char *p;
    const char *end;
} ogm_line;

typedef enum ogm_state {
    OGM_LTIMECODE = 0,
    OGM_LNAME,
    OGM_LERROR,
    OGM_LFIN
} ogm_state;

/* string.IsNullOrWhiteSpace, restricted to the ASCII whitespace these files
 * can contain. */
static int ogm_is_space(char c) {
    return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f';
}

static int ogm_is_blank(const char *p, const char *end) {
    for (; p < end; p++) {
        if (!ogm_is_space(*p)) {
            return 0;
        }
    }
    return 1;
}

/* Splits off the next '\n'-terminated line and advances the cursor. The last
 * line needs no terminator; the '\n' itself is not part of the line, while the
 * '\r' of a CRLF pair is (the reference splits the same way). */
static ogm_line ogm_next_line(const char **cursor, const char *end) {
    const char *p = *cursor;
    const char *nl = memchr(p, '\n', (size_t)(end - p));
    ogm_line line;
    line.p = p;
    line.end = nl ? nl : end;
    *cursor = nl ? nl + 1 : end;
    return line;
}

/* Matches ^\s*CHAPTER\d+ and returns the position after the digits, or NULL. */
static const char *ogm_skip_chapter_number(const char *p, const char *end) {
    while (p < end && ogm_is_space(*p)) {
        p++;
    }
    if ((size_t)(end - p) < 8 || memcmp(p, "CHAPTER", 7) != 0) {
        return NULL;
    }
    p += 7;
    const char *digits = p;
    while (p < end && *p >= '0' && *p <= '9') {
        p++;
    }
    return p == digits ? NULL : p;
}

/* Matches ^\s*CHAPTER\d+\s*=, the reference's RTimeCodeLine shape. */
static int ogm_match_time_line(const ogm_line *line) {
    const char *p = ogm_skip_chapter_number(line->p, line->end);
    if (!p) {
        return 0;
    }
    while (p < line->end && ogm_is_space(*p)) {
        p++;
    }
    return p < line->end && *p == '=';
}

/* Matches ^\s*CHAPTER\d+NAME\s*=\s*(?<chapterName>.*) and returns the name
 * range. A trailing '\r' is removed, like the reference's Trim('\r'). */
static int ogm_match_name_line(const ogm_line *line, const char **name, size_t *name_len) {
    const char *p = ogm_skip_chapter_number(line->p, line->end);
    if (!p || (size_t)(line->end - p) < 4 || memcmp(p, "NAME", 4) != 0) {
        return 0;
    }
    p += 4;
    while (p < line->end && ogm_is_space(*p)) {
        p++;
    }
    if (p >= line->end || *p != '=') {
        return 0;
    }
    p++;
    while (p < line->end && ogm_is_space(*p)) {
        p++;
    }

    const char *nend = line->end;
    while (nend > p && nend[-1] == '\r') {
        nend--;
    }
    *name = p;
    *name_len = (size_t)(nend - p);
    return 1;
}

/* ------------------------------------------------------------------ */
/* Time codes                                                         */
/* ------------------------------------------------------------------ */

/* A matched time code, as the component ranges the reference's named groups
 * capture. */
typedef struct ogm_timecode {
    const char *hour;
    size_t hour_len;
    const char *minute;
    size_t minute_len;
    const char *second;
    size_t second_len;
    const char *frac;
    size_t frac_len;
} ogm_timecode;

typedef enum ogm_time_status {
    OGM_TIME_OK = 0,   /* a time code was read */
    OGM_TIME_NONE,     /* the line has none; the reference uses zero there */
    OGM_TIME_OVERFLOW  /* a component does not fit the reference's integer */
} ogm_time_status;

static const char *ogm_skip_ws(const char *p, const char *end) {
    while (p < end && ogm_is_space(*p)) {
        p++;
    }
    return p;
}

/* Advances over a digit run, recording it. The run may be empty. */
static void ogm_scan_digits(const char **pp, const char *end, const char **start,
                            size_t *len) {
    const char *p = *pp;
    *start = p;
    while (p < end && *p >= '0' && *p <= '9') {
        p++;
    }
    *len = (size_t)(p - *start);
    *pp = p;
}

/* Finds the leftmost match of the reference's pattern,
 * (\d+)\s*:\s*(\d+)\s*:\s*(\d+)\s*[.,]\s*(\d{3,9}).
 *
 * The digit runs are greedy and a shorter run can never be followed by the
 * separator the pattern requires, so no backtracking is needed. A match that
 * starts inside a digit run cannot succeed when the start of the run fails,
 * because both see the same character after the run. */
static int ogm_find_timecode(const ogm_line *line, ogm_timecode *tc) {
    const char *s = line->p;
    const char *end = line->end;

    for (const char *p = s; p < end; p++) {
        if (*p < '0' || *p > '9') {
            continue;
        }
        if (p > s && p[-1] >= '0' && p[-1] <= '9') {
            continue;
        }

        const char *q = p;
        ogm_scan_digits(&q, end, &tc->hour, &tc->hour_len);
        q = ogm_skip_ws(q, end);
        if (q >= end || *q != ':') {
            continue;
        }
        q = ogm_skip_ws(q + 1, end);
        ogm_scan_digits(&q, end, &tc->minute, &tc->minute_len);
        if (tc->minute_len == 0) {
            continue;
        }
        q = ogm_skip_ws(q, end);
        if (q >= end || *q != ':') {
            continue;
        }
        q = ogm_skip_ws(q + 1, end);
        ogm_scan_digits(&q, end, &tc->second, &tc->second_len);
        if (tc->second_len == 0) {
            continue;
        }
        q = ogm_skip_ws(q, end);
        if (q >= end || (*q != '.' && *q != ',')) {
            continue;
        }
        /* The pattern takes 3 to 9 fraction digits and ignores the rest. */
        q = ogm_skip_ws(q + 1, end);
        ogm_scan_digits(&q, end, &tc->frac, &tc->frac_len);
        if (tc->frac_len < 3) {
            continue;
        }
        if (tc->frac_len > 9) {
            tc->frac_len = 9;
        }
        return 1;
    }
    return 0;
}

/* int.Parse, which the reference uses for the hour, minute and second. A value
 * beyond Int32.MaxValue makes the reference throw, so it is reported as an
 * overflow rather than wrapped. */
static int ogm_component_value(const char *p, size_t n, int64_t *out) {
    int64_t v = 0;
    for (size_t i = 0; i < n; i++) {
        int digit = p[i] - '0';
        if (v > (INT32_MAX - digit) / 10) {
            return 0;
        }
        v = v * 10 + digit;
    }
    *out = v;
    return 1;
}

/* The reference turns the fraction into milliseconds with double arithmetic and
 * then truncates to 100 ns ticks:
 *
 *     long.Parse(raw) / Math.Pow(10, raw.Length - 3) * TimeSpan.TicksPerMillisecond
 *
 * The same operations are performed here so that the result matches bit for
 * bit, including the truncation. */
static int64_t ogm_fraction_ticks(const char *p, size_t n) {
    static const double pow10[] = {1.0,      10.0,      100.0,      1000.0,
                                   10000.0,  100000.0,  1000000.0};
    int64_t raw = 0;
    for (size_t i = 0; i < n; i++) {
        raw = raw * 10 + (p[i] - '0');
    }
    double millis = (double)raw / pow10[n - 3];
    return (int64_t)(millis * 10000.0);
}

/* Reads the line's time code. A line without one yields zero, which is what the
 * reference's ToTimeSpan returns for an empty match. */
static ogm_time_status ogm_line_time(const ogm_line *line, int64_t *out_ns) {
    ogm_timecode tc;
    if (!ogm_find_timecode(line, &tc)) {
        *out_ns = 0;
        return OGM_TIME_NONE;
    }

    int64_t hour = 0;
    int64_t minute = 0;
    int64_t second = 0;
    if (!ogm_component_value(tc.hour, tc.hour_len, &hour) ||
        !ogm_component_value(tc.minute, tc.minute_len, &minute) ||
        !ogm_component_value(tc.second, tc.second_len, &second)) {
        return OGM_TIME_OVERFLOW;
    }

    /* TimeSpan(int, int, int) accepts out-of-range minutes and seconds (they
     * just carry) but rejects a total that does not fit in ticks. */
    int64_t total_seconds = hour * 3600 + minute * 60 + second;
    if (total_seconds > 922337203685LL || total_seconds < -922337203685LL) {
        return OGM_TIME_OVERFLOW;
    }
    int64_t ticks = total_seconds * 10000000LL;

    int64_t frac_ticks = ogm_fraction_ticks(tc.frac, tc.frac_len);
    if (ticks > INT64_MAX - frac_ticks) {
        return OGM_TIME_OVERFLOW;
    }
    ticks += frac_ticks;

    /* The reference stores 100 ns ticks, which reach further than the
     * nanoseconds this library exposes; such a file is rejected rather than
     * wrapped. */
    if (ticks > INT64_MAX / 100 || ticks < INT64_MIN / 100) {
        return OGM_TIME_OVERFLOW;
    }
    *out_ns = ticks * 100;
    return OGM_TIME_OK;
}

/* ------------------------------------------------------------------ */
/* Parser                                                             */
/* ------------------------------------------------------------------ */

tc_status tc_ogm_parse_mem(const void *buf, size_t len, const char *hint, tc_data *d) {
    (void)hint;
    if (!buf || !d) {
        return tc_fail(TC_E_INVALID, "ogm: null argument");
    }

    const char *text = (const char *)buf;
    const char *text_end = text + len;

    /* The reference's StreamReader consumes a UTF-8 BOM before the text is
     * split into lines. */
    if (len >= 3 && (unsigned char)text[0] == 0xEF && (unsigned char)text[1] == 0xBB &&
        (unsigned char)text[2] == 0xBF) {
        text += 3;
    }

    /* text.Trim(' ', '\t', '\r', '\n') runs before the split, so whitespace at
     * either end of the file never reaches the state machine. Only the last
     * line is affected by the trailing trim. */
    while (text < text_end &&
           (*text == ' ' || *text == '\t' || *text == '\r' || *text == '\n')) {
        text++;
    }
    while (text_end > text && (text_end[-1] == ' ' || text_end[-1] == '\t' ||
                               text_end[-1] == '\r' || text_end[-1] == '\n')) {
        text_end--;
    }

    /* The reference inspects the first line before running the state machine
     * and throws when it is not a time line, an empty file included. */
    const char *cursor = text;
    const ogm_line first = ogm_next_line(&cursor, text_end);
    if (!ogm_match_time_line(&first)) {
        return tc_fail(TC_E_FORMAT, "ogm: the first line is not a CHAPTER time line");
    }
    int64_t initial_ns = 0;
    if (ogm_line_time(&first, &initial_ns) == OGM_TIME_OVERFLOW) {
        return tc_fail(TC_E_FORMAT, "ogm: the first time code is out of range");
    }

    tc_entry *e = tc_data_add_entry(d);
    if (!e) {
        return TC_E_NOMEM;
    }

    ogm_state state = OGM_LTIMECODE;
    int64_t time_ns = 0;
    cursor = text;
    while (cursor < text_end && state != OGM_LFIN) {
        const ogm_line line = ogm_next_line(&cursor, text_end);

        if (state == OGM_LTIMECODE) {
            if (ogm_is_blank(line.p, line.end)) {
                continue;
            }
            if (ogm_match_time_line(&line)) {
                int64_t ns = 0;
                if (ogm_line_time(&line, &ns) == OGM_TIME_OVERFLOW) {
                    return tc_fail(TC_E_FORMAT, "ogm: a time code is out of range");
                }
                /* Times are relative to the first chapter. */
                time_ns = ns - initial_ns;
                state = OGM_LNAME;
                continue;
            }
            state = OGM_LERROR;
            continue;
        }

        if (state == OGM_LNAME) {
            if (ogm_is_blank(line.p, line.end)) {
                continue;
            }
            const char *name = NULL;
            size_t name_len = 0;
            if (!ogm_match_name_line(&line, &name, &name_len)) {
                state = OGM_LERROR;
                continue;
            }
            char *owned = tc_strndup(name, name_len);
            if (!owned) {
                return TC_E_NOMEM;
            }
            tc_status st = tc_entry_add_chapter(e, owned, time_ns, -1);
            if (st != TC_OK) {
                return st;
            }
            state = OGM_LTIMECODE;
            continue;
        }

        /* OGM_LERROR: the reference logs the unexpected line and keeps the
         * chapters parsed so far, unless there are none. */
        if (e->chapter_count == 0) {
            return tc_fail(TC_E_FORMAT, "ogm: unable to parse this ogm file");
        }
        state = OGM_LFIN;
    }

    if (e->chapter_count == 0) {
        /* A time line with no name line leaves the list empty; the reference
         * throws there when it reads the duration. */
        return tc_fail(TC_E_FORMAT, "ogm: unable to parse this ogm file");
    }

    /* These files carry no total duration; the reference uses the last chapter. */
    e->duration_ns = e->chapters[e->chapter_count - 1].time_ns;
    return TC_OK;
}
