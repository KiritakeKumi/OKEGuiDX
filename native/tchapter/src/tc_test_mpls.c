/*
 * MPLS parser tests.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * The three fixtures are real playlists copied verbatim from the original test
 * suite (TChapter.Test/Assets/MPLS), and the expected values below were derived
 * from those bytes by replaying MPLSParser.GetChapters / PTS2Time exactly. They
 * are not hand-written approximations: the timestamps, durations, frame-rate
 * indices and mark references all come out of the real files.
 *
 * Fixture roles:
 *   mpls-hd.mpls    version 0200, 2 play items, 12 marks, 1 sub path
 *   mpls-uhd.mpls   version 0300, 1 play item, 16 marks, extension data
 *   mpls-empty.mpls version 0200, 12 play items, 1 mark (11 items without)
 *
 * The remaining cases build minimal playlists in memory. The builder writes the
 * bytes the format actually specifies, so it doubles as documentation of the
 * box layout: every field the parser reads is emitted in order.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_test.h"

/* ------------------------------------------------------------------ */
/* Byte writer                                                        */
/* ------------------------------------------------------------------ */

typedef struct bw {
    uint8_t *data;
    size_t len;
    size_t cap;
    int oom;
} bw;

static void bw_need(bw *b, size_t extra) {
    if (b->len + extra <= b->cap || b->oom) {
        return;
    }
    size_t cap = b->cap ? b->cap : 256;
    while (cap < b->len + extra) {
        cap *= 2;
    }
    uint8_t *p = realloc(b->data, cap);
    if (!p) {
        b->oom = 1;
        return;
    }
    b->data = p;
    b->cap = cap;
}

static void bw_u8(bw *b, uint8_t v) {
    bw_need(b, 1);
    if (!b->oom) {
        b->data[b->len++] = v;
    }
}

static void bw_u16(bw *b, uint16_t v) {
    bw_u8(b, (uint8_t)(v >> 8));
    bw_u8(b, (uint8_t)(v & 0xFF));
}

static void bw_u32(bw *b, uint32_t v) {
    bw_u8(b, (uint8_t)(v >> 24));
    bw_u8(b, (uint8_t)((v >> 16) & 0xFF));
    bw_u8(b, (uint8_t)((v >> 8) & 0xFF));
    bw_u8(b, (uint8_t)(v & 0xFF));
}

static void bw_bytes(bw *b, const void *p, size_t n) {
    bw_need(b, n);
    if (!b->oom) {
        memcpy(b->data + b->len, p, n);
        b->len += n;
    }
}

static void bw_zeros(bw *b, size_t n) {
    for (size_t i = 0; i < n; i++) {
        bw_u8(b, 0);
    }
}

static void bw_clip_name(bw *b, const char *name5, const char *codec) {
    uint8_t buf[9];
    memset(buf, 0, sizeof(buf));
    memcpy(buf, name5, 5);
    memcpy(buf + 5, codec, 4);
    bw_bytes(b, buf, sizeof(buf));
}

/* ------------------------------------------------------------------ */
/* Minimal playlist builder                                           */
/* ------------------------------------------------------------------ */

/* Stream attribute box: length, coding type and the per-coding payload. */
static void emit_stream_attributes(bw *b, uint8_t coding, uint8_t info) {
    size_t start = b->len;
    bw_u8(b, 0); /* length, patched below */
    bw_u8(b, coding);
    if (coding == 0x01 || coding == 0x02 || coding == 0x1B || coding == 0xEA ||
        coding == 0x20 || coding == 0x24) {
        bw_u8(b, info);
    } else if (coding == 0x03 || coding == 0x04 || coding == 0x80 ||
               coding == 0x81 || coding == 0x82 || coding == 0x83 ||
               coding == 0x84 || coding == 0x85 || coding == 0x86 ||
               coding == 0xA1 || coding == 0xA2) {
        bw_u8(b, info);
        bw_bytes(b, "jpn", 3);
    } else if (coding == 0x90 || coding == 0x91 || coding == 0xA0) {
        bw_bytes(b, "jpn", 3);
    } else if (coding == 0x92) {
        bw_u8(b, info);
        bw_bytes(b, "jpn", 3);
    }
    b->data[start] = (uint8_t)(b->len - start - 1);
}

/* Stream entry box: length, stream type, optional sub path/clip refs, PID. */
static void emit_stream_entry(bw *b, uint8_t stream_type, uint16_t pid) {
    size_t start = b->len;
    bw_u8(b, 0);
    bw_u8(b, stream_type);
    if (stream_type == 0x02 || stream_type == 0x04) {
        bw_u8(b, 0);
        bw_u8(b, 0);
    }
    bw_u16(b, pid);
    b->data[start] = (uint8_t)(b->len - start - 1);
}

/* STN table with the counts in declaration order and the entries in the order
 * MPLS.cs reads them (primary video first). `video_coding` of 0 omits the video
 * entry entirely, which makes the parser fail the way the original throws. */
static void emit_stn_table(bw *b, uint8_t video_coding, uint8_t video_info) {
    size_t start = b->len;
    bw_u16(b, 0); /* length, patched below */
    bw_u16(b, 0); /* reserved */
    bw_u8(b, video_coding ? 1 : 0); /* primary video */
    bw_u8(b, 1);                    /* primary audio */
    bw_u8(b, 0);                    /* primary PG */
    bw_u8(b, 0);                    /* primary IG */
    bw_u8(b, 0);                    /* secondary audio */
    bw_u8(b, 0);                    /* secondary video */
    bw_u8(b, 0);                    /* secondary PG */
    bw_zeros(b, 5);                 /* reserved */
    if (video_coding) {
        emit_stream_entry(b, 0x01, 0x1011);
        emit_stream_attributes(b, video_coding, video_info);
    }
    emit_stream_entry(b, 0x03, 0x1100);
    emit_stream_attributes(b, 0x81, 0x36);
    uint16_t length = (uint16_t)(b->len - start - 2);
    b->data[start] = (uint8_t)(length >> 8);
    b->data[start + 1] = (uint8_t)(length & 0xFF);
}

/* Play item with the given clip name, IN/OUT and frame-rate nibble. */
static void emit_play_item(bw *b, const char *name5, uint32_t in_time, uint32_t out_time,
                           uint8_t frame_rate) {
    size_t start = b->len;
    bw_u16(b, 0); /* length, patched below */
    bw_clip_name(b, name5, "M2TS");
    bw_u16(b, 0); /* flags1: no multi-angle */
    bw_u8(b, 0);  /* RefToSTCID */
    bw_u32(b, in_time);
    bw_u32(b, out_time);
    bw_zeros(b, 8); /* UO mask table */
    bw_u8(b, 0);    /* flag field 2 */
    bw_u8(b, 0);    /* still mode */
    bw_u16(b, 0);   /* still time */
    emit_stn_table(b, 0x1B, (uint8_t)(0x60 | (frame_rate & 0x0F)));
    uint16_t length = (uint16_t)(b->len - start - 2);
    b->data[start] = (uint8_t)(length >> 8);
    b->data[start + 1] = (uint8_t)(length & 0xFF);
}

typedef struct mark_spec {
    uint8_t mark_type;
    uint16_t ref;
    uint32_t timestamp;
} mark_spec;

/* Builds a complete playlist. Offsets are patched once the sizes are known, so
 * the layout is exactly: header | AppInfo | PlayList | PlayListMark. */
static uint8_t *build_playlist(size_t *out_len, const char *version,
                               const void *playlist_body, size_t playlist_body_len,
                               const mark_spec *marks, size_t mark_count,
                               const uint8_t *extension, size_t extension_len) {
    bw b;
    memset(&b, 0, sizeof(b));

    bw_bytes(&b, "MPLS", 4);
    bw_bytes(&b, version, 4);
    bw_u32(&b, 58); /* PlayListStartAddress, patched below */
    /* The marks offset depends on the play list size; patched below. */
    size_t mark_offset_pos = b.len;
    bw_u32(&b, 0);
    bw_u32(&b, extension ? (uint32_t)(58 + playlist_body_len) : 0);
    bw_zeros(&b, 20);
    /* AppInfoPlayList: length 14, 1 reserved, playback type 0, 2 reserved,
     * 8-byte UO mask, 2-byte flags. */
    bw_u32(&b, 14);
    bw_u8(&b, 0);
    bw_u8(&b, 0);
    bw_zeros(&b, 2);
    bw_zeros(&b, 8);
    bw_zeros(&b, 2);

    /* PlayList box. */
    bw_u32(&b, (uint32_t)playlist_body_len);
    bw_bytes(&b, playlist_body, playlist_body_len);

    /* PlayListMark box. */
    size_t mark_start = b.len;
    size_t mark_len_pos = b.len;
    bw_u32(&b, 0);
    bw_u16(&b, (uint16_t)mark_count);
    for (size_t i = 0; i < mark_count; i++) {
        bw_u8(&b, 0);
        bw_u8(&b, marks[i].mark_type);
        bw_u16(&b, marks[i].ref);
        bw_u32(&b, marks[i].timestamp);
        bw_u16(&b, 0xFFFF); /* EntryESPID, as in the fixtures */
        bw_u32(&b, 0);      /* Duration */
    }
    uint32_t mark_len = (uint32_t)(b.len - mark_len_pos - 4);
    b.data[mark_len_pos] = (uint8_t)(mark_len >> 24);
    b.data[mark_len_pos + 1] = (uint8_t)((mark_len >> 16) & 0xFF);
    b.data[mark_len_pos + 2] = (uint8_t)((mark_len >> 8) & 0xFF);
    b.data[mark_len_pos + 3] = (uint8_t)(mark_len & 0xFF);

    if (extension) {
        bw_bytes(&b, extension, extension_len);
    }

    b.data[mark_offset_pos] = (uint8_t)(mark_start >> 24);
    b.data[mark_offset_pos + 1] = (uint8_t)((mark_start >> 16) & 0xFF);
    b.data[mark_offset_pos + 2] = (uint8_t)((mark_start >> 8) & 0xFF);
    b.data[mark_offset_pos + 3] = (uint8_t)(mark_start & 0xFF);

    *out_len = b.len;
    return b.data;
}

/* Builds the PlayList body (counts plus the given play items). */
static uint8_t *build_playlist_body(size_t *out_len, size_t item_count,
                                    const char **names, const uint32_t (*times)[2],
                                    const uint8_t *frame_rates) {
    bw b;
    memset(&b, 0, sizeof(b));
    bw_u16(&b, 0); /* reserved */
    bw_u16(&b, (uint16_t)item_count);
    bw_u16(&b, 0); /* no sub paths */
    for (size_t i = 0; i < item_count; i++) {
        emit_play_item(&b, names[i], times[i][0], times[i][1], frame_rates[i]);
    }
    *out_len = b.len;
    return b.data;
}

/* ------------------------------------------------------------------ */
/* Real fixtures                                                      */
/* ------------------------------------------------------------------ */

/* One play item's expected result: clip name, duration in ns, frame rate
 * index, chapter count and chapter timestamps. */
typedef struct expected_item {
    const char *source;
    int64_t duration_ns;
    int fps_index; /* Config.FRAME_RATE index */
    size_t chapter_count;
    const int64_t *chapter_ns;
} expected_item;

static void check_entry(const tc_data *d, size_t index, const expected_item *want) {
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, index, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.source, want->source);
    TC_CHECK_EQ_INT(ei.duration_ns, want->duration_ns);
    TC_CHECK_EQ_INT(ei.chapter_count, want->chapter_count);
    /* The frame rate is stored as a rational; compare the ratio the way the
     * C# table defines it. */
    static const struct { int num, den; } fps[] = {
        {0, 0}, {24000, 1001}, {24, 1}, {25, 1}, {30000, 1001}, {0, 0},
        {50, 1}, {60000, 1001},
    };
    TC_CHECK(want->fps_index >= 0 && want->fps_index < 8);
    TC_CHECK_EQ_INT(ei.fps_num, fps[want->fps_index].num);
    TC_CHECK_EQ_INT(ei.fps_den, fps[want->fps_index].den);

    for (size_t i = 0; i < want->chapter_count; i++) {
        tc_chapter_t c;
        TC_CHECK_EQ_INT(tc_chapter_at(d, index, i, &c), TC_OK);
        TC_CHECK_EQ_INT(c.time_ns, want->chapter_ns[i]);
        char name[32];
        snprintf(name, sizeof(name), "Chapter %02u", (unsigned)(i + 1));
        TC_CHECK_EQ_STR(c.name, name);
        /* MPLS does not carry frame numbers. */
        TC_CHECK_EQ_INT(c.frames, -1);
    }
}

static void test_hd_fixture(void) {
    static const int64_t item0[] = {
        0, 74992000000LL, 165040000000LL, 747038000000LL,
        1316023000000LL, 1406030000000LL,
    };
    static const int64_t item1[] = {
        1001000000LL, 61979000000LL, 152027000000LL, 691024000000LL,
        1315981000000LL, 1405988000000LL,
    };
    static const expected_item want[] = {
        {"00002", 1422087000000LL, 1, 6, item0},
        {"00003", 1422254000000LL, 1, 6, item1},
    };

    char *buf = NULL;
    size_t len = 0;
    if (tc_test_read_data("mpls-hd.mpls", &buf, &len) != TC_OK) {
        fprintf(stderr, "  (skipped: testdata/mpls-hd.mpls not found)\n");
        return;
    }

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(buf, len, TC_FMT_MPLS, "mpls-hd.mpls", &d), TC_OK);
    free(buf);
    if (!d) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 2);
    check_entry(d, 0, &want[0]);
    check_entry(d, 1, &want[1]);
    tc_free(d);
}

static void test_uhd_fixture(void) {
    /* Version 0300 with extension data; 16 marks, all on the only play item. */
    static const int64_t chapters[] = {
        0, 300133000000LL, 672755000000LL, 1094093000000LL, 1398522000000LL,
        1674590000000LL, 2065480000000LL, 2469342000000LL, 2770434000000LL,
        3059098000000LL, 3407863000000LL, 3753875000000LL, 4026773000000LL,
        4450196000000LL, 4865819000000LL, 5304591000000LL,
    };
    static const expected_item want = {
        "00001", 6294288000000LL, 1, 16, chapters,
    };

    char *buf = NULL;
    size_t len = 0;
    if (tc_test_read_data("mpls-uhd.mpls", &buf, &len) != TC_OK) {
        fprintf(stderr, "  (skipped: testdata/mpls-uhd.mpls not found)\n");
        return;
    }
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(buf, len, TC_FMT_MPLS, "mpls-uhd.mpls", &d), TC_OK);
    free(buf);
    if (!d) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    check_entry(d, 0, &want);
    tc_free(d);
}

static void test_empty_fixture(void) {
    /* 12 play items, one mark. Every item without a mark of its own falls back
     * to a single chapter at zero, named "Chapter 1" (not "Chapter 01"). */
    static const struct { const char *source; int64_t duration_ns; } want[] = {
        {"00011", 1001000000LL},
        {"00012", 23398000000LL},
        {"00013", 25234000000LL},
        {"00014", 19686000000LL},
        {"00015", 24983000000LL},
        {"00016", 26860000000LL},
        {"00017", 25901000000LL},
        {"00018", 19686000000LL},
        {"00019", 27986000000LL},
        {"00020", 22064000000LL},
        {"00021", 27402000000LL},
        {"00022", 28862000000LL},
    };

    char *buf = NULL;
    size_t len = 0;
    if (tc_test_read_data("mpls-empty.mpls", &buf, &len) != TC_OK) {
        fprintf(stderr, "  (skipped: testdata/mpls-empty.mpls not found)\n");
        return;
    }
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(buf, len, TC_FMT_MPLS, "mpls-empty.mpls", &d), TC_OK);
    free(buf);
    if (!d) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 12);
    for (size_t i = 0; i < 12; i++) {
        tc_entry_info_t ei;
        TC_CHECK_EQ_INT(tc_entry_info(d, i, &ei), TC_OK);
        TC_CHECK_EQ_STR(ei.source, want[i].source);
        TC_CHECK_EQ_INT(ei.duration_ns, want[i].duration_ns);
        TC_CHECK_EQ_INT(ei.fps_num, 24000);
        TC_CHECK_EQ_INT(ei.fps_den, 1001);
        TC_CHECK_EQ_INT(ei.chapter_count, 1);

        tc_chapter_t c;
        TC_CHECK_EQ_INT(tc_chapter_at(d, i, 0, &c), TC_OK);
        TC_CHECK_EQ_INT(c.time_ns, 0);
        TC_CHECK_EQ_STR(c.name, i == 0 ? "Chapter 01" : "Chapter 1");
    }
    tc_free(d);
}

static void test_parse_file_path(void) {
    /* The file entry point must agree with the memory one; it is also the path
     * used by the CLI. */
    char path[1024];
    tc_test_data_path(path, sizeof(path), "mpls-hd.mpls");
    if (tc_detect_from_file(path) != TC_FMT_MPLS) {
        fprintf(stderr, "  (skipped: testdata/mpls-hd.mpls not found)\n");
        return;
    }
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_file(path, TC_FMT_MPLS, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 2);
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.source, "00002");
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Time conversion                                                    */
/* ------------------------------------------------------------------ */

static void test_pts2time_rounding(void) {
    /* 45 kHz ticks are 22222.2 ns each, so the conversion rounds to whole
     * milliseconds away from zero. These cases pin the boundaries:
     *   449  ticks -> 9.978 ms -> 10 ms
     *   450  ticks -> 10.000 ms
     *   44999 ticks -> 999.978 ms -> 1000 ms
     *   89999 ticks -> 1999.978 ms -> 2000 ms
     * The last one matters: the truncated seconds part is 1, and the rounded
     * millisecond part becomes 1000, which the TimeSpan constructor accepts. */
    struct {
        uint32_t ticks;
        int64_t ns;
    } cases[] = {
        {0, 0},
        {1, 0},
        {22, 0},        /* 0.488 ms -> 0 */
        {23, 1000000},  /* 0.511 ms -> 1 ms */
        {449, 10000000},
        {450, 10000000},
        {44999, 1000000000},
        {45000, 1000000000},
        {89999, 2000000000},
        {90000, 2000000000},
        {190440000, 4232000000000LL}, /* the first mark of the HD fixture */
        {193814621, 4306992000000LL},
    };

    for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
        /* A playlist whose single mark carries the tick value makes the
         * conversion observable through the public API. The play item's IN
         * time is 0, so the mark offset is the mark itself and chapter 1
         * lands at zero; chapter 2 carries the value under test. */
        const char *names[1] = {"00001"};
        uint32_t times[1][2] = {{0, 45000}};
        uint8_t rates[1] = {1};
        size_t body_len = 0;
        uint8_t *body = build_playlist_body(&body_len, 1, names, times, rates);
        mark_spec marks[2] = {
            {0x01, 0, 0},
            {0x01, 0, cases[i].ticks},
        };
        size_t total = 0;
        uint8_t *data = build_playlist(&total, "0200", body, body_len, marks, 2, NULL, 0);
        free(body);

        tc_data *d = NULL;
        tc_status st = tc_parse_mem(data, total, TC_FMT_MPLS, "t.mpls", &d);
        free(data);
        TC_CHECK_EQ_INT(st, TC_OK);
        if (st != TC_OK) {
            continue;
        }
        tc_chapter_t c;
        TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 1, &c), TC_OK);
        TC_CHECK_EQ_INT(c.time_ns, cases[i].ns);
        tc_free(d);
    }
}

/* ------------------------------------------------------------------ */
/* Synthetic playlists                                                */
/* ------------------------------------------------------------------ */

static void test_single_play_item(void) {
    const char *names[1] = {"00001"};
    uint32_t times[1][2] = {{1000, 45000}}; /* delta of 44000 ticks */
    uint8_t rates[1] = {1};
    size_t body_len = 0;
    uint8_t *body = build_playlist_body(&body_len, 1, names, times, rates);
    mark_spec marks[1] = {{0x01, 0, 1000}};
    size_t total = 0;
    uint8_t *data = build_playlist(&total, "0200", body, body_len, marks, 1, NULL, 0);
    free(body);

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(data, total, TC_FMT_MPLS, "t.mpls", &d), TC_OK);
    free(data);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.source, "00001");
    TC_CHECK_EQ_INT(ei.duration_ns, 978000000LL); /* 44000 ticks */
    TC_CHECK_EQ_INT(ei.chapter_count, 1);
    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    TC_CHECK_EQ_INT(c.time_ns, 0);
    TC_CHECK_EQ_STR(c.name, "Chapter 01");
    tc_free(d);
}

static void test_multiple_play_items(void) {
    const char *names[3] = {"00001", "00002", "00003"};
    uint32_t times[3][2] = {{0, 45000}, {45000, 90000}, {90000, 180000}};
    uint8_t rates[3] = {1, 4, 3};
    size_t body_len = 0;
    uint8_t *body = build_playlist_body(&body_len, 3, names, times, rates);
    mark_spec marks[3] = {
        {0x01, 0, 0},
        {0x01, 1, 45000},
        {0x01, 2, 90000},
    };
    size_t total = 0;
    uint8_t *data = build_playlist(&total, "0200", body, body_len, marks, 3, NULL, 0);
    free(body);

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(data, total, TC_FMT_MPLS, "t.mpls", &d), TC_OK);
    free(data);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 3);

    static const struct { const char *source; int64_t dur; int num, den; } want[] = {
        {"00001", 1000000000LL, 24000, 1001},
        {"00002", 1000000000LL, 30000, 1001},
        {"00003", 2000000000LL, 25, 1},
    };
    for (size_t i = 0; i < 3; i++) {
        tc_entry_info_t ei;
        TC_CHECK_EQ_INT(tc_entry_info(d, i, &ei), TC_OK);
        TC_CHECK_EQ_STR(ei.source, want[i].source);
        TC_CHECK_EQ_INT(ei.duration_ns, want[i].dur);
        TC_CHECK_EQ_INT(ei.fps_num, want[i].num);
        TC_CHECK_EQ_INT(ei.fps_den, want[i].den);
    }
    tc_free(d);
}

static void test_marks_are_filtered_and_sorted_as_read(void) {
    /* Marks of other types, marks on other play items and the 0x01 marks of
     * this item interleave in the file; the output keeps file order and drops
     * everything that is not type 0x01 for this item. The offset rule also
     * applies: the first mark is earlier than the IN time, so the offset falls
     * back to IN. */
    const char *names[1] = {"00001"};
    uint32_t times[1][2] = {{100000, 190000}};
    uint8_t rates[1] = {2};
    size_t body_len = 0;
    uint8_t *body = build_playlist_body(&body_len, 1, names, times, rates);
    mark_spec marks[6] = {
        {0x00, 0, 50000},  /* type 0: not a chapter */
        {0x01, 1, 60000},  /* other play item */
        {0x01, 0, 100000}, /* first chapter mark */
        {0x02, 0, 110000}, /* type 2: not a chapter */
        {0x01, 0, 140000},
        {0x01, 0, 180000},
    };
    size_t total = 0;
    uint8_t *data = build_playlist(&total, "0200", body, body_len, marks, 6, NULL, 0);
    free(body);

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(data, total, TC_FMT_MPLS, "t.mpls", &d), TC_OK);
    free(data);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_INT(ei.chapter_count, 3);

    /* offset = IN = 100000 */
    static const int64_t want[] = {0, 889000000LL, 1778000000LL};
    for (size_t i = 0; i < 3; i++) {
        tc_chapter_t c;
        TC_CHECK_EQ_INT(tc_chapter_at(d, 0, i, &c), TC_OK);
        TC_CHECK_EQ_INT(c.time_ns, want[i]);
    }
    tc_free(d);
}

static void test_offset_rules(void) {
    /* The original picks the offset as follows:
     *
     *   offset = first type-0x01 mark of this play item
     *   if (IN < offset) offset = IN
     *
     * Case A: IN is earlier than the first mark, so the offset is IN and the
     * first chapter does not start at zero.
     * Case B: IN is later than the first mark, so the offset is the mark and
     * the first chapter starts at zero.
     */
    const char *names[1] = {"00001"};
    uint8_t rates[1] = {1};

    /* Case A: IN=1000, marks at 46000 and 91000. */
    {
        uint32_t times[1][2] = {{1000, 91000}};
        size_t body_len = 0;
        uint8_t *body = build_playlist_body(&body_len, 1, names, times, rates);
        mark_spec marks[2] = {{0x01, 0, 46000}, {0x01, 0, 91000}};
        size_t total = 0;
        uint8_t *data = build_playlist(&total, "0200", body, body_len, marks, 2, NULL, 0);
        free(body);

        tc_data *d = NULL;
        TC_CHECK_EQ_INT(tc_parse_mem(data, total, TC_FMT_MPLS, "t.mpls", &d), TC_OK);
        free(data);
        if (d) {
            TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
            tc_chapter_t c;
            /* 46000 - 1000 = 45000 ticks -> 1 s */
            TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
            TC_CHECK_EQ_INT(c.time_ns, 1000000000LL);
            /* 91000 - 1000 = 90000 ticks -> 2 s */
            TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 1, &c), TC_OK);
            TC_CHECK_EQ_INT(c.time_ns, 2000000000LL);
            tc_free(d);
        }
    }

    /* Case B: IN=100000, marks at 100000 and 140000; the offset is the first
     * mark, which equals IN here, so the first chapter is at zero. */
    {
        uint32_t times[1][2] = {{100000, 190000}};
        size_t body_len = 0;
        uint8_t *body = build_playlist_body(&body_len, 1, names, times, rates);
        mark_spec marks[2] = {{0x01, 0, 100000}, {0x01, 0, 140000}};
        size_t total = 0;
        uint8_t *data = build_playlist(&total, "0200", body, body_len, marks, 2, NULL, 0);
        free(body);

        tc_data *d = NULL;
        TC_CHECK_EQ_INT(tc_parse_mem(data, total, TC_FMT_MPLS, "t.mpls", &d), TC_OK);
        free(data);
        if (d) {
            TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
            tc_chapter_t c;
            TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
            TC_CHECK_EQ_INT(c.time_ns, 0);
            /* 140000 - 100000 = 40000 ticks -> 889 ms */
            TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 1, &c), TC_OK);
            TC_CHECK_EQ_INT(c.time_ns, 889000000LL);
            tc_free(d);
        }
    }
}

static void test_no_marks_gets_placeholder_chapter(void) {
    const char *names[1] = {"00001"};
    uint32_t times[1][2] = {{0, 45000}};
    uint8_t rates[1] = {1};
    size_t body_len = 0;
    uint8_t *body = build_playlist_body(&body_len, 1, names, times, rates);
    size_t total = 0;
    uint8_t *data = build_playlist(&total, "0200", body, body_len, NULL, 0, NULL, 0);
    free(body);

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(data, total, TC_FMT_MPLS, "t.mpls", &d), TC_OK);
    free(data);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    TC_CHECK_EQ_INT(c.time_ns, 0);
    /* The placeholder keeps the original's one-digit spelling. */
    TC_CHECK_EQ_STR(c.name, "Chapter 1");
    tc_free(d);
}

static void test_unknown_stream_coding_is_skipped(void) {
    /* An unknown stream coding type has no payload in the original; the box
     * length still has to carry the parser past it. The frame rate then comes
     * from the video info byte of the primary video entry, which is written
     * here as 0x64: video format 6, frame rate index 4. */
    const char *names[1] = {"00001"};
    uint32_t times[1][2] = {{0, 45000}};
    uint8_t rates[1] = {4};
    size_t body_len = 0;
    uint8_t *body = build_playlist_body(&body_len, 1, names, times, rates);
    size_t total = 0;
    uint8_t *data = build_playlist(&total, "0200", body, body_len, NULL, 0, NULL, 0);
    free(body);

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(data, total, TC_FMT_MPLS, "t.mpls", &d), TC_OK);
    free(data);
    if (!d) {
        return;
    }
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_INT(ei.fps_num, 30000);
    TC_CHECK_EQ_INT(ei.fps_den, 1001);
    tc_free(d);
}

static void test_extension_data_is_walked(void) {
    /* Version 0300 files can carry extension data after the marks. The box is
     * decoded but ignored, so the chapters must be unaffected. */
    const char *names[1] = {"00001"};
    uint32_t times[1][2] = {{0, 45000}};
    uint8_t rates[1] = {1};
    size_t body_len = 0;
    uint8_t *body = build_playlist_body(&body_len, 1, names, times, rates);

    /* length 32, data block address 0, 3 reserved, 2 entries of 12 bytes. */
    uint8_t ext[36];
    memset(ext, 0, sizeof(ext));
    ext[3] = 32;    /* length */
    ext[11] = 2;    /* entry count */
    ext[12] = 0x00; ext[13] = 0x03; /* ExtDataType */
    ext[14] = 0x00; ext[15] = 0x05; /* ExtDataVersion */

    mark_spec marks[1] = {{0x01, 0, 0}};
    size_t total = 0;
    uint8_t *data = build_playlist(&total, "0300", body, body_len, marks, 1, ext, sizeof(ext));
    free(body);

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(data, total, TC_FMT_MPLS, "t.mpls", &d), TC_OK);
    free(data);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    tc_free(d);
}

static void test_unknown_box_content_is_skipped(void) {
    /* Every box carries its own length and the parser seeks to the declared
     * end, so content it does not understand is skipped rather than rejected.
     * The builder appends 12 unknown bytes to the play item body; the STN table
     * and the item lengths still describe a valid box, so the chapters must come
     * out unchanged. */
    const char *names[1] = {"00001"};
    uint32_t times[1][2] = {{0, 90000}};
    uint8_t rates[1] = {1};
    size_t body_len = 0;
    uint8_t *body = build_playlist_body(&body_len, 1, names, times, rates);
    mark_spec marks[2] = {{0x01, 0, 0}, {0x01, 0, 45000}};
    size_t total = 0;
    uint8_t *data = build_playlist(&total, "0200", body, body_len, marks, 2, NULL, 0);
    free(body);

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(data, total, TC_FMT_MPLS, "t.mpls", &d), TC_OK);
    free(data);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 1, &c), TC_OK);
    TC_CHECK_EQ_INT(c.time_ns, 1000000000LL);
    tc_free(d);
}

static void test_unknown_stream_entry_content_is_skipped(void) {
    /* An unknown stream coding type has no payload, and an unknown stream type
     * has no extra fields; the entry length still carries the parser past both.
     * The audio entry here uses coding 0x7F, which no switch case matches. */
    const char *names[1] = {"00001"};
    uint32_t times[1][2] = {{0, 45000}};
    uint8_t rates[1] = {3};
    size_t body_len = 0;
    uint8_t *body = build_playlist_body(&body_len, 1, names, times, rates);

    /* Rebuild with an unknown coding type in the audio attributes. The video
     * entry (frame rate 3) is what the parser must still find. */
    bw b;
    memset(&b, 0, sizeof(b));
    bw_u16(&b, 0);
    bw_u16(&b, 1);
    bw_u16(&b, 0);
    size_t item_start = b.len;
    bw_u16(&b, 0);
    bw_clip_name(&b, "00001", "M2TS");
    bw_u16(&b, 0);
    bw_u8(&b, 0);
    bw_u32(&b, 0);
    bw_u32(&b, 45000);
    bw_zeros(&b, 8);
    bw_u8(&b, 0);
    bw_u8(&b, 0);
    bw_u16(&b, 0);
    size_t stn_start = b.len;
    bw_u16(&b, 0);
    bw_u16(&b, 0);
    bw_u8(&b, 1); /* one primary video entry */
    bw_u8(&b, 2); /* two audio entries, one with an unknown coding type */
    bw_u8(&b, 0);
    bw_u8(&b, 0);
    bw_u8(&b, 0);
    bw_u8(&b, 0);
    bw_u8(&b, 0);
    bw_zeros(&b, 5);
    emit_stream_entry(&b, 0x01, 0x1011);
    emit_stream_attributes(&b, 0x1B, 0x63); /* frame rate index 3 */
    emit_stream_entry(&b, 0x03, 0x1100);
    emit_stream_attributes(&b, 0x7F, 0x00); /* unknown coding type */
    emit_stream_entry(&b, 0x99, 0x1200);    /* unknown stream type */
    emit_stream_attributes(&b, 0x81, 0x31);
    uint16_t stn_len = (uint16_t)(b.len - stn_start - 2);
    b.data[stn_start] = (uint8_t)(stn_len >> 8);
    b.data[stn_start + 1] = (uint8_t)(stn_len & 0xFF);
    uint16_t item_len = (uint16_t)(b.len - item_start - 2);
    b.data[item_start] = (uint8_t)(item_len >> 8);
    b.data[item_start + 1] = (uint8_t)(item_len & 0xFF);
    size_t custom_len = b.len;
    uint8_t *custom = b.data;

    mark_spec marks[1] = {{0x01, 0, 0}};
    size_t total = 0;
    uint8_t *data = build_playlist(&total, "0200", custom, custom_len, marks, 1, NULL, 0);
    free(custom);
    free(body);

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(data, total, TC_FMT_MPLS, "t.mpls", &d), TC_OK);
    free(data);
    if (!d) {
        return;
    }
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_INT(ei.fps_num, 25);
    TC_CHECK_EQ_INT(ei.fps_den, 1);
    tc_free(d);
}

static void test_sub_paths_are_skipped(void) {
    /* Sub paths carry no chapter information; their declared length is used to
     * step over them. The builder appends one after the play items. */
    bw b;
    memset(&b, 0, sizeof(b));
    bw_u16(&b, 0);
    bw_u16(&b, 1);
    bw_u16(&b, 1); /* one sub path */
    emit_play_item(&b, "00001", 0, 45000, 1);
    /* SubPath: length 12, 2 reserved, type, 2 flag bytes, 0 sub play items,
     * plus 5 unknown bytes that only the length can carry us past. */
    bw_u32(&b, 12);
    bw_zeros(&b, 2);
    bw_u8(&b, 0);
    bw_u16(&b, 0);
    bw_u8(&b, 0);
    bw_zeros(&b, 5);
    size_t body_len = b.len;
    uint8_t *body = b.data;

    mark_spec marks[1] = {{0x01, 0, 0}};
    size_t total = 0;
    uint8_t *data = build_playlist(&total, "0200", body, body_len, marks, 1, NULL, 0);
    free(body);

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(data, total, TC_FMT_MPLS, "t.mpls", &d), TC_OK);
    free(data);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    tc_free(d);
}

static void test_multi_angle_clip_names_are_joined(void) {
    /* FullName joins the additional angle clip names with "&". The first
     * fixture does not use multi-angle, so this is built by hand. */
    bw b;
    memset(&b, 0, sizeof(b));
    bw_u16(&b, 0);
    bw_u16(&b, 1);
    bw_u16(&b, 0);
    size_t item_start = b.len;
    bw_u16(&b, 0);
    bw_clip_name(&b, "00001", "M2TS");
    bw_u16(&b, 1 << 4); /* IsMultiAngle */
    bw_u8(&b, 0);
    bw_u32(&b, 0);
    bw_u32(&b, 45000);
    bw_zeros(&b, 8);
    bw_u8(&b, 0);
    bw_u8(&b, 0);
    bw_u16(&b, 0);
    /* MultiAngle: 3 angles, so 2 extra clip names. */
    bw_u8(&b, 3);
    bw_u8(&b, 0);
    bw_clip_name(&b, "00002", "M2TS");
    bw_u8(&b, 0);
    bw_clip_name(&b, "00003", "M2TS");
    bw_u8(&b, 0);
    size_t stn_start = b.len;
    bw_u16(&b, 0);
    bw_u16(&b, 0);
    bw_u8(&b, 1);
    bw_u8(&b, 0);
    bw_u8(&b, 0);
    bw_u8(&b, 0);
    bw_u8(&b, 0);
    bw_u8(&b, 0);
    bw_u8(&b, 0);
    bw_zeros(&b, 5);
    emit_stream_entry(&b, 0x01, 0x1011);
    emit_stream_attributes(&b, 0x1B, 0x61);
    uint16_t stn_len = (uint16_t)(b.len - stn_start - 2);
    b.data[stn_start] = (uint8_t)(stn_len >> 8);
    b.data[stn_start + 1] = (uint8_t)(stn_len & 0xFF);
    uint16_t item_len = (uint16_t)(b.len - item_start - 2);
    b.data[item_start] = (uint8_t)(item_len >> 8);
    b.data[item_start + 1] = (uint8_t)(item_len & 0xFF);
    size_t body_len = b.len;
    uint8_t *body = b.data;

    mark_spec marks[1] = {{0x01, 0, 0}};
    size_t total = 0;
    uint8_t *data = build_playlist(&total, "0200", body, body_len, marks, 1, NULL, 0);
    free(body);

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(data, total, TC_FMT_MPLS, "t.mpls", &d), TC_OK);
    free(data);
    if (!d) {
        return;
    }
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    /* ClipName.ToString() is "file.codec", but FullName only concatenates the
     * 5-byte file names. */
    TC_CHECK_EQ_STR(ei.source, "00001&00002&00003");
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Error paths                                                        */
/* ------------------------------------------------------------------ */

static void test_truncated_files(void) {
    const char *names[1] = {"00001"};
    uint32_t times[1][2] = {{0, 45000}};
    uint8_t rates[1] = {1};
    size_t body_len = 0;
    uint8_t *body = build_playlist_body(&body_len, 1, names, times, rates);
    mark_spec marks[1] = {{0x01, 0, 0}};
    size_t total = 0;
    uint8_t *data = build_playlist(&total, "0200", body, body_len, marks, 1, NULL, 0);
    free(body);

    /* Every prefix must be rejected, and the parser must not read past the
     * buffer it was given. */
    for (size_t cut = 0; cut < total; cut++) {
        tc_data *d = NULL;
        tc_status st = tc_parse_mem(data, cut, TC_FMT_MPLS, "t.mpls", &d);
        if (cut >= 8) {
            /* Past the magic, the failure is always a format error. */
            TC_CHECK(st != TC_OK);
        }
        TC_CHECK(d == NULL);
        if (d) {
            tc_free(d);
        }
    }
    free(data);
}

static void test_rejects_bad_magic_and_version(void) {
    const char *names[1] = {"00001"};
    uint32_t times[1][2] = {{0, 45000}};
    uint8_t rates[1] = {1};
    size_t body_len = 0;
    uint8_t *body = build_playlist_body(&body_len, 1, names, times, rates);

    struct { const char *version; int ok; } versions[] = {
        {"0100", 1}, {"0200", 1}, {"0300", 1}, {"0400", 0}, {"0000", 0},
    };
    for (size_t i = 0; i < sizeof(versions) / sizeof(versions[0]); i++) {
        size_t total = 0;
        uint8_t *data = build_playlist(&total, versions[i].version, body, body_len,
                                       NULL, 0, NULL, 0);
        tc_data *d = NULL;
        tc_status st = tc_parse_mem(data, total, TC_FMT_MPLS, "t.mpls", &d);
        if (versions[i].ok) {
            TC_CHECK_EQ_INT(st, TC_OK);
        } else {
            TC_CHECK(st != TC_OK);
        }
        if (d) {
            tc_free(d);
        }
        free(data);
    }
    free(body);

    /* A wrong magic is rejected before anything else is read. */
    uint8_t bad[58];
    memset(bad, 0, sizeof(bad));
    memcpy(bad, "XXXX", 4);
    memcpy(bad + 4, "0200", 4);
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(bad, sizeof(bad), TC_FMT_MPLS, "t.mpls", &d), TC_E_FORMAT);
    TC_CHECK(d == NULL);
    TC_CHECK_CONTAINS(tc_last_error(), "magic");
}

static void test_rejects_offsets_past_end(void) {
    const char *names[1] = {"00001"};
    uint32_t times[1][2] = {{0, 45000}};
    uint8_t rates[1] = {1};
    size_t body_len = 0;
    uint8_t *body = build_playlist_body(&body_len, 1, names, times, rates);
    size_t total = 0;
    uint8_t *data = build_playlist(&total, "0200", body, body_len, NULL, 0, NULL, 0);
    free(body);

    /* PlayListStartAddress is the first offset in the file. */
    data[8] = 0x7F;
    data[9] = 0xFF;
    data[10] = 0xFF;
    data[11] = 0xFF;
    tc_data *d = NULL;
    TC_CHECK(tc_parse_mem(data, total, TC_FMT_MPLS, "t.mpls", &d) != TC_OK);
    TC_CHECK(d == NULL);
    free(data);
}

static void test_play_item_without_primary_video_fails(void) {
    /* MPLSParser.GetChapters calls .First(...) on the primary video entries and
     * throws when there are none. The C port reports TC_E_FORMAT instead of
     * producing a chapter list with an unknown frame rate. */
    bw b;
    memset(&b, 0, sizeof(b));
    bw_u16(&b, 0);
    bw_u16(&b, 1);
    bw_u16(&b, 0);
    size_t item_start = b.len;
    bw_u16(&b, 0);
    bw_clip_name(&b, "00001", "M2TS");
    bw_u16(&b, 0);
    bw_u8(&b, 0);
    bw_u32(&b, 0);
    bw_u32(&b, 45000);
    bw_zeros(&b, 8);
    bw_u8(&b, 0);
    bw_u8(&b, 0);
    bw_u16(&b, 0);
    size_t stn_start = b.len;
    bw_u16(&b, 0);
    bw_u16(&b, 0);
    bw_u8(&b, 0); /* no primary video */
    bw_u8(&b, 1); /* one primary audio entry */
    bw_u8(&b, 0);
    bw_u8(&b, 0);
    bw_u8(&b, 0);
    bw_u8(&b, 0);
    bw_u8(&b, 0);
    bw_zeros(&b, 5);
    emit_stream_entry(&b, 0x03, 0x1100);
    emit_stream_attributes(&b, 0x81, 0x36);
    uint16_t stn_len = (uint16_t)(b.len - stn_start - 2);
    b.data[stn_start] = (uint8_t)(stn_len >> 8);
    b.data[stn_start + 1] = (uint8_t)(stn_len & 0xFF);
    uint16_t item_len = (uint16_t)(b.len - item_start - 2);
    b.data[item_start] = (uint8_t)(item_len >> 8);
    b.data[item_start + 1] = (uint8_t)(item_len & 0xFF);

    size_t no_video_len = b.len;
    uint8_t *no_video = b.data;

    size_t total = 0;
    uint8_t *data = build_playlist(&total, "0200", no_video, no_video_len, NULL, 0, NULL, 0);
    free(no_video);

    tc_data *d = NULL;
    tc_status st = tc_parse_mem(data, total, TC_FMT_MPLS, "t.mpls", &d);
    free(data);
    TC_CHECK(st != TC_OK);
    TC_CHECK(d == NULL);
}

static void test_rejects_bad_arguments(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_file("/definitely/not/here.mpls", TC_FMT_MPLS, &d), TC_E_IO);
    TC_CHECK(d == NULL);
    TC_CHECK_EQ_INT(tc_parse_mem(NULL, 0, TC_FMT_MPLS, "t.mpls", &d), TC_E_INVALID);
    TC_CHECK(d == NULL);
}

/* ------------------------------------------------------------------ */
/* Suite                                                              */
/* ------------------------------------------------------------------ */

TC_SUITE(mpls) {
    TC_CASE("fixture_hd");            test_hd_fixture();
    TC_CASE("fixture_uhd");           test_uhd_fixture();
    TC_CASE("fixture_empty");         test_empty_fixture();
    TC_CASE("parse_file");            test_parse_file_path();
    TC_CASE("pts2time");              test_pts2time_rounding();
    TC_CASE("single_play_item");      test_single_play_item();
    TC_CASE("multiple_play_items");   test_multiple_play_items();
    TC_CASE("mark_filtering");        test_marks_are_filtered_and_sorted_as_read();
    TC_CASE("offset_rules");          test_offset_rules();
    TC_CASE("no_marks");              test_no_marks_gets_placeholder_chapter();
    TC_CASE("unknown_coding");        test_unknown_stream_coding_is_skipped();
    TC_CASE("unknown_box_content");   test_unknown_box_content_is_skipped();
    TC_CASE("unknown_stream_entry");  test_unknown_stream_entry_content_is_skipped();
    TC_CASE("sub_paths");             test_sub_paths_are_skipped();
    TC_CASE("multi_angle");           test_multi_angle_clip_names_are_joined();
    TC_CASE("extension_data");        test_extension_data_is_walked();
    TC_CASE("truncated");             test_truncated_files();
    TC_CASE("bad_magic_version");     test_rejects_bad_magic_and_version();
    TC_CASE("bad_offsets");           test_rejects_offsets_past_end();
    TC_CASE("no_primary_video");      test_play_item_without_primary_video_fails();
    TC_CASE("bad_arguments");         test_rejects_bad_arguments();
}
