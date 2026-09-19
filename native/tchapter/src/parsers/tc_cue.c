/*
 * CUE sheet parser (B4).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Port of TChapter.Parsing.CUEParser. The original is a state machine driven by
 * five regular expressions; the expressions are reimplemented here with
 * explicit scanners because the library has no regex dependency.
 *
 * Two behaviours of the original are worth calling out:
 *
 *  - CUE timestamps are MM:SS:FF, where FF counts 1/75 s frames. The shared
 *    tc_parse_timestamp helper expects HH:MM:SS.frac and would read the frame
 *    field as seconds, so the conversion is done here:
 *    milliseconds = round(FF * 1000 / 75).
 *
 *  - The .NET original reads the file with
 *    `new StreamReader(stream, detectEncodingFromByteOrderMarks: true)`. The
 *    byte order mark selects UTF-8/UTF-16/UTF-32 and everything else is decoded
 *    as UTF-8 with ill-formed sequences replaced by U+FFFD. The original has no
 *    Shift_JIS or GBK detection, so neither has this port: a SJIS or GBK cue
 *    sheet loses its non-ASCII text to replacement characters in both
 *    implementations.
 */
#include <stdlib.h>
#include <string.h>

#include "tc_internal.h"

/* ------------------------------------------------------------------ */
/* Character helpers                                                  */
/* ------------------------------------------------------------------ */

/* Length in bytes of the whitespace character starting at s, or 0 when the
 * byte does not start one. The set matches System.Char.IsWhiteSpace, which is
 * what the .NET regex \s matches (the decoded text is always valid UTF-8, so
 * the sequence can be decoded without error handling). */
static size_t cue_ws_len(const char *s, size_t len) {
    unsigned char c = (unsigned char)s[0];
    if (c == ' ' || (c >= 0x09 && c <= 0x0D)) {
        return 1;
    }
    if (c < 0xC2 || len < 2) {
        return 0;
    }
    uint32_t cp;
    size_t n;
    if ((c & 0xE0) == 0xC0) {
        cp = (uint32_t)(c & 0x1F);
        n = 2;
    } else if ((c & 0xF0) == 0xE0 && len >= 3) {
        cp = (uint32_t)(c & 0x0F);
        n = 3;
    } else if ((c & 0xF8) == 0xF0 && len >= 4) {
        cp = (uint32_t)(c & 0x07);
        n = 4;
    } else {
        return 0;
    }
    for (size_t i = 1; i < n; i++) {
        unsigned char cc = (unsigned char)s[i];
        if ((cc & 0xC0) != 0x80) {
            return 0;
        }
        cp = (cp << 6) | (uint32_t)(cc & 0x3F);
    }
    switch (cp) {
    case 0x0085: /* NEL */
    case 0x00A0: /* no-break space */
    case 0x1680:
    case 0x2028:
    case 0x2029:
    case 0x202F:
    case 0x205F:
    case 0x3000: /* ideographic space */
        return n;
    default:
        return (cp >= 0x2000 && cp <= 0x200A) ? n : 0;
    }
}

/* Consumes whitespace at *pos and returns how many bytes were skipped. */
static size_t cue_skip_ws(const char *s, size_t len, size_t *pos) {
    size_t n = 0;
    while (*pos < len) {
        size_t w = cue_ws_len(s + *pos, len - *pos);
        if (w == 0) {
            break;
        }
        *pos += w;
        n += w;
    }
    return n;
}

/* True when the line is empty or whitespace only: string.IsNullOrWhiteSpace,
 * which ends the parse in the NewTrack state. */
static int cue_line_blank(const char *s, size_t len) {
    size_t i = 0;
    while (i < len) {
        size_t w = cue_ws_len(s + i, len - i);
        if (w == 0) {
            return 0;
        }
        i += w;
    }
    return 1;
}

/* Finds `needle` in the line at or after `from`; returns `len` when absent. */
static size_t cue_find(const char *s, size_t len, size_t from, const char *needle) {
    size_t n = strlen(needle);
    if (from > len || len - from < n) {
        return len;
    }
    for (size_t i = from; i + n <= len; i++) {
        if (memcmp(s + i, needle, n) == 0) {
            return i;
        }
    }
    return len;
}

/* ------------------------------------------------------------------ */
/* Regex-equivalent matchers                                          */
/* ------------------------------------------------------------------ */

/* TITLE\s+"(.+)" or PERFORMER\s+"(.+)". The capture is greedy, so it runs to
 * the last quote on the line that still leaves at least one character between
 * the quotes. */
static int cue_match_quoted(const char *line, size_t len, const char *keyword,
                            const char **cap, size_t *cap_len) {
    size_t klen = strlen(keyword);
    size_t p = 0;
    for (;;) {
        p = cue_find(line, len, p, keyword);
        if (p == len) {
            return 0;
        }
        size_t q = p + klen;
        size_t ws = cue_skip_ws(line, len, &q);
        if (ws > 0 && q < len && line[q] == '"') {
            size_t close = q + 1;
            for (size_t i = len; i > q + 1; i--) {
                if (line[i - 1] == '"') {
                    close = i - 1;
                    break;
                }
            }
            if (close > q + 1) {
                *cap = line + q + 1;
                *cap_len = close - q - 1;
                return 1;
            }
        }
        p++;
    }
}

/* FILE\s+"(.+)"\s+(WAVE|MP3|AIFF|BINARY|MOTOROLA). The capture ends at the last
 * quote that is followed by whitespace and one of the keywords. */
static int cue_match_file(const char *line, size_t len, const char **cap, size_t *cap_len) {
    static const char *const types[] = {"WAVE", "MP3", "AIFF", "BINARY", "MOTOROLA"};
    size_t p = 0;
    for (;;) {
        p = cue_find(line, len, p, "FILE");
        if (p == len) {
            return 0;
        }
        size_t q = p + 4;
        size_t ws = cue_skip_ws(line, len, &q);
        if (ws > 0 && q < len && line[q] == '"') {
            for (size_t i = len; i > q + 1; i--) {
                if (line[i - 1] != '"') {
                    continue;
                }
                size_t close = i - 1;
                if (close <= q + 1) {
                    continue;
                }
                size_t r = close + 1;
                if (cue_skip_ws(line, len, &r) == 0) {
                    continue;
                }
                for (size_t t = 0; t < sizeof(types) / sizeof(types[0]); t++) {
                    size_t tn = strlen(types[t]);
                    if (r + tn <= len && memcmp(line + r, types[t], tn) == 0) {
                        *cap = line + q + 1;
                        *cap_len = close - q - 1;
                        return 1;
                    }
                }
            }
        }
        p++;
    }
}

/* TRACK\s+(\d+). Returns 1 on a match, 0 when the line does not match and -1
 * when the number does not fit in a 32-bit int (int.Parse would throw). */
static int cue_match_track(const char *line, size_t len, int64_t *number) {
    size_t p = 0;
    for (;;) {
        p = cue_find(line, len, p, "TRACK");
        if (p == len) {
            return 0;
        }
        size_t q = p + 5;
        size_t ws = cue_skip_ws(line, len, &q);
        if (ws > 0 && q < len && line[q] >= '0' && line[q] <= '9') {
            int64_t v = 0;
            int overflow = 0;
            while (q < len && line[q] >= '0' && line[q] <= '9') {
                int d = line[q] - '0';
                if (!overflow) {
                    if (v > (INT32_MAX - d) / 10) {
                        overflow = 1;
                    } else {
                        v = v * 10 + d;
                    }
                }
                q++;
            }
            if (overflow) {
                return -1;
            }
            *number = v;
            return 1;
        }
        p++;
    }
}

/* INDEX\s+(\d+)\s+(\d{2}):(\d{2}):(\d{2}). Returns 1 on a match, 0 when the
 * line does not match and -1 when the index does not fit in a 32-bit int. */
static int cue_match_index(const char *line, size_t len, int64_t *index, int64_t *out_ns) {
    size_t p = 0;
    for (;;) {
        p = cue_find(line, len, p, "INDEX");
        if (p == len) {
            return 0;
        }
        size_t q = p + 5;
        if (cue_skip_ws(line, len, &q) == 0 || q >= len || line[q] < '0' || line[q] > '9') {
            p++;
            continue;
        }
        int64_t idx = 0;
        int overflow = 0;
        while (q < len && line[q] >= '0' && line[q] <= '9') {
            int d = line[q] - '0';
            if (!overflow) {
                if (idx > (INT32_MAX - d) / 10) {
                    overflow = 1;
                } else {
                    idx = idx * 10 + d;
                }
            }
            q++;
        }
        if (overflow) {
            return -1;
        }
        if (cue_skip_ws(line, len, &q) == 0 || q + 8 > len) {
            p++;
            continue;
        }
        int ok = 1;
        for (size_t i = 0; i < 8 && ok; i++) {
            if (i == 2 || i == 5) {
                ok = line[q + i] == ':';
            } else {
                ok = line[q + i] >= '0' && line[q + i] <= '9';
            }
        }
        if (!ok) {
            p++;
            continue;
        }
        int minutes = (line[q] - '0') * 10 + (line[q + 1] - '0');
        int seconds = (line[q + 3] - '0') * 10 + (line[q + 4] - '0');
        int frames = (line[q + 6] - '0') * 10 + (line[q + 7] - '0');
        /* Math.Round(frames * (1000F / 75)); frames is 0..99, so the rounding
         * never lands exactly halfway and integer arithmetic is exact. */
        int millis = (frames * 1000 + 37) / 75;
        *index = idx;
        *out_ns = ((int64_t)minutes * 60 + seconds) * 1000000000LL + (int64_t)millis * 1000000LL;
        return 1;
    }
}

/* ------------------------------------------------------------------ */
/* Encoding                                                           */
/* ------------------------------------------------------------------ */

#define CUE_REPLACEMENT 0xFFFD

static void cue_put_utf8(tc_buf *out, uint32_t cp) {
    if (cp < 0x80) {
        tc_buf_putc(out, (char)cp);
    } else if (cp < 0x800) {
        tc_buf_putc(out, (char)(0xC0 | (cp >> 6)));
        tc_buf_putc(out, (char)(0x80 | (cp & 0x3F)));
    } else if (cp < 0x10000) {
        tc_buf_putc(out, (char)(0xE0 | (cp >> 12)));
        tc_buf_putc(out, (char)(0x80 | ((cp >> 6) & 0x3F)));
        tc_buf_putc(out, (char)(0x80 | (cp & 0x3F)));
    } else {
        tc_buf_putc(out, (char)(0xF0 | (cp >> 18)));
        tc_buf_putc(out, (char)(0x80 | ((cp >> 12) & 0x3F)));
        tc_buf_putc(out, (char)(0x80 | ((cp >> 6) & 0x3F)));
        tc_buf_putc(out, (char)(0x80 | (cp & 0x3F)));
    }
}

/* UTF-8 with the .NET replacement fallback: every ill-formed subsequence is
 * replaced by one U+FFFD, following the maximal subpart rule. .NET Framework
 * and .NET Core disagree on the exact replacement count for some truncated
 * sequences; the Unicode rule is used here. */
static void cue_utf8_decode(const uint8_t *s, size_t len, tc_buf *out) {
    size_t i = 0;
    while (i < len) {
        uint8_t b = s[i];
        size_t need;
        uint32_t cp;
        if (b < 0x80) {
            tc_buf_putc(out, (char)b);
            i++;
            continue;
        }
        if (b >= 0xC2 && b <= 0xDF) {
            need = 1;
            cp = (uint32_t)(b & 0x1F);
        } else if (b >= 0xE0 && b <= 0xEF) {
            need = 2;
            cp = (uint32_t)(b & 0x0F);
        } else if (b >= 0xF0 && b <= 0xF4) {
            need = 3;
            cp = (uint32_t)(b & 0x07);
        } else {
            /* Continuation byte without a lead, or an overlong/out-of-range
             * lead byte: one byte, one replacement. */
            cue_put_utf8(out, CUE_REPLACEMENT);
            i++;
            continue;
        }

        size_t j = i + 1;
        size_t got = 0;
        while (got < need && j < len) {
            uint8_t c = s[j];
            int ok;
            if (got == 0 && b == 0xE0) {
                ok = c >= 0xA0 && c <= 0xBF; /* reject overlong forms */
            } else if (got == 0 && b == 0xED) {
                ok = c >= 0x80 && c <= 0x9F; /* reject surrogates */
            } else if (got == 0 && b == 0xF0) {
                ok = c >= 0x90 && c <= 0xBF; /* reject overlong forms */
            } else if (got == 0 && b == 0xF4) {
                ok = c >= 0x80 && c <= 0x8F; /* reject > U+10FFFF */
            } else {
                ok = c >= 0x80 && c <= 0xBF;
            }
            if (!ok) {
                break;
            }
            cp = (cp << 6) | (uint32_t)(c & 0x3F);
            j++;
            got++;
        }
        if (got == need) {
            cue_put_utf8(out, cp);
        } else {
            /* The maximal subpart is consumed; the offending byte is decoded
             * again on the next iteration. */
            cue_put_utf8(out, CUE_REPLACEMENT);
        }
        i = j;
    }
}

static void cue_utf16_decode(const uint8_t *s, size_t len, int big_endian, tc_buf *out) {
    size_t i = 0;
    while (i + 1 < len) {
        uint32_t u = big_endian ? ((uint32_t)s[i] << 8 | (uint32_t)s[i + 1])
                                : ((uint32_t)s[i + 1] << 8 | (uint32_t)s[i]);
        i += 2;
        if (u >= 0xD800 && u <= 0xDBFF) {
            uint32_t lo = 0;
            if (i + 1 < len) {
                lo = big_endian ? ((uint32_t)s[i] << 8 | (uint32_t)s[i + 1])
                                : ((uint32_t)s[i + 1] << 8 | (uint32_t)s[i]);
            }
            if (lo >= 0xDC00 && lo <= 0xDFFF) {
                i += 2;
                cue_put_utf8(out, 0x10000 + ((u - 0xD800) << 10) + (lo - 0xDC00));
            } else {
                cue_put_utf8(out, CUE_REPLACEMENT); /* unpaired high surrogate */
            }
            continue;
        }
        if (u >= 0xDC00 && u <= 0xDFFF) {
            cue_put_utf8(out, CUE_REPLACEMENT); /* unpaired low surrogate */
            continue;
        }
        cue_put_utf8(out, u);
    }
    if (i < len) {
        cue_put_utf8(out, CUE_REPLACEMENT); /* odd trailing byte */
    }
}

static void cue_utf32_decode(const uint8_t *s, size_t len, int big_endian, tc_buf *out) {
    size_t i = 0;
    while (i + 3 < len) {
        uint32_t cp = big_endian
                          ? ((uint32_t)s[i] << 24 | (uint32_t)s[i + 1] << 16 |
                             (uint32_t)s[i + 2] << 8 | (uint32_t)s[i + 3])
                          : ((uint32_t)s[i + 3] << 24 | (uint32_t)s[i + 2] << 16 |
                             (uint32_t)s[i + 1] << 8 | (uint32_t)s[i]);
        i += 4;
        if (cp > 0x10FFFF || (cp >= 0xD800 && cp <= 0xDFFF)) {
            cue_put_utf8(out, CUE_REPLACEMENT);
        } else {
            cue_put_utf8(out, cp);
        }
    }
    if (i < len) {
        cue_put_utf8(out, CUE_REPLACEMENT); /* incomplete trailing code unit */
    }
}

/* Detects the byte order mark exactly like StreamReader's
 * detectEncodingFromByteOrderMarks, then transcodes the payload to UTF-8. */
static void cue_decode(const uint8_t *raw, size_t len, tc_buf *out) {
    if (len >= 2 && raw[0] == 0xFE && raw[1] == 0xFF) {
        cue_utf16_decode(raw + 2, len - 2, 1, out);
    } else if (len >= 2 && raw[0] == 0xFF && raw[1] == 0xFE) {
        if (len >= 4 && raw[2] == 0x00 && raw[3] == 0x00) {
            cue_utf32_decode(raw + 4, len - 4, 0, out);
        } else {
            cue_utf16_decode(raw + 2, len - 2, 0, out);
        }
    } else if (len >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF) {
        cue_utf8_decode(raw + 3, len - 3, out);
    } else if (len >= 4 && raw[0] == 0x00 && raw[1] == 0x00 && raw[2] == 0xFE &&
               raw[3] == 0xFF) {
        cue_utf32_decode(raw + 4, len - 4, 1, out);
    } else {
        cue_utf8_decode(raw, len, out);
    }
}

/* ------------------------------------------------------------------ */
/* Parser state machine                                               */
/* ------------------------------------------------------------------ */

typedef enum cue_state {
    CUE_ST_START = 0,
    CUE_ST_NEW_TRACK,
    CUE_ST_TRACK,
    CUE_ST_ERROR,
    CUE_ST_FIN
} cue_state;

typedef struct cue_track {
    int64_t number;
    char *name; /* owned by the track */
    int64_t time_ns;
    int complete; /* an INDEX 01 line was seen */
} cue_track;

static void cue_tracks_free(cue_track *tracks, size_t count) {
    for (size_t i = 0; i < count; i++) {
        free(tracks[i].name);
    }
    free(tracks);
}

/* chapter.Name = capture.Trim('\r') */
static tc_status cue_track_set_name(cue_track *t, const char *cap, size_t cap_len) {
    while (cap_len > 0 && cap[0] == '\r') {
        cap++;
        cap_len--;
    }
    while (cap_len > 0 && cap[cap_len - 1] == '\r') {
        cap_len--;
    }
    char *name = tc_strndup(cap, cap_len);
    if (!name) {
        return TC_E_NOMEM;
    }
    free(t->name);
    t->name = name;
    return TC_OK;
}

/* chapter.Name += $" [{capture.Trim('\r')}]" */
static tc_status cue_track_append_name(cue_track *t, const char *cap, size_t cap_len) {
    while (cap_len > 0 && cap[0] == '\r') {
        cap++;
        cap_len--;
    }
    while (cap_len > 0 && cap[cap_len - 1] == '\r') {
        cap_len--;
    }
    size_t old = t->name ? strlen(t->name) : 0;
    char *name = tc_realloc(t->name, old + cap_len + 4);
    if (!name) {
        return TC_E_NOMEM;
    }
    name[old] = ' ';
    name[old + 1] = '[';
    memcpy(name + old + 2, cap, cap_len);
    name[old + 2 + cap_len] = ']';
    name[old + 3 + cap_len] = '\0';
    t->name = name;
    return TC_OK;
}

static tc_status cue_parse_text(const char *text, size_t len, tc_data *d) {
    cue_state state = CUE_ST_START;
    tc_buf title;
    tc_buf source;
    tc_buf_init(&title);
    tc_buf_init(&source);
    int have_title = 0;
    int have_source = 0;

    cue_track *tracks = NULL;
    size_t count = 0;
    size_t track_cap = 0;
    tc_status st = TC_OK;

    size_t pos = 0;
    for (;;) {
        size_t end = pos;
        while (end < len && text[end] != '\n') {
            end++;
        }
        const char *line = text + pos;
        size_t line_len = end - pos;

        switch (state) {
        case CUE_ST_START: {
            const char *cap;
            size_t cap_len;
            if (cue_match_quoted(line, line_len, "TITLE", &cap, &cap_len)) {
                tc_buf_reset(&title);
                tc_buf_write(&title, cap, cap_len);
                if (title.oom) {
                    st = TC_E_NOMEM;
                    goto out;
                }
                have_title = 1;
                break;
            }
            if (cue_match_file(line, line_len, &cap, &cap_len)) {
                tc_buf_reset(&source);
                tc_buf_write(&source, cap, cap_len);
                if (source.oom) {
                    st = TC_E_NOMEM;
                    goto out;
                }
                have_source = 1;
                state = CUE_ST_NEW_TRACK;
            }
            break;
        }

        case CUE_ST_NEW_TRACK: {
            if (cue_line_blank(line, line_len)) {
                state = CUE_ST_FIN;
                break;
            }
            int64_t number = 0;
            int m = cue_match_track(line, line_len, &number);
            if (m < 0) {
                state = CUE_ST_ERROR;
                break;
            }
            if (m > 0) {
                if (count == track_cap) {
                    size_t ncap = track_cap ? track_cap * 2 : 8;
                    cue_track *nt = tc_realloc(tracks, ncap * sizeof(*nt));
                    if (!nt) {
                        st = TC_E_NOMEM;
                        goto out;
                    }
                    tracks = nt;
                    track_cap = ncap;
                }
                tracks[count].number = number;
                tracks[count].name = NULL;
                tracks[count].time_ns = 0;
                tracks[count].complete = 0;
                count++;
                state = CUE_ST_TRACK;
            }
            break;
        }

        case CUE_ST_TRACK: {
            const char *cap;
            size_t cap_len;
            if (cue_match_quoted(line, line_len, "TITLE", &cap, &cap_len)) {
                st = cue_track_set_name(&tracks[count - 1], cap, cap_len);
                if (st != TC_OK) {
                    goto out;
                }
                break;
            }
            if (cue_match_quoted(line, line_len, "PERFORMER", &cap, &cap_len)) {
                st = cue_track_append_name(&tracks[count - 1], cap, cap_len);
                if (st != TC_OK) {
                    goto out;
                }
                break;
            }
            int64_t index = 0;
            int64_t ns = 0;
            int m = cue_match_index(line, line_len, &index, &ns);
            if (m < 0) {
                state = CUE_ST_ERROR;
                break;
            }
            if (m > 0) {
                if (index == 0) {
                    break; /* pre-gap, ignored */
                }
                if (index == 1) {
                    tracks[count - 1].time_ns = ns;
                    tracks[count - 1].complete = 1;
                    state = CUE_ST_NEW_TRACK;
                    break;
                }
                state = CUE_ST_ERROR;
            }
            break;
        }

        case CUE_ST_ERROR:
            st = tc_fail(TC_E_FORMAT, "cue: unable to parse this cue file");
            goto out;

        case CUE_ST_FIN:
            goto done;
        }

        if (end >= len) {
            break;
        }
        pos = end + 1;
    }

done: {
    size_t complete = 0;
    for (size_t i = 0; i < count; i++) {
        if (tracks[i].complete) {
            complete++;
        }
    }
    if (complete == 0) {
        st = tc_fail(TC_E_FORMAT, "cue: empty cue file");
        goto out;
    }

    /* The original sorts the chapters by track number before reporting them.
     * Insertion sort keeps equal numbers in file order. */
    for (size_t i = 1; i < count; i++) {
        cue_track key = tracks[i];
        size_t j = i;
        while (j > 0 && tracks[j - 1].number > key.number) {
            tracks[j] = tracks[j - 1];
            j--;
        }
        tracks[j] = key;
    }

    tc_entry *e = tc_data_add_entry(d);
    if (!e) {
        st = TC_E_NOMEM;
        goto out;
    }
    if (have_title) {
        e->title = title.data;
        title.data = NULL;
    }
    if (have_source) {
        e->source = source.data;
        source.data = NULL;
    }
    for (size_t i = 0; i < count; i++) {
        if (!tracks[i].complete) {
            continue;
        }
        char *name = tracks[i].name;
        tracks[i].name = NULL;
        st = tc_entry_add_chapter(e, name, tracks[i].time_ns, -1);
        if (st != TC_OK) {
            goto out;
        }
        /* After the sort the last chapter is the one with the highest track
         * number; the original uses its time as the duration. */
        e->duration_ns = tracks[i].time_ns;
    }
}

out:
    tc_buf_free(&title);
    tc_buf_free(&source);
    cue_tracks_free(tracks, count);
    return st;
}

/* ------------------------------------------------------------------ */
/* Entry points                                                       */
/* ------------------------------------------------------------------ */

tc_status tc_cue_parse_file(const char *path, tc_data *d) {
    if (!path || !d) {
        return tc_fail(TC_E_INVALID, "cue: null argument");
    }
    char *raw = NULL;
    size_t len = 0;
    tc_status st = tc_read_file(path, &raw, &len);
    if (st != TC_OK) {
        return st;
    }

    tc_buf text;
    tc_buf_init(&text);
    cue_decode((const uint8_t *)raw, len, &text);
    free(raw);
    if (text.oom) {
        tc_buf_free(&text);
        return TC_E_NOMEM;
    }

    st = cue_parse_text(text.data ? text.data : "", text.len, d);
    tc_buf_free(&text);
    return st;
}

tc_status tc_cue_parse_mem(const void *buf, size_t len, const char *hint, tc_data *d) {
    (void)hint; /* the format is fixed; the file name is not needed */
    if (!buf || !d) {
        return tc_fail(TC_E_INVALID, "cue: null argument");
    }
    tc_buf text;
    tc_buf_init(&text);
    cue_decode((const uint8_t *)buf, len, &text);
    if (text.oom) {
        tc_buf_free(&text);
        return TC_E_NOMEM;
    }
    tc_status st = cue_parse_text(text.data ? text.data : "", text.len, d);
    tc_buf_free(&text);
    return st;
}
