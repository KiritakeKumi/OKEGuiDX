/*
 * MP4 parser tests (B8).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * The real fixture mp4-nero.mp4 is the reference implementation's own test
 * asset (TChapter.Test/Assets/MP4/nero.mp4), copied verbatim. It carries a
 * Nero chapter list (moov/udta/chpl, version 1) with four chapters at 0, 10,
 * 20 and 30 seconds, and an mvhd duration of 29.15 s. MP4Parser.ReadFromFile
 * rebuilds chapter start times by accumulating durations, so the expected times
 * are exactly those four values.
 *
 * The QuickTime chapter path has no fixture in the reference repository (the
 * original test only covers Nero), so its inputs are built here. They follow
 * the layout mp4v2 writes and reads: a text track referenced by a video track
 * through tref/chap, whose samples are a 16-bit big-endian length followed by
 * the title.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_test.h"

/* ------------------------------------------------------------------ */
/* Synthetic MP4 writer                                               */
/* ------------------------------------------------------------------ */

static void put_be32(tc_buf *b, uint32_t v) {
    tc_buf_putc(b, (char)((v >> 24) & 0xffu));
    tc_buf_putc(b, (char)((v >> 16) & 0xffu));
    tc_buf_putc(b, (char)((v >> 8) & 0xffu));
    tc_buf_putc(b, (char)(v & 0xffu));
}

static void put_be64(tc_buf *b, uint64_t v) {
    put_be32(b, (uint32_t)(v >> 32));
    put_be32(b, (uint32_t)(v & 0xffffffffu));
}

static uint32_t get_be32(const char *p) {
    const uint8_t *u = (const uint8_t *)p;
    return ((uint32_t)u[0] << 24) | ((uint32_t)u[1] << 16) |
           ((uint32_t)u[2] << 8) | (uint32_t)u[3];
}

static void patch_be32(char *p, uint32_t v) {
    p[0] = (char)((v >> 24) & 0xffu);
    p[1] = (char)((v >> 16) & 0xffu);
    p[2] = (char)((v >> 8) & 0xffu);
    p[3] = (char)(v & 0xffu);
}

/* Opens a box and returns the offset of its size field, for box_close. */
static size_t box_open(tc_buf *b, const char *type) {
    size_t at = b->len;
    put_be32(b, 0);
    tc_buf_write(b, type, 4);
    return at;
}

static void box_close(tc_buf *b, size_t at) {
    patch_be32(b->data + at, (uint32_t)(b->len - at));
}

/* A full box header: version 0 and no flags. */
static void put_full_box(tc_buf *b) {
    put_be32(b, 0);
}

/* Version 1 full-box header: the Nero chapter list uses it. */
static void put_full_box_v1(tc_buf *b) {
    put_be32(b, 0x01000000);
}

static void put_ftyp(tc_buf *b) {
    size_t at = box_open(b, "ftyp");
    tc_buf_write(b, "isom", 4);
    put_be32(b, 512);
    tc_buf_write(b, "isomiso2mp41", 12);
    box_close(b, at);
}

/* mvhd version 0, 100-byte payload. */
static void put_mvhd(tc_buf *b, uint32_t timescale, uint64_t duration) {
    size_t at = box_open(b, "mvhd");
    put_full_box(b);
    put_be32(b, 0); /* creation */
    put_be32(b, 0); /* modification */
    put_be32(b, timescale);
    put_be32(b, (uint32_t)duration);
    put_be32(b, 0x00010000); /* rate 1.0 */
    tc_buf_putc(b, 1);       /* volume 1.0 */
    tc_buf_putc(b, 0);
    tc_buf_putc(b, 0);
    tc_buf_putc(b, 0);
    for (int i = 0; i < 8; i++) {
        tc_buf_putc(b, 0);
    }
    for (int i = 0; i < 36; i++) {
        tc_buf_putc(b, 0);
    }
    for (int i = 0; i < 24; i++) {
        tc_buf_putc(b, 0);
    }
    put_be32(b, 3); /* next track id */
    box_close(b, at);
}

/* tkhd version 0, 84-byte payload. */
static void put_tkhd(tc_buf *b, uint32_t track_id) {
    size_t at = box_open(b, "tkhd");
    put_full_box(b);
    put_be32(b, 0); /* creation */
    put_be32(b, 0); /* modification */
    put_be32(b, track_id);
    put_be32(b, 0); /* reserved */
    put_be32(b, 0); /* duration */
    for (int i = 0; i < 8; i++) {
        tc_buf_putc(b, 0);
    }
    for (int i = 0; i < 2 + 2 + 2 + 2; i++) {
        tc_buf_putc(b, 0);
    }
    for (int i = 0; i < 36; i++) {
        tc_buf_putc(b, 0);
    }
    put_be32(b, 0); /* width */
    put_be32(b, 0); /* height */
    box_close(b, at);
}

/* mdhd version 0, 24-byte payload. */
static void put_mdhd(tc_buf *b, uint32_t timescale, uint64_t duration) {
    size_t at = box_open(b, "mdhd");
    put_full_box(b);
    put_be32(b, 0); /* creation */
    put_be32(b, 0); /* modification */
    put_be32(b, timescale);
    put_be32(b, (uint32_t)duration);
    put_be32(b, 0); /* language + pre_defined */
    box_close(b, at);
}

/* hdlr with a four-character handler type. */
static void put_hdlr(tc_buf *b, const char *handler) {
    size_t at = box_open(b, "hdlr");
    put_full_box(b);
    put_be32(b, 0); /* pre_defined */
    tc_buf_write(b, handler, 4);
    for (int i = 0; i < 12; i++) {
        tc_buf_putc(b, 0);
    }
    tc_buf_putc(b, 0); /* empty name */
    box_close(b, at);
}

/* A minimal sample description: one 8-byte "text" entry. */
static void put_stsd(tc_buf *b) {
    size_t at = box_open(b, "stsd");
    put_full_box(b);
    put_be32(b, 1);
    size_t entry = box_open(b, "text");
    for (int i = 0; i < 8; i++) {
        tc_buf_putc(b, 0);
    }
    box_close(b, entry);
    box_close(b, at);
}

/* ------------------------------------------------------------------ */
/* Nero chapter list                                                  */
/* ------------------------------------------------------------------ */

typedef struct nero_chapter {
    uint64_t start; /* 100 ns units for version 1 */
    const char *name;
} nero_chapter;

static void put_chpl_v1(tc_buf *b, const nero_chapter *chapters, size_t n) {
    size_t at = box_open(b, "chpl");
    put_full_box_v1(b);
    tc_buf_putc(b, 0); /* reserved byte */
    put_be32(b, (uint32_t)n);
    for (size_t i = 0; i < n; i++) {
        put_be64(b, chapters[i].start);
        size_t len = strlen(chapters[i].name);
        tc_buf_putc(b, (char)(len & 0xffu));
        tc_buf_write(b, chapters[i].name, len);
    }
    box_close(b, at);
}

/* The F4V version 0 layout: 8-bit count, times in movie timescale units. */
static void put_chpl_v0(tc_buf *b, const nero_chapter *chapters, size_t n) {
    size_t at = box_open(b, "chpl");
    put_full_box(b);
    tc_buf_putc(b, (char)n);
    for (size_t i = 0; i < n; i++) {
        put_be64(b, chapters[i].start);
        size_t len = strlen(chapters[i].name);
        tc_buf_putc(b, (char)(len & 0xffu));
        tc_buf_write(b, chapters[i].name, len);
    }
    box_close(b, at);
}
/* Builds a file whose only chapters are a Nero list. */
static void build_nero_file(tc_buf *b, uint32_t timescale, uint64_t duration,
                            const nero_chapter *chapters, size_t n,
                            int chpl_version) {
    tc_buf_init(b);
    put_ftyp(b);
    size_t moov = box_open(b, "moov");
    put_mvhd(b, timescale, duration);
    size_t udta = box_open(b, "udta");
    if (chpl_version == 1) {
        put_chpl_v1(b, chapters, n);
    } else {
        put_chpl_v0(b, chapters, n);
    }
    box_close(b, udta);
    box_close(b, moov);
    size_t mdat = box_open(b, "mdat");
    tc_buf_puts(b, "payload");
    box_close(b, mdat);
}

/* A file with an mvhd but no chapter storage at all. */
static void build_no_chapters_file(tc_buf *b, uint32_t timescale,
                                   uint64_t duration) {
    tc_buf_init(b);
    put_ftyp(b);
    size_t moov = box_open(b, "moov");
    put_mvhd(b, timescale, duration);
    box_close(b, moov);
    size_t mdat = box_open(b, "mdat");
    tc_buf_puts(b, "payload");
    box_close(b, mdat);
}

/* ------------------------------------------------------------------ */
/* QuickTime chapter track                                            */
/* ------------------------------------------------------------------ */

typedef struct qt_sample {
    const char *name;
    uint32_t duration; /* mdhd timescale units */
    uint32_t chunk;    /* 1-based chunk index */
} qt_sample;

/* Writes stts with runs of equal durations merged, as a real writer does. */
static void put_stts(tc_buf *b, const qt_sample *samples, size_t n) {
    size_t at = box_open(b, "stts");
    put_full_box(b);
    size_t entries = 0;
    for (size_t i = 0; i < n;) {
        size_t j = i;
        while (j < n && samples[j].duration == samples[i].duration) {
            j++;
        }
        entries++;
        i = j;
    }
    put_be32(b, (uint32_t)entries);
    for (size_t i = 0; i < n;) {
        size_t j = i;
        while (j < n && samples[j].duration == samples[i].duration) {
            j++;
        }
        put_be32(b, (uint32_t)(j - i));
        put_be32(b, samples[i].duration);
        i = j;
    }
    box_close(b, at);
}

/* Derives stsc from the chunk assignment: consecutive chunks with the same
 * sample count share one entry. Returns the number of chunks. */
static uint32_t put_stsc(tc_buf *b, const qt_sample *samples, size_t n) {
    uint32_t max_chunk = 0;
    for (size_t i = 0; i < n; i++) {
        if (samples[i].chunk > max_chunk) {
            max_chunk = samples[i].chunk;
        }
    }

    size_t at = box_open(b, "stsc");
    put_full_box(b);
    /* Reserve the count; the runs are written after it is known. */
    size_t count_at = b->len;
    put_be32(b, 0);

    uint32_t entries = 0;
    uint32_t chunk = 1;
    while (chunk <= max_chunk) {
        uint32_t per_chunk = 0;
        for (size_t i = 0; i < n; i++) {
            if (samples[i].chunk == chunk) {
                per_chunk++;
            }
        }
        uint32_t run_end = chunk;
        while (run_end < max_chunk) {
            uint32_t next = 0;
            for (size_t i = 0; i < n; i++) {
                if (samples[i].chunk == run_end + 1) {
                    next++;
                }
            }
            if (next != per_chunk) {
                break;
            }
            run_end++;
        }
        put_be32(b, chunk);
        put_be32(b, per_chunk);
        put_be32(b, 1); /* sample description index */
        entries++;
        chunk = run_end + 1;
    }
    patch_be32(b->data + count_at, entries);
    box_close(b, at);
    return max_chunk;
}

static void put_stsz(tc_buf *b, const qt_sample *samples, size_t n) {
    size_t at = box_open(b, "stsz");
    put_full_box(b);
    put_be32(b, 0); /* per-sample sizes */
    put_be32(b, (uint32_t)n);
    for (size_t i = 0; i < n; i++) {
        put_be32(b, (uint32_t)(2 + strlen(samples[i].name)));
    }
    box_close(b, at);
}

/* Writes stco with one entry per chunk and returns the offset of the first
 * entry, so the caller can patch the real file offsets in. */
static size_t put_stco(tc_buf *b, uint32_t chunk_count) {
    size_t at = box_open(b, "stco");
    put_full_box(b);
    put_be32(b, chunk_count);
    size_t first = b->len;
    for (uint32_t i = 0; i < chunk_count; i++) {
        put_be32(b, 0);
    }
    box_close(b, at);
    return first;
}

/* Builds a file whose chapters live in a QuickTime text track. When `nero_n`
 * is non-zero a Nero chapter list is added to the same moov as well, which
 * lets a test check which storage the parser prefers. */
static void build_qt_file(tc_buf *b, uint32_t timescale,
                          const qt_sample *samples, size_t n,
                          const nero_chapter *nero, size_t nero_n) {
    tc_buf_init(b);
    put_ftyp(b);

    uint64_t total = 0;
    for (size_t i = 0; i < n; i++) {
        total += samples[i].duration;
    }

    size_t moov = box_open(b, "moov");
    put_mvhd(b, 1000, total);
    if (nero_n > 0) {
        size_t udta = box_open(b, "udta");
        put_chpl_v1(b, nero, nero_n);
        box_close(b, udta);
    }

    /* Video track 1 references text track 2 through tref/chap. */
    size_t video = box_open(b, "trak");
    put_tkhd(b, 1);
    size_t vmdia = box_open(b, "mdia");
    put_mdhd(b, 90000, total);
    put_hdlr(b, "vide");
    box_close(b, vmdia);
    size_t tref = box_open(b, "tref");
    size_t chap = box_open(b, "chap");
    put_be32(b, 2);
    box_close(b, chap);
    box_close(b, tref);
    box_close(b, video);

    /* Chapter track 2, handler "text". */
    size_t text = box_open(b, "trak");
    put_tkhd(b, 2);
    size_t tmdia = box_open(b, "mdia");
    put_mdhd(b, timescale, total);
    put_hdlr(b, "text");
    size_t minf = box_open(b, "minf");
    size_t stbl = box_open(b, "stbl");
    put_stsd(b);
    put_stts(b, samples, n);
    uint32_t chunk_count = put_stsc(b, samples, n);
    put_stsz(b, samples, n);
    size_t stco_first = put_stco(b, chunk_count);
    box_close(b, stbl);
    box_close(b, minf);
    box_close(b, tmdia);
    box_close(b, text);
    box_close(b, moov);

    /* Sample data, in order; the first sample of each chunk sets its offset. */
    size_t mdat = box_open(b, "mdat");
    for (size_t i = 0; i < n; i++) {
        int first_in_chunk = i == 0 || samples[i].chunk != samples[i - 1].chunk;
        if (first_in_chunk) {
            patch_be32(b->data + stco_first + (size_t)(samples[i].chunk - 1) * 4,
                       (uint32_t)b->len);
        }
        size_t len = strlen(samples[i].name);
        tc_buf_putc(b, (char)((len >> 8) & 0xffu));
        tc_buf_putc(b, (char)(len & 0xffu));
        tc_buf_write(b, samples[i].name, len);
    }
    box_close(b, mdat);
}

/* Returns the offset of the first top-level box of the given type, or 0. */
static size_t find_top_box(const tc_buf *b, const char *type) {
    size_t off = 0;
    while (off + 8 <= b->len) {
        uint32_t size = get_be32(b->data + off);
        if (memcmp(b->data + off + 4, type, 4) == 0) {
            return off;
        }
        if (size < 8) {
            break;
        }
        off += size;
    }
    return 0;
}

/* ------------------------------------------------------------------ */
/* Harness helpers                                                    */
/* ------------------------------------------------------------------ */

static tc_status write_and_parse(const tc_buf *b, tc_data **out) {
    const char *path = "build/mp4_synthetic.mp4";
    FILE *f = fopen(path, "wb");
    if (!f) {
        return TC_E_IO;
    }
    size_t n = 0;
    if (b->len > 0) {
        n = fwrite(b->data, 1, b->len, f);
    }
    fclose(f);
    if (n != b->len) {
        return TC_E_IO;
    }
    return tc_parse_file(path, TC_FMT_MP4, out);
}

static void cleanup(tc_buf *b, tc_data *d) {
    tc_buf_free(b);
    tc_free(d);
}

/* Asserts one chapter's time and name. */
static void check_chapter(const tc_data *d, size_t entry, size_t index,
                          int64_t time_ns, const char *name) {
    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, entry, index, &c), TC_OK);
    TC_CHECK_EQ_INT(c.time_ns, time_ns);
    TC_CHECK_EQ_STR(c.name, name);
    /* MP4 has no frame numbers. */
    TC_CHECK_EQ_INT(c.frames, -1);
}

/* ------------------------------------------------------------------ */
/* Real fixture                                                       */
/* ------------------------------------------------------------------ */

static void test_real_nero_fixture(void) {
    char *path = NULL;
    size_t len = 0;
    if (tc_test_read_data("mp4-nero.mp4", &path, &len) != TC_OK) {
        /* The fixture is optional so that a checkout without it still builds;
         * report loudly rather than silently passing. */
        fprintf(stderr, "  (skipped: testdata/mp4-nero.mp4 not found)\n");
        return;
    }
    free(path);

    tc_data *d = NULL;
    tc_status st = tc_parse_file("testdata/mp4-nero.mp4", TC_FMT_MP4, &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st != TC_OK) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }

    /* chpl version 1, four entries at 0, 10, 20 and 30 s. */
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 4);
    check_chapter(d, 0, 0, 0, "Chapter 01");
    check_chapter(d, 0, 1, 10000000000LL, "Chapter 02");
    check_chapter(d, 0, 2, 20000000000LL, "Chapter 03");
    check_chapter(d, 0, 3, 30000000000LL, "Chapter 04");

    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    /* The durations sum to the movie duration (mvhd: 29150 ms). */
    TC_CHECK_EQ_INT(ei.duration_ns, 29150000000LL);
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Nero chapters                                                      */
/* ------------------------------------------------------------------ */

static void test_nero_v1_synthetic(void) {
    /* 100 ns units: 0, 1.5 s, 4 s. Movie duration 10 s. */
    const nero_chapter chapters[] = {
        {0, "Intro"},
        {15000000ULL, "Part A"},
        {40000000ULL, "Part B"},
    };
    tc_buf b;
    build_nero_file(&b, 1000, 10000, chapters, 3, 1);

    tc_data *d = NULL;
    tc_status st = write_and_parse(&b, &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st != TC_OK) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        cleanup(&b, d);
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 3);
    check_chapter(d, 0, 0, 0, "Intro");
    check_chapter(d, 0, 1, 1500000000LL, "Part A");
    check_chapter(d, 0, 2, 4000000000LL, "Part B");
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    /* Last chapter runs to the movie duration: 4 s + 6 s = 10 s. */
    TC_CHECK_EQ_INT(ei.duration_ns, 10000000000LL);
    cleanup(&b, d);
}

/* The F4V version 0 layout stores start times in movie timescale units. */
static void test_nero_v0_timescale(void) {
    const nero_chapter chapters[] = {
        {0, "One"},
        {1000, "Two"},
        {2500, "Three"},
    };
    tc_buf b;
    build_nero_file(&b, 1000, 5000, chapters, 3, 0);

    tc_data *d = NULL;
    tc_status st = write_and_parse(&b, &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st != TC_OK) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        cleanup(&b, d);
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 3);
    check_chapter(d, 0, 0, 0, "One");
    check_chapter(d, 0, 1, 1000000000LL, "Two");
    check_chapter(d, 0, 2, 2500000000LL, "Three");
    cleanup(&b, d);
}

/* A byte order mark selects UTF-16 or is stripped from UTF-8. */
static void test_nero_title_encodings(void) {
    tc_buf b;
    tc_buf_init(&b);
    put_ftyp(&b);
    size_t moov = box_open(&b, "moov");
    put_mvhd(&b, 1000, 4000);
    size_t udta = box_open(&b, "udta");
    size_t chpl = box_open(&b, "chpl");
    put_full_box_v1(&b);
    tc_buf_putc(&b, 0);
    put_be32(&b, 3);
    /* UTF-8 with a BOM: 3 BOM bytes + 5 text bytes. */
    put_be64(&b, 0);
    tc_buf_putc(&b, 8);
    tc_buf_write(&b, "\xEF\xBB\xBFhello", 8);
    /* UTF-16LE with a BOM. */
    put_be64(&b, 10000000);
    tc_buf_putc(&b, 6);
    tc_buf_write(&b, "\xFF\xFEh\x00i\x00", 6);
    /* UTF-16BE with a BOM. */
    put_be64(&b, 20000000);
    tc_buf_putc(&b, 6);
    tc_buf_write(&b, "\xFE\xFF\x00h\x00i", 6);
    box_close(&b, chpl);
    box_close(&b, udta);
    box_close(&b, moov);
    size_t mdat = box_open(&b, "mdat");
    tc_buf_puts(&b, "x");
    box_close(&b, mdat);

    tc_data *d = NULL;
    tc_status st = write_and_parse(&b, &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st != TC_OK) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        cleanup(&b, d);
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 3);
    check_chapter(d, 0, 0, 0, "hello");
    check_chapter(d, 0, 1, 1000000000LL, "hi");
    check_chapter(d, 0, 2, 2000000000LL, "hi");
    cleanup(&b, d);
}

/* A 64-bit box size (size field 1) on the moov box must be traversed. */
static void test_nero_64bit_box_size(void) {
    const nero_chapter chapters[] = {
        {0, "First"},
        {50000000ULL, "Second"},
    };
    tc_buf plain;
    build_nero_file(&plain, 1000, 6000, chapters, 2, 1);

    /* Widen the top-level moov box to 64-bit form. */
    size_t moov_at = find_top_box(&plain, "moov");
    TC_CHECK(moov_at != 0);
    if (moov_at == 0) {
        tc_buf_free(&plain);
        return;
    }

    tc_buf b;
    tc_buf_init(&b);
    tc_buf_write(&b, plain.data, moov_at);
    put_be32(&b, 1);
    tc_buf_write(&b, "moov", 4);
    /* The 64-bit size covers the whole box including its 16-byte header. */
    put_be64(&b, (uint64_t)get_be32(plain.data + moov_at) + 8);
    tc_buf_write(&b, plain.data + moov_at + 8, plain.len - moov_at - 8);
    tc_buf_free(&plain);

    tc_data *d = NULL;
    tc_status st = write_and_parse(&b, &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st != TC_OK) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        cleanup(&b, d);
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    check_chapter(d, 0, 0, 0, "First");
    check_chapter(d, 0, 1, 5000000000LL, "Second");
    cleanup(&b, d);
}

/* ------------------------------------------------------------------ */
/* QuickTime chapters                                                 */
/* ------------------------------------------------------------------ */

static void test_quicktime_track(void) {
    /* Timescale 600: chapters at 0, 1 s and 2.5 s. */
    const qt_sample samples[] = {
        {"Opening", 600, 1},
        {"Middle", 900, 1},
        {"Ending", 1200, 1},
    };
    tc_buf b;
    build_qt_file(&b, 600, samples, 3, NULL, 0);

    tc_data *d = NULL;
    tc_status st = write_and_parse(&b, &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st != TC_OK) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        cleanup(&b, d);
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 3);
    check_chapter(d, 0, 0, 0, "Opening");
    check_chapter(d, 0, 1, 1000000000LL, "Middle");
    check_chapter(d, 0, 2, 2500000000LL, "Ending");
    cleanup(&b, d);
}

/* Samples spread over several chunks with different samples-per-chunk runs. */
static void test_quicktime_multi_chunk(void) {
    /* Timescale 1000; chunk 1 holds one sample, chunk 2 two, chunk 3 one. */
    const qt_sample samples[] = {
        {"A", 250, 1},
        {"B", 250, 2},
        {"C", 500, 2},
        {"D", 1000, 3},
    };
    tc_buf b;
    build_qt_file(&b, 1000, samples, 4, NULL, 0);

    tc_data *d = NULL;
    tc_status st = write_and_parse(&b, &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st != TC_OK) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        cleanup(&b, d);
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 4);
    check_chapter(d, 0, 0, 0, "A");
    check_chapter(d, 0, 1, 250000000LL, "B");
    check_chapter(d, 0, 2, 500000000LL, "C");
    check_chapter(d, 0, 3, 1000000000LL, "D");
    cleanup(&b, d);
}

/* QuickTime is tried first when a file carries both storages. */
static void test_quicktime_wins_over_nero(void) {
    const qt_sample samples[] = {
        {"Qt chapter", 1000, 1},
    };
    const nero_chapter nero[] = {{0, "Nero chapter"}};
    tc_buf b;
    build_qt_file(&b, 1000, samples, 1, nero, 1);

    tc_data *d = NULL;
    tc_status st = write_and_parse(&b, &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st != TC_OK) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        cleanup(&b, d);
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    check_chapter(d, 0, 0, 0, "Qt chapter");
    cleanup(&b, d);
}

/* ------------------------------------------------------------------ */
/* No chapters                                                        */
/* ------------------------------------------------------------------ */

/* The reference fabricates one chapter at the movie duration, named
 * "Chapter 01", when neither storage is present. */
static void test_no_chapters_fallback(void) {
    tc_buf b;
    build_no_chapters_file(&b, 1000, 29150);

    tc_data *d = NULL;
    tc_status st = write_and_parse(&b, &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st != TC_OK) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        cleanup(&b, d);
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    check_chapter(d, 0, 0, 29150000000LL, "Chapter 01");
    cleanup(&b, d);
}

/* An empty chpl list is not chapters either: the fallback applies. */
static void test_empty_chpl_fallback(void) {
    tc_buf b;
    build_nero_file(&b, 1000, 5000, NULL, 0, 1);

    tc_data *d = NULL;
    tc_status st = write_and_parse(&b, &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st != TC_OK) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        cleanup(&b, d);
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    check_chapter(d, 0, 0, 5000000000LL, "Chapter 01");
    cleanup(&b, d);
}

/* ------------------------------------------------------------------ */
/* Error paths                                                        */
/* ------------------------------------------------------------------ */

static void test_truncated_chpl_title(void) {
    const nero_chapter chapters[] = {
        {0, "One"},
        {10000000ULL, "A name that gets cut off"},
    };
    tc_buf full;
    build_nero_file(&full, 1000, 20000, chapters, 2, 1);

    /* Cut the last bytes of the chpl box, in the middle of the second title.
     * Its declared size still claims the original length. */
    size_t moov_at = find_top_box(&full, "moov");
    size_t udta_at = moov_at + 8 + get_be32(full.data + moov_at + 8);
    size_t chpl_at = udta_at + 8;
    uint32_t chpl_size = get_be32(full.data + chpl_at);

    tc_buf b;
    tc_buf_init(&b);
    tc_buf_write(&b, full.data, chpl_at + chpl_size - 3);
    tc_buf_free(&full);

    tc_data *d = NULL;
    tc_status st = write_and_parse(&b, &d);
    TC_CHECK(st != TC_OK);
    TC_CHECK(d == NULL);
    cleanup(&b, d);
}

static void test_chpl_count_out_of_range(void) {
    tc_buf b;
    tc_buf_init(&b);
    put_ftyp(&b);
    size_t moov = box_open(&b, "moov");
    put_mvhd(&b, 1000, 10000);
    size_t udta = box_open(&b, "udta");
    size_t chpl = box_open(&b, "chpl");
    put_full_box_v1(&b);
    tc_buf_putc(&b, 0);
    put_be32(&b, 0xffffffffu); /* impossible count */
    box_close(&b, chpl);
    box_close(&b, udta);
    box_close(&b, moov);

    tc_data *d = NULL;
    tc_status st = write_and_parse(&b, &d);
    TC_CHECK_EQ_INT(st, TC_E_FORMAT);
    TC_CHECK_CONTAINS(tc_last_error(), "out of range");
    cleanup(&b, d);
}

/* Version 0 can hold at most 255 chapters; a count that cannot fit in the box
 * must fail rather than read past the end. */
static void test_chpl_v0_count_out_of_range(void) {
    tc_buf b;
    tc_buf_init(&b);
    put_ftyp(&b);
    size_t moov = box_open(&b, "moov");
    put_mvhd(&b, 1000, 10000);
    size_t udta = box_open(&b, "udta");
    size_t chpl = box_open(&b, "chpl");
    put_full_box(&b);
    tc_buf_putc(&b, (char)200); /* 200 entries claimed, none present */
    box_close(&b, chpl);
    box_close(&b, udta);
    box_close(&b, moov);

    tc_data *d = NULL;
    tc_status st = write_and_parse(&b, &d);
    TC_CHECK_EQ_INT(st, TC_E_FORMAT);
    TC_CHECK_CONTAINS(tc_last_error(), "out of range");
    cleanup(&b, d);
}

static void test_no_moov(void) {
    tc_buf b;
    tc_buf_init(&b);
    put_ftyp(&b);
    size_t mdat = box_open(&b, "mdat");
    tc_buf_puts(&b, "just media");
    box_close(&b, mdat);

    tc_data *d = NULL;
    tc_status st = write_and_parse(&b, &d);
    TC_CHECK_EQ_INT(st, TC_E_FORMAT);
    TC_CHECK_CONTAINS(tc_last_error(), "no moov box");
    cleanup(&b, d);
}

static void test_invalid_box_size(void) {
    /* A size below the 8-byte header is malformed. */
    tc_buf b;
    tc_buf_init(&b);
    put_be32(&b, 4);
    tc_buf_write(&b, "junk", 4);
    tc_buf_puts(&b, "xxxx");

    tc_data *d = NULL;
    tc_status st = write_and_parse(&b, &d);
    TC_CHECK_EQ_INT(st, TC_E_FORMAT);
    TC_CHECK_CONTAINS(tc_last_error(), "truncated or invalid");
    cleanup(&b, d);
}

static void test_not_an_mp4_file(void) {
    const char *const inputs[] = {
        "",
        "junk",
        "\x00\x00\x00\x08junk",
    };
    for (size_t i = 0; i < sizeof(inputs) / sizeof(inputs[0]); i++) {
        tc_buf b;
        tc_buf_init(&b);
        if (inputs[i][0] != '\0') {
            tc_buf_write(&b, inputs[i], strlen(inputs[i]));
        }
        tc_data *d = NULL;
        tc_status st = write_and_parse(&b, &d);
        TC_CHECK(st != TC_OK);
        TC_CHECK(d == NULL);
        cleanup(&b, d);
    }
}

static void test_missing_file_is_io_error(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_file("/definitely/not/here.mp4", TC_FMT_MP4, &d),
                    TC_E_IO);
    TC_CHECK(d == NULL);
}

static void test_null_arguments(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_file(NULL, TC_FMT_MP4, &d), TC_E_INVALID);
    TC_CHECK_EQ_INT(tc_parse_file("x.mp4", TC_FMT_MP4, NULL), TC_E_INVALID);
}

/* ------------------------------------------------------------------ */
/* Suite                                                              */
/* ------------------------------------------------------------------ */

TC_SUITE(mp4) {
    TC_CASE("real_nero_fixture");     test_real_nero_fixture();
    TC_CASE("nero_v1");               test_nero_v1_synthetic();
    TC_CASE("nero_v0_timescale");     test_nero_v0_timescale();
    TC_CASE("nero_title_encodings");  test_nero_title_encodings();
    TC_CASE("nero_64bit_box");        test_nero_64bit_box_size();
    TC_CASE("quicktime_track");       test_quicktime_track();
    TC_CASE("quicktime_multi_chunk"); test_quicktime_multi_chunk();
    TC_CASE("quicktime_over_nero");   test_quicktime_wins_over_nero();
    TC_CASE("no_chapters");           test_no_chapters_fallback();
    TC_CASE("empty_chpl");            test_empty_chpl_fallback();
    TC_CASE("truncated_chpl");        test_truncated_chpl_title();
    TC_CASE("chpl_count_range");      test_chpl_count_out_of_range();
    TC_CASE("chpl_v0_count_range");   test_chpl_v0_count_out_of_range();
    TC_CASE("no_moov");               test_no_moov();
    TC_CASE("invalid_box_size");      test_invalid_box_size();
    TC_CASE("not_mp4");               test_not_an_mp4_file();
    TC_CASE("missing_file");          test_missing_file_is_io_error();
    TC_CASE("null_arguments");        test_null_arguments();
}
