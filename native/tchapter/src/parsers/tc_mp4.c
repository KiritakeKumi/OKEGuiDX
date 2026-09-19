/*
 * MP4 chapter parser.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Replaces the reference implementation's dependency on libmp4v2, which ships
 * prebuilt win-x86/x64 binaries only and therefore blocks every other target
 * (INVENTORY.md section 4). MP4Parser.cs is a thin P/Invoke shell around
 * MP4GetChapters; this file reimplements the box walk that call performs so the
 * library keeps its "no dependencies beyond libc" guarantee. No mp4v2 code is
 * used here.
 *
 * Two chapter storages are read, in the reference's order (QuickTime first):
 *
 *   1. QuickTime: a text track that a video or audio track references through
 *      tref/chap. Each sample is a 16-bit big-endian length followed by the
 *      title (MP4File::AddChapter).
 *   2. Nero: moov/udta/chpl. Version 1, written by mp4v2, ffmpeg and l-smash,
 *      stores start times in 100 ns units and a 32-bit entry count; the F4V
 *      version 0 stores them in movie timescale units with an 8-bit count.
 *
 * The reference reports durations rather than start times, and
 * MP4Parser.ReadFromFile rebuilds the start times by accumulating them. That
 * accumulation is kept here: a chapter's time is the sum of the previous
 * chapters' durations in milliseconds. For Nero chapters a duration is the gap
 * to the next start time and the last one runs to the movie duration; for
 * QuickTime chapters it is the sample duration. When a file has neither
 * storage, the reference fabricates a single chapter at the movie duration
 * named "Chapter 01", and that fallback is reproduced.
 */
#include <stdlib.h>
#include <string.h>

#include "tc_internal.h"

/* ------------------------------------------------------------------ */
/* Box traversal                                                      */
/* ------------------------------------------------------------------ */

typedef struct mp4_view {
    const uint8_t *data;
    size_t len;
} mp4_view;

/* A byte range inside the file: [start, end). */
typedef struct mp4_box {
    size_t start;
    size_t end;
} mp4_box;

enum mp4_box_result {
    MP4_BOX_MALFORMED = -1, /* header invalid or a box truncated mid-header */
    MP4_BOX_END = 0,        /* nothing left to read */
    MP4_BOX_OK = 1
};

static size_t mp4_box_size(const mp4_box *b) { return b->end - b->start; }

/* Reads the box header at `off` within [0, limit). The size is 32-bit
 * big-endian; size 1 means an 8-byte size follows the type, size 0 means the
 * box runs to the end of the range (MP4Atom::ReadAtom).
 *
 * A box that claims more bytes than its parent holds is clamped to the parent,
 * as mp4v2 does, so a truncated mdat does not stop a later moov from being
 * read. A header that does not fit at all, or a size below the header length,
 * is malformed. */
static int mp4_box_at(const mp4_view *f, size_t off, size_t limit, size_t *next,
                      mp4_box *body, char type[5]) {
    if (limit > f->len) {
        limit = f->len;
    }
    if (off > limit || limit - off < 8) {
        return MP4_BOX_END;
    }
    uint64_t size = tc_be32(f->data + off);
    memcpy(type, f->data + off + 4, 4);
    type[4] = '\0';

    size_t header = 8;
    if (size == 1) {
        if (limit - off < 16) {
            return MP4_BOX_MALFORMED;
        }
        size = tc_be64(f->data + off + 8);
        header = 16;
    }
    if (size == 0) {
        size = (uint64_t)(limit - off);
    }
    if (size < header) {
        return MP4_BOX_MALFORMED;
    }
    if (size > (uint64_t)(limit - off)) {
        size = (uint64_t)(limit - off);
    }

    body->start = off + header;
    body->end = off + (size_t)size;
    *next = off + (size_t)size;
    return MP4_BOX_OK;
}

/* Finds the first direct child of `parent` with the given type. */
static int mp4_child(const mp4_view *f, mp4_box parent, const char *type,
                     mp4_box *out) {
    size_t off = parent.start;
    while (off < parent.end) {
        size_t next = 0;
        mp4_box body;
        char name[5];
        int rc = mp4_box_at(f, off, parent.end, &next, &body, name);
        if (rc != MP4_BOX_OK) {
            return rc;
        }
        if (memcmp(name, type, 4) == 0) {
            *out = body;
            return MP4_BOX_OK;
        }
        off = next;
    }
    return MP4_BOX_END;
}

/* Reads moov/mvhd. Returns 0 when the box is absent or too short. */
static int mp4_movie_header(const mp4_view *f, mp4_box moov,
                            uint32_t *timescale, uint64_t *duration) {
    mp4_box mvhd;
    if (mp4_child(f, moov, "mvhd", &mvhd) != MP4_BOX_OK ||
        mp4_box_size(&mvhd) < 4) {
        return 0;
    }
    uint8_t version = f->data[mvhd.start];
    size_t off = mvhd.start + 4 + (version == 1 ? 16 : 8);
    if (off > mvhd.end || mvhd.end - off < 8) {
        return 0;
    }
    *timescale = tc_be32(f->data + off);
    *duration = version == 1 ? tc_be64(f->data + off + 4)
                             : tc_be32(f->data + off + 4);
    return 1;
}

/* ------------------------------------------------------------------ */
/* Titles                                                             */
/* ------------------------------------------------------------------ */

#define MP4_REPLACEMENT 0xFFFD

static void mp4_put_utf8(tc_buf *out, uint32_t cp) {
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

/* UTF-8 with the .NET replacement fallback: every ill-formed subsequence
 * becomes one U+FFFD, following the maximal subpart rule. */
static void mp4_utf8_decode(const uint8_t *s, size_t len, tc_buf *out) {
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
            mp4_put_utf8(out, MP4_REPLACEMENT);
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
            mp4_put_utf8(out, cp);
        } else {
            /* The maximal subpart is consumed; the offending byte is decoded
             * again on the next iteration. */
            mp4_put_utf8(out, MP4_REPLACEMENT);
        }
        i = j;
    }
}

static void mp4_utf16_decode(const uint8_t *s, size_t len, int big_endian,
                             tc_buf *out) {
    size_t i = 0;
    while (i + 1 < len) {
        uint32_t u = big_endian ? ((uint32_t)s[i] << 8 | (uint32_t)s[i + 1])
                                : ((uint32_t)s[i + 1] << 8 | (uint32_t)s[i]);
        i += 2;
        if (u >= 0xD800 && u <= 0xDBFF) {
            uint32_t lo = 0;
            if (i + 1 < len) {
                lo = big_endian
                         ? ((uint32_t)s[i] << 8 | (uint32_t)s[i + 1])
                         : ((uint32_t)s[i + 1] << 8 | (uint32_t)s[i]);
            }
            if (lo >= 0xDC00 && lo <= 0xDFFF) {
                i += 2;
                mp4_put_utf8(out, 0x10000 + ((u - 0xD800) << 10) + (lo - 0xDC00));
            } else {
                mp4_put_utf8(out, MP4_REPLACEMENT); /* unpaired high surrogate */
            }
            continue;
        }
        if (u >= 0xDC00 && u <= 0xDFFF) {
            mp4_put_utf8(out, MP4_REPLACEMENT); /* unpaired low surrogate */
            continue;
        }
        mp4_put_utf8(out, u);
    }
    if (i < len) {
        mp4_put_utf8(out, MP4_REPLACEMENT); /* odd trailing byte */
    }
}

/* Decodes a chapter title the way MP4Parser.GetString does: a byte order mark
 * selects UTF-16 or UTF-8, anything else is UTF-8, and the text ends at the
 * first NUL. The mark itself is not part of the name (the reference's UTF-16
 * branch decodes it into a leading U+FEFF; no real writer emits UTF-16 here,
 * and the UTF-8 branch strips its mark, so stripping is used for both). */
static char *mp4_decode_title(const uint8_t *s, size_t len) {
    tc_buf out;
    tc_buf_init(&out);
    if (len >= 2 && s[0] == 0xFF && s[1] == 0xFE) {
        mp4_utf16_decode(s + 2, len - 2, 0, &out);
    } else if (len >= 2 && s[0] == 0xFE && s[1] == 0xFF) {
        mp4_utf16_decode(s + 2, len - 2, 1, &out);
    } else if (len >= 3 && s[0] == 0xEF && s[1] == 0xBB && s[2] == 0xBF) {
        mp4_utf8_decode(s + 3, len - 3, &out);
    } else {
        mp4_utf8_decode(s, len, &out);
    }
    if (out.oom) {
        tc_buf_free(&out);
        return NULL;
    }
    /* NUL is encoded as a single 0x00 in both decoders, so a byte scan finds
     * the same cut as the reference's IndexOf('\0') after decoding. */
    size_t n = 0;
    while (n < out.len && out.data[n] != '\0') {
        n++;
    }
    char *name = tc_malloc(n + 1);
    if (name) {
        memcpy(name, out.data ? out.data : "", n);
        name[n] = '\0';
    }
    tc_buf_free(&out);
    return name;
}

/* ------------------------------------------------------------------ */
/* Time conversion                                                    */
/* ------------------------------------------------------------------ */

/* mp4v2's MP4ConvertTime(t, scale, 1000): integer arithmetic while the product
 * fits in 64 bits, floating point with round-half-up otherwise. */
static uint64_t mp4_ms_from(uint64_t t, uint32_t scale) {
    if (scale == 0 || scale == 1000) {
        return t;
    }
    /* ceil(log2(t)), matching mp4v2's ilog2. */
    int bits = 0;
    uint64_t power = 1;
    while (bits < 64 && t > power) {
        power <<= 1;
        bits++;
    }
    if (bits + 10 <= 64) { /* ilog2(1000) == 10 */
        return (t * 1000u) / scale;
    }
    double d = 1000.0 * (double)t / (double)scale;
    return (uint64_t)(d + 0.5);
}

/* ------------------------------------------------------------------ */
/* Nero chapters (moov/udta/chpl)                                     */
/* ------------------------------------------------------------------ */

typedef struct mp4_nero_entry {
    uint64_t start;
    char *name;
} mp4_nero_entry;

static void mp4_nero_free(mp4_nero_entry *entries, size_t count) {
    for (size_t i = 0; i < count; i++) {
        free(entries[i].name);
    }
    free(entries);
}

/* Parses the chpl payload into a caller-owned array. *count is 0 for an empty
 * list; a truncated box or a count that cannot fit is TC_E_FORMAT. */
static tc_status mp4_chpl_read(const mp4_view *f, mp4_box chpl,
                               mp4_nero_entry **out, size_t *count,
                               uint8_t *version) {
    *out = NULL;
    *count = 0;

    if (mp4_box_size(&chpl) < 4) {
        return tc_fail(TC_E_FORMAT, "mp4: truncated chpl box");
    }
    *version = f->data[chpl.start];
    size_t off = chpl.start + 4;

    uint64_t n;
    if (*version == 1) {
        /* version+flags, one reserved byte, 32-bit entry count. */
        if (mp4_box_size(&chpl) - 4 < 5) {
            return tc_fail(TC_E_FORMAT, "mp4: truncated chpl box");
        }
        n = tc_be32(f->data + off + 1);
        off += 5;
    } else {
        /* F4V version 0: 8-bit entry count. */
        if (mp4_box_size(&chpl) - 4 < 1) {
            return tc_fail(TC_E_FORMAT, "mp4: truncated chpl box");
        }
        n = f->data[off];
        off += 1;
    }
    if (n == 0) {
        return TC_OK;
    }
    /* Every entry needs at least a 64-bit start time and a length byte. */
    if (n > (uint64_t)(chpl.end - off) / 9) {
        return tc_fail(TC_E_FORMAT, "mp4: chpl chapter count out of range");
    }

    mp4_nero_entry *list = tc_calloc((size_t)n, sizeof(*list));
    if (!list) {
        return TC_E_NOMEM;
    }
    size_t used = 0;
    tc_status st = TC_OK;
    for (uint64_t i = 0; i < n; i++) {
        if (chpl.end - off < 9) {
            st = tc_fail(TC_E_FORMAT, "mp4: truncated chpl chapter entry");
            break;
        }
        uint64_t start = tc_be64(f->data + off);
        size_t title_len = f->data[off + 8];
        off += 9;
        if (chpl.end - off < title_len) {
            st = tc_fail(TC_E_FORMAT, "mp4: truncated chpl chapter title");
            break;
        }
        char *name = mp4_decode_title(f->data + off, title_len);
        if (!name) {
            st = TC_E_NOMEM;
            break;
        }
        list[used].start = start;
        list[used].name = name;
        used++;
        off += title_len;
    }
    if (st != TC_OK) {
        mp4_nero_free(list, used);
        return st;
    }
    *out = list;
    *count = used;
    return TC_OK;
}

/* ------------------------------------------------------------------ */
/* QuickTime chapters (tref/chap -> text track)                       */
/* ------------------------------------------------------------------ */

typedef struct mp4_track_info {
    mp4_box trak;
    uint32_t id;
    char handler[5];
} mp4_track_info;

/* Reads a track's id (tkhd) and handler type (mdia/hdlr). Returns 0 when either
 * is missing, which makes the track invisible to the reference as well. */
static int mp4_track_info_read(const mp4_view *f, mp4_box trak,
                               mp4_track_info *t) {
    memset(t, 0, sizeof(*t));
    t->trak = trak;

    mp4_box tkhd;
    if (mp4_child(f, trak, "tkhd", &tkhd) != MP4_BOX_OK ||
        mp4_box_size(&tkhd) < 4) {
        return 0;
    }
    uint8_t version = f->data[tkhd.start];
    size_t off = tkhd.start + 4 + (version == 1 ? 16 : 8);
    if (off > tkhd.end || tkhd.end - off < 4) {
        return 0;
    }
    t->id = tc_be32(f->data + off);

    mp4_box mdia;
    if (mp4_child(f, trak, "mdia", &mdia) != MP4_BOX_OK) {
        return 0;
    }
    mp4_box hdlr;
    if (mp4_child(f, mdia, "hdlr", &hdlr) != MP4_BOX_OK ||
        mp4_box_size(&hdlr) < 12) {
        return 0;
    }
    memcpy(t->handler, f->data + hdlr.start + 8, 4);
    t->handler[4] = '\0';
    return 1;
}

/* True when `trak` has a tref/chap entry naming `chapter_id`. */
static int mp4_has_chap_ref(const mp4_view *f, mp4_box trak,
                            uint32_t chapter_id) {
    mp4_box tref;
    if (mp4_child(f, trak, "tref", &tref) != MP4_BOX_OK) {
        return 0;
    }
    mp4_box chap;
    if (mp4_child(f, tref, "chap", &chap) != MP4_BOX_OK) {
        return 0;
    }
    /* The payload is a plain list of 32-bit track ids. */
    for (size_t off = chap.start; chap.end - off >= 4; off += 4) {
        if (tc_be32(f->data + off) == chapter_id) {
            return 1;
        }
    }
    return 0;
}

/* Finds the chapter track: the first text track that a video or audio track
 * references through tref/chap (MP4File::FindChapterTrack). Returns
 * TC_E_FORMAT for a malformed moov, which is what makes mp4v2's MP4Read fail. */
static tc_status mp4_qt_find_track(const mp4_view *f, mp4_box moov,
                                   mp4_box *out_trak, int *found) {
    *found = 0;

    mp4_track_info *tracks = NULL;
    size_t count = 0;
    size_t cap = 0;

    size_t off = moov.start;
    while (off < moov.end) {
        size_t next = 0;
        mp4_box body;
        char name[5];
        int rc = mp4_box_at(f, off, moov.end, &next, &body, name);
        if (rc != MP4_BOX_OK) {
            free(tracks);
            return tc_fail(TC_E_FORMAT, "mp4: malformed moov box");
        }
        if (memcmp(name, "trak", 4) == 0) {
            if (count == cap) {
                size_t new_cap = cap ? cap * 2 : 4;
                mp4_track_info *grown =
                    tc_realloc(tracks, new_cap * sizeof(*grown));
                if (!grown) {
                    free(tracks);
                    return TC_E_NOMEM;
                }
                tracks = grown;
                cap = new_cap;
            }
            if (mp4_track_info_read(f, body, &tracks[count])) {
                count++;
            }
        }
        off = next;
    }

    for (size_t i = 0; i < count && !*found; i++) {
        if (!tc_ieq(tracks[i].handler, "text")) {
            continue;
        }
        for (size_t j = 0; j < count; j++) {
            if (!tc_ieq(tracks[j].handler, "vide") &&
                !tc_ieq(tracks[j].handler, "soun")) {
                continue;
            }
            if (mp4_has_chap_ref(f, tracks[j].trak, tracks[i].id)) {
                *out_trak = tracks[i].trak;
                *found = 1;
                break;
            }
        }
    }
    free(tracks);
    return TC_OK;
}

typedef struct mp4_qt_chapter {
    char *name;
    int64_t duration_ms;
} mp4_qt_chapter;

static void mp4_qt_free(mp4_qt_chapter *chapters, size_t count) {
    for (size_t i = 0; i < count; i++) {
        free(chapters[i].name);
    }
    free(chapters);
}

/* Reads the chapter track's samples: stts for the durations, stsc/stsz/stco for
 * the sample data.
 *
 * Returns TC_OK with *count == 0 whenever the track cannot serve as a chapter
 * track (missing tables, no samples, inconsistent sample data). mp4v2 treats
 * every such failure as "no QuickTime chapters" and lets the caller fall
 * through, so a corrupt track never hides the Nero chapters that follow. Only
 * an allocation failure is reported as an error. */
static tc_status mp4_qt_read(const mp4_view *f, mp4_box trak,
                             mp4_qt_chapter **out, size_t *count) {
    *out = NULL;
    *count = 0;

    mp4_box mdia;
    if (mp4_child(f, trak, "mdia", &mdia) != MP4_BOX_OK) {
        return TC_OK;
    }
    mp4_box mdhd;
    if (mp4_child(f, mdia, "mdhd", &mdhd) != MP4_BOX_OK ||
        mp4_box_size(&mdhd) < 4) {
        return TC_OK;
    }
    uint8_t mdhd_version = f->data[mdhd.start];
    size_t mdhd_off = mdhd.start + 4 + (mdhd_version == 1 ? 16 : 8);
    if (mdhd_off > mdhd.end || mdhd.end - mdhd_off < 4) {
        return TC_OK;
    }
    uint32_t timescale = tc_be32(f->data + mdhd_off);
    if (timescale == 0) {
        return TC_OK;
    }

    mp4_box minf;
    if (mp4_child(f, mdia, "minf", &minf) != MP4_BOX_OK) {
        return TC_OK;
    }
    mp4_box stbl;
    if (mp4_child(f, minf, "stbl", &stbl) != MP4_BOX_OK) {
        return TC_OK;
    }

    mp4_box stts;
    if (mp4_child(f, stbl, "stts", &stts) != MP4_BOX_OK ||
        mp4_box_size(&stts) < 8) {
        return TC_OK;
    }
    uint32_t stts_n = tc_be32(f->data + stts.start + 4);
    if (stts_n == 0 || stts_n > (mp4_box_size(&stts) - 8) / 8) {
        return TC_OK;
    }
    size_t stts_off = stts.start + 8;

    mp4_box stsz;
    if (mp4_child(f, stbl, "stsz", &stsz) != MP4_BOX_OK ||
        mp4_box_size(&stsz) < 12) {
        return TC_OK;
    }
    uint32_t fixed_size = tc_be32(f->data + stsz.start + 4);
    uint32_t sample_count = tc_be32(f->data + stsz.start + 8);
    if (sample_count == 0) {
        return TC_OK;
    }
    if (fixed_size == 0 &&
        sample_count > (mp4_box_size(&stsz) - 12) / 4) {
        return TC_OK;
    }

    mp4_box stsc;
    if (mp4_child(f, stbl, "stsc", &stsc) != MP4_BOX_OK ||
        mp4_box_size(&stsc) < 8) {
        return TC_OK;
    }
    uint32_t stsc_n = tc_be32(f->data + stsc.start + 4);
    if (stsc_n == 0 || stsc_n > (mp4_box_size(&stsc) - 8) / 12) {
        return TC_OK;
    }
    size_t stsc_off = stsc.start + 8;

    mp4_box stco;
    int co64;
    if (mp4_child(f, stbl, "stco", &stco) == MP4_BOX_OK) {
        co64 = 0;
    } else if (mp4_child(f, stbl, "co64", &stco) == MP4_BOX_OK) {
        co64 = 1;
    } else {
        return TC_OK;
    }
    if (mp4_box_size(&stco) < 8) {
        return TC_OK;
    }
    uint32_t chunk_count = tc_be32(f->data + stco.start + 4);
    size_t chunk_entry = co64 ? 8 : 4;
    if (chunk_count > (mp4_box_size(&stco) - 8) / chunk_entry) {
        return TC_OK;
    }
    size_t chunk_off = stco.start + 8;

    mp4_qt_chapter *list = tc_calloc(sample_count, sizeof(*list));
    if (!list) {
        return TC_E_NOMEM;
    }

    size_t made = 0;
    int usable = 1;

    /* stts cursor: the current run of identical sample deltas. */
    uint32_t stts_i = 0;
    uint32_t stts_left = tc_be32(f->data + stts_off);
    uint32_t stts_delta = tc_be32(f->data + stts_off + 4);

    /* stsc cursor: `chunk_id` and `samples_per_chunk` describe the run the
     * next sample belongs to; the run advances on chunk boundaries. */
    uint32_t sc = 0;
    uint32_t chunk_id = tc_be32(f->data + stsc_off);
    uint32_t samples_per_chunk = tc_be32(f->data + stsc_off + 4);
    uint32_t sample_in_chunk = 0;
    uint64_t chunk_offset = 0;
    uint64_t offset_in_chunk = 0;
    int have_chunk = 0;

    if (samples_per_chunk == 0) {
        usable = 0;
    }

    for (uint32_t i = 0; usable && i < sample_count; i++) {
        while (stts_left == 0 && stts_i + 1 < stts_n) {
            stts_i++;
            stts_left = tc_be32(f->data + stts_off + (size_t)stts_i * 8);
            stts_delta = tc_be32(f->data + stts_off + (size_t)stts_i * 8 + 4);
        }
        uint32_t delta = stts_left > 0 ? stts_delta : 0;
        if (stts_left > 0) {
            stts_left--;
        }

        if (sample_in_chunk == samples_per_chunk) {
            chunk_id++;
            sample_in_chunk = 0;
            offset_in_chunk = 0;
            while (sc + 1 < stsc_n &&
                   chunk_id >=
                       tc_be32(f->data + stsc_off + (size_t)(sc + 1) * 12)) {
                sc++;
                samples_per_chunk =
                    tc_be32(f->data + stsc_off + (size_t)sc * 12 + 4);
            }
            if (samples_per_chunk == 0) {
                usable = 0;
                break;
            }
            have_chunk = 0;
        }
        if (!have_chunk) {
            if (chunk_id < 1 || chunk_id > chunk_count) {
                usable = 0;
                break;
            }
            chunk_offset =
                co64 ? tc_be64(f->data + chunk_off + (size_t)(chunk_id - 1) * 8)
                     : tc_be32(f->data + chunk_off + (size_t)(chunk_id - 1) * 4);
            have_chunk = 1;
        }

        uint32_t sample_size =
            fixed_size
                ? fixed_size
                : tc_be32(f->data + stsz.start + 12 + (size_t)i * 4);
        uint64_t sample_off = chunk_offset + offset_in_chunk;
        offset_in_chunk += sample_size;
        sample_in_chunk++;

        char *name;
        if (sample_size < 2) {
            name = tc_strdup("");
        } else if (sample_off > (uint64_t)f->len ||
                   (uint64_t)f->len - sample_off < sample_size) {
            usable = 0;
            break;
        } else {
            /* A 16-bit length, then the title. mp4v2 clamps the length to the
             * reference's 1023-byte title buffer before copying; it is also
             * clamped to the sample here, which the reference does not do. */
            size_t title_len =
                ((size_t)f->data[(size_t)sample_off] << 8) |
                (size_t)f->data[(size_t)sample_off + 1];
            if (title_len > 1023) {
                title_len = 1023;
            }
            if (title_len > sample_size - 2) {
                title_len = sample_size - 2;
            }
            name =
                mp4_decode_title(f->data + (size_t)sample_off + 2, title_len);
        }
        if (!name) {
            mp4_qt_free(list, made);
            return TC_E_NOMEM;
        }
        list[made].name = name;
        list[made].duration_ms = (int64_t)mp4_ms_from(delta, timescale);
        made++;
    }

    if (!usable) {
        mp4_qt_free(list, made);
        return TC_OK;
    }
    *out = list;
    *count = made;
    return TC_OK;
}

/* ------------------------------------------------------------------ */
/* Entry assembly                                                     */
/* ------------------------------------------------------------------ */

/* Adds one entry whose chapters start at the running total of the previous
 * chapters' durations, mirroring MP4Parser.ReadFromFile. Takes ownership of
 * the names it consumes; on failure the caller frees the remaining ones. */
static tc_status mp4_add_entry(tc_data *d, char **names,
                               const int64_t *durations_ms, size_t count) {
    tc_entry *e = tc_data_add_entry(d);
    if (!e) {
        return TC_E_NOMEM;
    }
    int64_t current_ms = 0;
    for (size_t i = 0; i < count; i++) {
        char *name = names[i];
        names[i] = NULL;
        if (tc_entry_add_chapter(e, name, current_ms * 1000000LL, -1) != TC_OK) {
            return TC_E_NOMEM;
        }
        current_ms += durations_ms[i];
    }
    e->duration_ns = current_ms * 1000000LL;
    return TC_OK;
}

/* The reference's no-chapter fallback: a single chapter at the movie duration
 * named "Chapter 01" (MP4Parser.ReadFromFile's else branch). The chapter time
 * is TimeSpan.FromSeconds, which .NET Framework rounds to whole milliseconds;
 * that rounding is reproduced because OKEGui runs on net472. */
static tc_status mp4_fallback_entry(const mp4_view *f, mp4_box moov,
                                    tc_data *d) {
    uint32_t timescale = 0;
    uint64_t duration = 0;
    if (!mp4_movie_header(f, moov, &timescale, &duration) || timescale == 0) {
        return tc_fail(TC_E_FORMAT,
                       "mp4: no chapters found and no usable movie header");
    }
    double millis = (double)duration * 1000.0 / (double)timescale;
    millis = millis >= 0.0 ? millis + 0.5 : millis - 0.5;
    int64_t ns = (int64_t)millis * 1000000LL;

    tc_entry *e = tc_data_add_entry(d);
    if (!e) {
        return TC_E_NOMEM;
    }
    char *name = tc_strdup("Chapter 01");
    if (!name) {
        return TC_E_NOMEM;
    }
    if (tc_entry_add_chapter(e, name, ns, -1) != TC_OK) {
        return TC_E_NOMEM;
    }
    e->duration_ns = ns;
    return TC_OK;
}

/* ------------------------------------------------------------------ */
/* Entry point                                                        */
/* ------------------------------------------------------------------ */

static tc_status mp4_parse(const uint8_t *data, size_t len, tc_data *d) {
    mp4_view f;
    f.data = data;
    f.len = len;

    /* Top-level scan for moov. */
    mp4_box moov;
    int have_moov = 0;
    size_t off = 0;
    while (off < len) {
        size_t next = 0;
        mp4_box body;
        char name[5];
        int rc = mp4_box_at(&f, off, len, &next, &body, name);
        if (rc == MP4_BOX_END) {
            break;
        }
        if (rc == MP4_BOX_MALFORMED) {
            return tc_fail(TC_E_FORMAT, "mp4: truncated or invalid box");
        }
        if (memcmp(name, "moov", 4) == 0) {
            moov = body;
            have_moov = 1;
            break;
        }
        off = next;
    }
    if (!have_moov) {
        return tc_fail(TC_E_FORMAT, "mp4: no moov box found");
    }

    /* 1. QuickTime chapters take precedence, as in MP4GetChapters. */
    {
        mp4_box trak;
        int found = 0;
        tc_status st = mp4_qt_find_track(&f, moov, &trak, &found);
        if (st != TC_OK) {
            return st;
        }
        if (found) {
            mp4_qt_chapter *chapters = NULL;
            size_t count = 0;
            st = mp4_qt_read(&f, trak, &chapters, &count);
            if (st != TC_OK) {
                return st;
            }
            if (count > 0) {
                char **names = tc_calloc(count, sizeof(*names));
                int64_t *durations = tc_calloc(count, sizeof(*durations));
                if (!names || !durations) {
                    free(names);
                    free(durations);
                    mp4_qt_free(chapters, count);
                    return TC_E_NOMEM;
                }
                for (size_t i = 0; i < count; i++) {
                    names[i] = chapters[i].name;
                    chapters[i].name = NULL;
                    durations[i] = chapters[i].duration_ms;
                }
                mp4_qt_free(chapters, count);
                st = mp4_add_entry(d, names, durations, count);
                if (st != TC_OK) {
                    for (size_t i = 0; i < count; i++) {
                        free(names[i]);
                    }
                }
                free(names);
                free(durations);
                return st;
            }
            mp4_qt_free(chapters, count);
        }
    }

    /* 2. Nero chapters. */
    {
        mp4_box udta;
        int rc = mp4_child(&f, moov, "udta", &udta);
        if (rc == MP4_BOX_MALFORMED) {
            return tc_fail(TC_E_FORMAT, "mp4: malformed moov box");
        }
        if (rc == MP4_BOX_OK) {
            mp4_box chpl;
            rc = mp4_child(&f, udta, "chpl", &chpl);
            if (rc == MP4_BOX_MALFORMED) {
                return tc_fail(TC_E_FORMAT, "mp4: malformed udta box");
            }
            if (rc == MP4_BOX_OK) {
                mp4_nero_entry *entries = NULL;
                size_t count = 0;
                uint8_t version = 0;
                tc_status st = mp4_chpl_read(&f, chpl, &entries, &count, &version);
                if (st != TC_OK) {
                    return st;
                }
                if (count > 0) {
                    /* Version 1 start times are 100 ns units; version 0 uses
                     * movie timescale units. */
                    uint64_t scale = 10000000;
                    uint32_t movie_timescale = 0;
                    uint64_t movie_duration = 0;
                    int have_movie = mp4_movie_header(&f, moov, &movie_timescale,
                                                      &movie_duration);
                    if (version == 0) {
                        if (!have_movie || movie_timescale == 0) {
                            mp4_nero_free(entries, count);
                            return tc_fail(TC_E_FORMAT,
                                           "mp4: chpl version 0 without a movie timescale");
                        }
                        scale = movie_timescale;
                    }

                    char **names = tc_calloc(count, sizeof(*names));
                    int64_t *durations = tc_calloc(count, sizeof(*durations));
                    if (!names || !durations) {
                        free(names);
                        free(durations);
                        mp4_nero_free(entries, count);
                        return TC_E_NOMEM;
                    }

                    /* A duration is the gap to the next start time; the last
                     * chapter runs to the movie duration
                     * (MP4File::GetChapters). */
                    int64_t movie_ms = 0;
                    if (have_movie && movie_timescale != 0) {
                        movie_ms = (int64_t)mp4_ms_from(movie_duration,
                                                        movie_timescale);
                    }
                    int64_t sum_ms = 0;
                    for (size_t i = 0; i < count; i++) {
                        int64_t next_ms =
                            i + 1 < count
                                ? (int64_t)mp4_ms_from(entries[i + 1].start,
                                                       (uint32_t)scale)
                                : movie_ms;
                        names[i] = entries[i].name;
                        entries[i].name = NULL;
                        durations[i] = next_ms - sum_ms;
                        sum_ms += durations[i];
                    }
                    mp4_nero_free(entries, count);
                    st = mp4_add_entry(d, names, durations, count);
                    if (st != TC_OK) {
                        for (size_t i = 0; i < count; i++) {
                            free(names[i]);
                        }
                    }
                    free(names);
                    free(durations);
                    return st;
                }
                mp4_nero_free(entries, count);
            }
        }
    }

    /* 3. No chapters anywhere. */
    return mp4_fallback_entry(&f, moov, d);
}

tc_status tc_mp4_parse_file(const char *path, tc_data *d) {
    char *buf = NULL;
    size_t len = 0;
    tc_status st = tc_read_file(path, &buf, &len);
    if (st != TC_OK) {
        return st;
    }
    st = mp4_parse((const uint8_t *)buf, len, d);
    free(buf);
    return st;
}
