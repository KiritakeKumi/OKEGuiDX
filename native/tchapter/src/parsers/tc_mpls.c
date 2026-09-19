/*
 * MPLS (Blu-ray playlist) chapter parser.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Ported from TChapter/Parsing/MPLSParser.cs and TChapter/Object/MPLS.cs. The
 * file is a big-endian container: a fixed header holds the offsets of the
 * PlayList and PlayListMark boxes, and every box carries its own length. The
 * original seeks to the end of each box once it is done with it, so trailing or
 * unknown content inside a box is skipped rather than rejected; that behaviour
 * is reproduced here.
 *
 * One entry is produced per PlayItem, mirroring MPLSParser.GetChapters: the
 * entry source is the clip information file name (plus "&<name>" for every
 * multi-angle clip), the duration is the play item's IN/OUT delta and the
 * chapters are the marks of type 0x01 that reference the play item.
 *
 * Time base: MPLS timestamps are 45 kHz, not nanoseconds. MPLSParser.PTS2Time
 * converts one as
 *
 *     total   = pts / 45000M                       (decimal division)
 *     seconds = Math.Floor(total)
 *     millis  = Math.Round((total - seconds) * 1000M, AwayFromZero)
 *     TimeSpan(0, 0, 0, (int)seconds, (int)millis)
 *
 * 45000 = 2^3 * 3^2 * 5^4, so the decimal quotient is inexact, but a remainder
 * can never be exactly half a millisecond: rem/45 == k + 1/2 would require
 * 2*rem = 45*(2k+1), which is even = odd. The nearest half is therefore at
 * least 1/90 away and the rounding is deterministic. It is exactly equal to
 *
 *     seconds = pts / 45000
 *     millis  = (2 * (pts % 45000) + 45) / 90
 *
 * which was checked against the .NET implementation for every remainder in
 * [0, 45000) and for random 32-bit timestamps.
 *
 * Deliberate deviations from the C# original, all reported:
 *  - the original throws InvalidOperationException when a play item has no
 *    primary video stream entry; here the parse fails with TC_E_FORMAT;
 *  - SubPath boxes are skipped by their declared length instead of being
 *    decoded, because their content is never used for chapters. The original
 *    decodes them, so a sub path whose interior is malformed is accepted here
 *    where the original would throw;
 *  - ExtensionData is decoded but not otherwise used, matching the original
 *    (which also only decodes it).
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_internal.h"

/* ------------------------------------------------------------------ */
/* Reader                                                             */
/* ------------------------------------------------------------------ */

typedef struct mpls_reader {
    const uint8_t *data;
    uint64_t len;
    uint64_t pos;
} mpls_reader;

/* Reads `n` bytes and optionally returns a pointer to them. Returns 0 when the
 * file ends first, which is the C counterpart of EndOfStreamException. */
static int mpls_take(mpls_reader *r, uint64_t n, const uint8_t **out) {
    if (r->pos > r->len || r->len - r->pos < n) {
        return 0;
    }
    if (out) {
        *out = r->data + (size_t)r->pos;
    }
    r->pos += n;
    return 1;
}

static int mpls_skip(mpls_reader *r, uint64_t n) { return mpls_take(r, n, NULL); }

static int mpls_u8(mpls_reader *r, uint8_t *v) {
    const uint8_t *p = NULL;
    if (!mpls_take(r, 1, &p)) {
        return 0;
    }
    *v = tc_be8(p);
    return 1;
}

static int mpls_u16(mpls_reader *r, uint16_t *v) {
    const uint8_t *p = NULL;
    if (!mpls_take(r, 2, &p)) {
        return 0;
    }
    *v = tc_be16(p);
    return 1;
}

static int mpls_u32(mpls_reader *r, uint32_t *v) {
    const uint8_t *p = NULL;
    if (!mpls_take(r, 4, &p)) {
        return 0;
    }
    *v = tc_be32(p);
    return 1;
}

static int mpls_seek(mpls_reader *r, uint64_t pos) {
    if (pos > r->len) {
        return 0;
    }
    r->pos = pos;
    return 1;
}

/* Seeks to the end of a box whose body starts at `body` and is `body_len`
 * bytes long. The original computes this as
 * `stream.Skip(Length - (stream.Position - position))`, which is simply
 * `position + Length`; content that read past the box end is therefore seeked
 * back, exactly as the original does. */
static int mpls_box_end(mpls_reader *r, uint64_t body, uint32_t body_len) {
    if (body > r->len || (uint64_t)body_len > r->len - body) {
        return 0;
    }
    r->pos = body + (uint64_t)body_len;
    return 1;
}

static tc_status mpls_truncated(const char *what) {
    return tc_fail(TC_E_FORMAT, "mpls: truncated %s", what);
}

/* ------------------------------------------------------------------ */
/* Time conversion                                                    */
/* ------------------------------------------------------------------ */

/* Converts a 45 kHz timestamp to nanoseconds; see the file comment for why this
 * integer form is exactly equivalent to the original decimal arithmetic. */
static int64_t mpls_pts_to_ns(uint32_t pts) {
    uint32_t seconds = pts / 45000u;
    uint32_t rem = pts % 45000u;
    /* round-half-away-from-zero(rem / 45), computed as floor(rem/45 + 1/2). */
    uint32_t millis = (2u * rem + 45u) / 90u;
    return (int64_t)seconds * 1000000000LL + (int64_t)millis * 1000000LL;
}

/* ------------------------------------------------------------------ */
/* Boxes                                                              */
/* ------------------------------------------------------------------ */

/* MPLS.cs reads StreamAttributes for every stream entry, but the chapter parser
 * only consumes the video info byte of the first primary video entry. The
 * attribute box is still walked entry by entry so that malformed content is
 * detected where the original would detect it. */
static tc_status mpls_parse_stream_attributes(mpls_reader *r, uint8_t *video_info) {
    uint8_t length = 0;
    uint8_t coding = 0;
    *video_info = 0;
    if (!mpls_u8(r, &length)) {
        return mpls_truncated("stream attributes");
    }
    const uint64_t body = r->pos;
    if (!mpls_u8(r, &coding)) {
        return mpls_truncated("stream attributes");
    }
    switch (coding) {
    case 0x01: /* MPEG-1 video */
    case 0x02: /* MPEG-2 video */
    case 0x1B: /* MPEG-4 AVC video */
    case 0xEA: /* SMPTE VC-1 video */
    case 0x20: /* MPEG-4 MVC video */
    case 0x24: /* HEVC video */
        if (!mpls_u8(r, video_info)) {
            return mpls_truncated("stream attributes");
        }
        break;
    case 0x03: /* MPEG-1 audio */
    case 0x04: /* MPEG-2 audio */
    case 0x80: /* LPCM */
    case 0x81: /* AC-3 */
    case 0x82: /* DTS */
    case 0x83: /* TrueHD */
    case 0x84: /* DD+ */
    case 0x85: /* DTS-HD HR */
    case 0x86: /* DTS-HD MA */
    case 0xA1: /* DD+ (secondary) */
    case 0xA2: /* DTS-HD (secondary) */
        /* audio info byte plus a 3-byte language code */
        if (!mpls_skip(r, 4)) {
            return mpls_truncated("stream attributes");
        }
        break;
    case 0x90: /* presentation graphics */
    case 0x91: /* interactive graphics */
    case 0xA0: /* secondary graphics */
        if (!mpls_skip(r, 3)) {
            return mpls_truncated("stream attributes");
        }
        break;
    case 0x92: /* text subtitle: character code plus language */
        if (!mpls_skip(r, 4)) {
            return mpls_truncated("stream attributes");
        }
        break;
    default:
        /* The original logs an unknown coding type and moves on. */
        break;
    }
    if (!mpls_box_end(r, body, length)) {
        return mpls_truncated("stream attributes");
    }
    return TC_OK;
}

static tc_status mpls_parse_stream_entry(mpls_reader *r, uint8_t *video_info) {
    uint8_t length = 0;
    uint8_t stream_type = 0;
    if (!mpls_u8(r, &length)) {
        return mpls_truncated("stream entry");
    }
    const uint64_t body = r->pos;
    if (!mpls_u8(r, &stream_type)) {
        return mpls_truncated("stream entry");
    }
    switch (stream_type) {
    case 0x02:
    case 0x04:
        /* RefToSubPathID and RefToSubClipID */
        if (!mpls_skip(r, 2)) {
            return mpls_truncated("stream entry");
        }
        break;
    case 0x01:
    case 0x03:
        break;
    default:
        /* The original logs an unknown stream type and moves on. */
        break;
    }
    /* RefToStreamPID */
    if (!mpls_skip(r, 2)) {
        return mpls_truncated("stream entry");
    }
    if (!mpls_box_end(r, body, length)) {
        return mpls_truncated("stream entry");
    }
    return mpls_parse_stream_attributes(r, video_info);
}

/* Reads the STN table far enough to learn the frame rate of the first primary
 * video entry, which is all MPLSParser.GetChapters uses it for. */
static tc_status mpls_parse_stn_table(mpls_reader *r, int32_t *frame_rate) {
    uint16_t length = 0;
    uint8_t counts[7];
    if (!mpls_u16(r, &length)) {
        return mpls_truncated("STN table");
    }
    const uint64_t body = r->pos;
    /* 2 reserved bytes, then the seven entry counts. */
    if (!mpls_skip(r, 2)) {
        return mpls_truncated("STN table");
    }
    for (size_t i = 0; i < 7; i++) {
        if (!mpls_u8(r, &counts[i])) {
            return mpls_truncated("STN table");
        }
    }
    /* 5 reserved bytes. */
    if (!mpls_skip(r, 5)) {
        return mpls_truncated("STN table");
    }

    /* MPLS.cs reads the entries in this order, which differs from the Blu-ray
     * specification (it reads secondary PG before primary IG):
     *   primary video, primary audio, primary PG, secondary PG,
     *   primary IG, secondary audio, secondary video. */
    static const size_t order[7] = {0, 1, 2, 6, 3, 4, 5};
    int primary_video_seen = 0;
    for (size_t k = 0; k < 7; k++) {
        size_t kind = order[k];
        for (uint8_t i = 0; i < counts[kind]; i++) {
            uint8_t video_info = 0;
            tc_status st = mpls_parse_stream_entry(r, &video_info);
            if (st != TC_OK) {
                return st;
            }
            if (kind == 0 && !primary_video_seen) {
                /* Config.FRAME_RATE is indexed with the low nibble of the video
                 * info byte; a non-video coding type leaves the byte at zero,
                 * which selects the "reserved" slot, just as in the original. */
                *frame_rate = (int32_t)(video_info & 0x0Fu);
                primary_video_seen = 1;
            }
        }
    }

    if (!mpls_box_end(r, body, length)) {
        return mpls_truncated("STN table");
    }
    return TC_OK;
}

/* ------------------------------------------------------------------ */
/* Play list                                                          */
/* ------------------------------------------------------------------ */

typedef struct mpls_play_item {
    char *source;      /* FullName: clip information file name plus angles */
    uint32_t in_time;
    uint32_t out_time;
    int32_t frame_rate; /* Config.FRAME_RATE index, -1 without primary video */
} mpls_play_item;

static void mpls_free_items(mpls_play_item *items, size_t count) {
    if (!items) {
        return;
    }
    for (size_t i = 0; i < count; i++) {
        free(items[i].source);
    }
    free(items);
}

static tc_status mpls_parse_play_item(mpls_reader *r, mpls_play_item *item) {
    uint16_t length = 0;
    const uint8_t *clip = NULL;
    uint16_t flags1 = 0;
    uint8_t ref_to_stc_id = 0;
    uint32_t in_time = 0;
    uint32_t out_time = 0;
    uint8_t flags2 = 0;
    uint8_t still_mode = 0;
    uint16_t still_time = 0;
    int32_t frame_rate = -1;
    char angle_suffix[256 * 6]; /* "&" plus 5 name bytes per angle */
    size_t suffix_len = 0;

    if (!mpls_u16(r, &length)) {
        return mpls_truncated("play item");
    }
    const uint64_t body = r->pos;

    /* ClipName: a 5-byte information file name and a 4-byte codec id. Only the
     * file name ends up in FullName; the codec id is skipped. */
    if (!mpls_take(r, 5, &clip) || !mpls_skip(r, 4)) {
        return mpls_truncated("play item clip name");
    }
    if (!mpls_u16(r, &flags1) || !mpls_u8(r, &ref_to_stc_id) ||
        !mpls_u32(r, &in_time) || !mpls_u32(r, &out_time)) {
        return mpls_truncated("play item header");
    }
    (void)ref_to_stc_id;
    /* UO mask table (8 bytes), then the random access flag, still mode and
     * still time. */
    if (!mpls_skip(r, 8) || !mpls_u8(r, &flags2) || !mpls_u8(r, &still_mode) ||
        !mpls_u16(r, &still_time)) {
        return mpls_truncated("play item flags");
    }
    (void)flags2;
    (void)still_mode;
    (void)still_time;

    /* IsMultiAngle is bit 4 of the first flag field. Every additional angle
     * contributes "&<clip information file name>" to FullName. */
    if (((flags1 >> 4) & 1u) != 0) {
        uint8_t angle_count = 0;
        uint8_t angle_flags = 0;
        if (!mpls_u8(r, &angle_count) || !mpls_u8(r, &angle_flags)) {
            return mpls_truncated("multi-angle header");
        }
        (void)angle_flags;
        for (unsigned i = 1; i < (unsigned)angle_count; i++) {
            const uint8_t *angle = NULL;
            uint8_t angle_ref = 0;
            if (!mpls_take(r, 5, &angle) || !mpls_skip(r, 4) || !mpls_u8(r, &angle_ref)) {
                return mpls_truncated("multi-angle entry");
            }
            (void)angle_ref;
            if (suffix_len + 6 <= sizeof(angle_suffix)) {
                angle_suffix[suffix_len++] = '&';
                memcpy(angle_suffix + suffix_len, angle, 5);
                suffix_len += 5;
            }
        }
    }

    tc_status st = mpls_parse_stn_table(r, &frame_rate);
    if (st != TC_OK) {
        return st;
    }
    if (!mpls_box_end(r, body, length)) {
        return mpls_truncated("play item");
    }

    char *source = tc_malloc(5 + suffix_len + 1);
    if (!source) {
        return TC_E_NOMEM;
    }
    memcpy(source, clip, 5);
    memcpy(source + 5, angle_suffix, suffix_len);
    source[5 + suffix_len] = '\0';

    item->source = source;
    item->in_time = in_time;
    item->out_time = out_time;
    item->frame_rate = frame_rate;
    return TC_OK;
}

static tc_status mpls_parse_playlist(mpls_reader *r, mpls_play_item **out, size_t *out_count) {
    uint32_t length = 0;
    uint16_t item_count = 0;
    uint16_t sub_path_count = 0;
    mpls_play_item *items = NULL;

    if (!mpls_u32(r, &length)) {
        return mpls_truncated("play list");
    }
    const uint64_t body = r->pos;
    if (!mpls_skip(r, 2) || !mpls_u16(r, &item_count) || !mpls_u16(r, &sub_path_count)) {
        return mpls_truncated("play list");
    }

    if (item_count > 0) {
        items = tc_calloc(item_count, sizeof(*items));
        if (!items) {
            return TC_E_NOMEM;
        }
    }
    for (size_t i = 0; i < (size_t)item_count; i++) {
        tc_status st = mpls_parse_play_item(r, &items[i]);
        if (st != TC_OK) {
            mpls_free_items(items, (size_t)item_count);
            return st;
        }
    }

    /* Sub paths never carry chapter information; skipping them by their
     * declared length is what the original effectively does. */
    for (size_t i = 0; i < (size_t)sub_path_count; i++) {
        uint32_t sub_length = 0;
        if (!mpls_u32(r, &sub_length)) {
            mpls_free_items(items, (size_t)item_count);
            return mpls_truncated("sub path");
        }
        if (!mpls_box_end(r, r->pos, sub_length)) {
            mpls_free_items(items, (size_t)item_count);
            return mpls_truncated("sub path");
        }
    }

    if (!mpls_box_end(r, body, length)) {
        mpls_free_items(items, (size_t)item_count);
        return mpls_truncated("play list");
    }
    *out = items;
    *out_count = (size_t)item_count;
    return TC_OK;
}

/* ------------------------------------------------------------------ */
/* Play list marks                                                    */
/* ------------------------------------------------------------------ */

typedef struct mpls_mark {
    uint8_t mark_type;
    uint16_t ref_play_item;
    uint32_t timestamp;
} mpls_mark;

static tc_status mpls_parse_marks(mpls_reader *r, mpls_mark **out, size_t *out_count) {
    uint32_t length = 0;
    uint16_t count = 0;
    mpls_mark *marks = NULL;

    if (!mpls_u32(r, &length)) {
        return mpls_truncated("play list mark");
    }
    const uint64_t body = r->pos;
    if (!mpls_u16(r, &count)) {
        return mpls_truncated("play list mark");
    }

    if (count > 0) {
        marks = tc_calloc(count, sizeof(*marks));
        if (!marks) {
            return TC_E_NOMEM;
        }
    }
    for (size_t i = 0; i < (size_t)count; i++) {
        /* 1 reserved byte, then the 18-byte mark body. */
        uint8_t reserved = 0;
        uint8_t type = 0;
        uint16_t ref = 0;
        uint32_t timestamp = 0;
        uint16_t entry_es_pid = 0;
        uint32_t duration = 0;
        if (!mpls_u8(r, &reserved) || !mpls_u8(r, &type) || !mpls_u16(r, &ref) ||
            !mpls_u32(r, &timestamp) || !mpls_u16(r, &entry_es_pid) ||
            !mpls_u32(r, &duration)) {
            free(marks);
            return mpls_truncated("mark");
        }
        (void)reserved;
        (void)entry_es_pid;
        (void)duration;
        marks[i].mark_type = type;
        marks[i].ref_play_item = ref;
        marks[i].timestamp = timestamp;
    }

    if (!mpls_box_end(r, body, length)) {
        free(marks);
        return mpls_truncated("play list mark");
    }
    *out = marks;
    *out_count = (size_t)count;
    return TC_OK;
}

/* ------------------------------------------------------------------ */
/* Extension data                                                     */
/* ------------------------------------------------------------------ */

/* ExtensionData carries metadata blocks that MPLSParser never reads, but the
 * original still walks the box, so a malformed one is an error there too. */
static tc_status mpls_parse_extension_data(mpls_reader *r) {
    uint32_t length = 0;
    uint8_t count = 0;

    if (!mpls_u32(r, &length)) {
        return mpls_truncated("extension data");
    }
    if (length == 0) {
        return TC_OK;
    }
    /* DataBlockStartAddress, then 3 reserved bytes and the entry count. */
    if (!mpls_skip(r, 4) || !mpls_skip(r, 3) || !mpls_u8(r, &count)) {
        return mpls_truncated("extension data");
    }
    for (size_t i = 0; i < (size_t)count; i++) {
        /* ExtDataType, ExtDataVersion, ExtDataStartAddress, ExtDataLength. */
        if (!mpls_skip(r, 12)) {
            return mpls_truncated("extension data entry");
        }
    }
    return TC_OK;
}

/* ------------------------------------------------------------------ */
/* Header                                                             */
/* ------------------------------------------------------------------ */

static tc_status mpls_parse_header(mpls_reader *r, uint32_t *playlist_start,
                                   uint32_t *mark_start, uint32_t *ext_start) {
    const uint8_t *p = NULL;

    if (!mpls_take(r, 4, &p)) {
        return mpls_truncated("header");
    }
    if (memcmp(p, "MPLS", 4) != 0) {
        return tc_fail(TC_E_FORMAT, "mpls: not a Blu-ray playlist (bad magic)");
    }
    if (!mpls_take(r, 4, &p)) {
        return mpls_truncated("version");
    }
    if (memcmp(p, "0100", 4) != 0 && memcmp(p, "0200", 4) != 0 &&
        memcmp(p, "0300", 4) != 0) {
        return tc_fail(TC_E_FORMAT, "mpls: unsupported version \"%.4s\"", (const char *)p);
    }
    if (!mpls_u32(r, playlist_start) || !mpls_u32(r, mark_start) ||
        !mpls_u32(r, ext_start)) {
        return mpls_truncated("header");
    }
    /* 20 reserved bytes, then the AppInfoPlayList box. Its content does not
     * influence chapters, but the box is walked so that a truncated header is
     * still reported. */
    if (!mpls_skip(r, 20)) {
        return mpls_truncated("header");
    }
    uint32_t app_length = 0;
    if (!mpls_u32(r, &app_length)) {
        return mpls_truncated("AppInfoPlayList");
    }
    const uint64_t app_body = r->pos;
    uint8_t reserved = 0;
    uint8_t playback_type = 0;
    if (!mpls_u8(r, &reserved) || !mpls_u8(r, &playback_type)) {
        return mpls_truncated("AppInfoPlayList");
    }
    (void)reserved;
    if (playback_type == 0x02 || playback_type == 0x03) {
        uint16_t playback_count = 0;
        if (!mpls_u16(r, &playback_count)) {
            return mpls_truncated("AppInfoPlayList");
        }
        (void)playback_count;
    } else {
        uint16_t reserved2 = 0;
        uint16_t flags = 0;
        if (!mpls_u16(r, &reserved2) || !mpls_skip(r, 8) || !mpls_u16(r, &flags)) {
            return mpls_truncated("AppInfoPlayList");
        }
        (void)reserved2;
        (void)flags;
    }
    if (!mpls_box_end(r, app_body, app_length)) {
        return mpls_truncated("AppInfoPlayList");
    }
    return TC_OK;
}

/* ------------------------------------------------------------------ */
/* Chapters                                                           */
/* ------------------------------------------------------------------ */

/* Config.FRAME_RATE from Config.cs, as an exact rational. Indices 0 and 5 are
 * "reserved" and map to zero. */
static void mpls_set_fps(tc_entry *e, int32_t index) {
    switch (index) {
    case 1:  e->fps_num = 24000; e->fps_den = 1001; break;
    case 2:  e->fps_num = 24;    e->fps_den = 1;    break;
    case 3:  e->fps_num = 25;    e->fps_den = 1;    break;
    case 4:  e->fps_num = 30000; e->fps_den = 1001; break;
    case 6:  e->fps_num = 50;    e->fps_den = 1;    break;
    case 7:  e->fps_num = 60000; e->fps_den = 1001; break;
    default: e->fps_num = 0;     e->fps_den = 1;    break;
    }
}

static tc_status mpls_build_entries(const mpls_play_item *items, size_t item_count,
                                    const mpls_mark *marks, size_t mark_count, tc_data *d) {
    for (size_t i = 0; i < item_count; i++) {
        const mpls_play_item *pi = &items[i];

        if (pi->frame_rate < 0) {
            return tc_fail(TC_E_FORMAT,
                           "mpls: play item %lu has no primary video stream",
                           (unsigned long)i);
        }

        tc_entry *e = tc_data_add_entry(d);
        if (!e) {
            return TC_E_NOMEM;
        }
        e->source = tc_strdup(pi->source);
        e->title = tc_strdup("");
        if (!e->source || !e->title) {
            return TC_E_NOMEM;
        }
        mpls_set_fps(e, pi->frame_rate);
        e->duration_ns = mpls_pts_to_ns(pi->out_time - pi->in_time);

        /* The original filters marks by type and play item reference; the
         * first match fixes the offset, which is the IN time when that is
         * earlier than the first mark. */
        size_t first = mark_count;
        for (size_t k = 0; k < mark_count; k++) {
            if (marks[k].mark_type == 0x01 && (size_t)marks[k].ref_play_item == i) {
                first = k;
                break;
            }
        }

        if (first == mark_count) {
            char *name = tc_strdup("Chapter 1");
            if (!name) {
                return TC_E_NOMEM;
            }
            tc_status st = tc_entry_add_chapter(e, name, 0, -1);
            if (st != TC_OK) {
                return st;
            }
            continue;
        }

        uint32_t offset = marks[first].timestamp;
        if (pi->in_time < offset) {
            offset = pi->in_time;
        }

        unsigned index = 1;
        for (size_t k = first; k < mark_count; k++) {
            if (marks[k].mark_type != 0x01 || (size_t)marks[k].ref_play_item != i) {
                continue;
            }
            char name[32];
            /* ChapterName.Get(): "Chapter" plus the 1-based index with a
             * minimum of two digits ("D2"). */
            snprintf(name, sizeof(name), "Chapter %02u", index);
            char *copy = tc_strdup(name);
            if (!copy) {
                return TC_E_NOMEM;
            }
            tc_status st = tc_entry_add_chapter(
                e, copy, mpls_pts_to_ns(marks[k].timestamp - offset), -1);
            if (st != TC_OK) {
                return st;
            }
            index++;
        }
    }
    return TC_OK;
}

/* ------------------------------------------------------------------ */
/* Entry points                                                       */
/* ------------------------------------------------------------------ */

static tc_status mpls_parse_buffer(const void *buf, size_t len, tc_data *d) {
    if (!buf) {
        return tc_fail(TC_E_INVALID, "mpls: null buffer");
    }
    if (!d) {
        return tc_fail(TC_E_INVALID, "mpls: null data");
    }

    mpls_reader r;
    r.data = (const uint8_t *)buf;
    r.len = (uint64_t)len;
    r.pos = 0;

    uint32_t playlist_start = 0;
    uint32_t mark_start = 0;
    uint32_t ext_start = 0;
    tc_status st = mpls_parse_header(&r, &playlist_start, &mark_start, &ext_start);
    if (st != TC_OK) {
        return st;
    }
    (void)ext_start;

    mpls_play_item *items = NULL;
    size_t item_count = 0;
    if (!mpls_seek(&r, playlist_start)) {
        return tc_fail(TC_E_FORMAT, "mpls: play list offset %lu is past the end of the file",
                       (unsigned long)playlist_start);
    }
    st = mpls_parse_playlist(&r, &items, &item_count);
    if (st != TC_OK) {
        return st;
    }

    mpls_mark *marks = NULL;
    size_t mark_count = 0;
    if (!mpls_seek(&r, mark_start)) {
        mpls_free_items(items, item_count);
        return tc_fail(TC_E_FORMAT, "mpls: mark offset %lu is past the end of the file",
                       (unsigned long)mark_start);
    }
    st = mpls_parse_marks(&r, &marks, &mark_count);
    if (st != TC_OK) {
        mpls_free_items(items, item_count);
        return st;
    }

    /* The original seeks to ExtensionData only when the header carries a
     * non-zero address; the box has no effect on chapters either way. */
    if (ext_start != 0) {
        if (!mpls_seek(&r, ext_start)) {
            free(marks);
            mpls_free_items(items, item_count);
            return tc_fail(TC_E_FORMAT, "mpls: extension data offset %lu is past the end of the file",
                           (unsigned long)ext_start);
        }
        st = mpls_parse_extension_data(&r);
        if (st != TC_OK) {
            free(marks);
            mpls_free_items(items, item_count);
            return st;
        }
    }

    st = mpls_build_entries(items, item_count, marks, mark_count, d);
    free(marks);
    mpls_free_items(items, item_count);
    if (st != TC_OK) {
        return st;
    }
    if (d->entry_count == 0) {
        return tc_fail(TC_E_FORMAT, "mpls: the play list contains no play items");
    }
    return TC_OK;
}

tc_status tc_mpls_parse_mem(const void *buf, size_t len, const char *hint, tc_data *d) {
    (void)hint;
    return mpls_parse_buffer(buf, len, d);
}

tc_status tc_mpls_parse_file(const char *path, tc_data *d) {
    char *buf = NULL;
    size_t len = 0;
    tc_status st = tc_read_file(path, &buf, &len);
    if (st != TC_OK) {
        return st;
    }
    st = mpls_parse_buffer(buf, len, d);
    free(buf);
    return st;
}
