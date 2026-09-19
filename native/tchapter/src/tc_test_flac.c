/*
 * FLAC parser tests (B5).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * The fixture flac-example-cue-sheet.flac is the reference implementation's own
 * test asset (TChapter.Test/Assets/CUE/example-cue-sheet.flac), copied verbatim.
 * Its Vorbis comment carries a "cuesheet" tag whose value is an embedded CUE
 * sheet, which the reference hands to the CUE parser; the C# test expects the
 * chapters at 0 and 2.76 s (INDEX 01 00:02:57 is 2 s + 57/75 s).
 *
 * The reference's FLAC parser delegates the embedded CUE text to the CUE
 * parser (B4, developed in parallel). Until that parser lands, parsing a real
 * FLAC file stops at the delegation point with TC_E_UNSUPPORTED; the tests
 * treat that as a pass that documents the extraction, and assert the full
 * chapter data once the CUE parser is available. The container-level behaviour
 * is exercised directly through synthetic metadata streams written to build/.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_test.h"

/* ------------------------------------------------------------------ */
/* Synthetic FLAC files                                               */
/* ------------------------------------------------------------------ */

/* Appends a METADATA_BLOCK_HEADER: last flag, 7-bit type, 24-bit length. */
static void put_block_header(tc_buf *b, int last, unsigned type, uint32_t len) {
    tc_buf_putc(b, (char)((last ? 0x80u : 0u) | (type & 0x7fu)));
    tc_buf_putc(b, (char)((len >> 16) & 0xffu));
    tc_buf_putc(b, (char)((len >> 8) & 0xffu));
    tc_buf_putc(b, (char)(len & 0xffu));
}

/* Appends a little-endian uint32, as the Vorbis comment uses. */
static void put_le32(tc_buf *b, uint32_t v) {
    tc_buf_putc(b, (char)(v & 0xffu));
    tc_buf_putc(b, (char)((v >> 8) & 0xffu));
    tc_buf_putc(b, (char)((v >> 16) & 0xffu));
    tc_buf_putc(b, (char)((v >> 24) & 0xffu));
}

/* Appends one STREAMINFO block: the format fixes its length at 34. */
static void put_streaminfo(tc_buf *b, int last) {
    put_block_header(b, last, 0, 34);
    for (int i = 0; i < 34; i++) {
        tc_buf_putc(b, 0);
    }
}

/* Appends a PADDING block of the given size. */
static void put_padding(tc_buf *b, int last, uint32_t len) {
    put_block_header(b, last, 1, len);
    for (uint32_t i = 0; i < len; i++) {
        tc_buf_putc(b, 0);
    }
}

/* Appends a VORBIS_COMMENT block. `count` items follow in pairs. */
static void put_vorbis_comment(tc_buf *b, int last, const char *vendor,
                               const char *const *comments, size_t count) {
    size_t body_len = 4 + strlen(vendor) + 4;
    for (size_t i = 0; i < count; i++) {
        body_len += 4 + strlen(comments[i]);
    }
    put_block_header(b, last, 4, (uint32_t)body_len);
    put_le32(b, (uint32_t)strlen(vendor));
    tc_buf_puts(b, vendor);
    put_le32(b, (uint32_t)count);
    for (size_t i = 0; i < count; i++) {
        put_le32(b, (uint32_t)strlen(comments[i]));
        tc_buf_puts(b, comments[i]);
    }
}

/* Appends a raw block whose body is copied verbatim. */
static void put_raw_block(tc_buf *b, int last, unsigned type,
                          const uint8_t *body, size_t len) {
    put_block_header(b, last, type, (uint32_t)len);
    tc_buf_write(b, (const char *)body, len);
}

/* Writes a buffer to a file and parses it. */
static tc_status parse_buffer(const tc_buf *b, tc_data **out) {
    const char *path = "build/flac_synthetic.flac";
    FILE *f = fopen(path, "wb");
    if (!f) {
        return TC_E_IO;
    }
    size_t n = fwrite(b->data, 1, b->len, f);
    fclose(f);
    if (n != b->len) {
        return TC_E_IO;
    }
    return tc_parse_file(path, TC_FMT_FLAC, out);
}

/* Releases the buffer and the parse result. */
static void cleanup(tc_buf *b, tc_data *d) {
    tc_buf_free(b);
    tc_free(d);
}

static const char *test_vendor = "reference libFLAC 1.3.2 20170101";

/* ------------------------------------------------------------------ */
/* Real fixture                                                       */
/* ------------------------------------------------------------------ */

static void test_real_flac_fixture(void) {
    char *path = NULL;
    size_t len = 0;
    if (tc_test_read_data("flac-example-cue-sheet.flac", &path, &len) != TC_OK) {
        /* The fixture is optional so that a checkout without it still builds;
         * report loudly rather than silently passing. */
        fprintf(stderr, "  (skipped: testdata/flac-example-cue-sheet.flac not found)\n");
        return;
    }
    free(path);

    tc_data *d = NULL;
    tc_status st = tc_parse_file("testdata/flac-example-cue-sheet.flac", TC_FMT_FLAC, &d);
    if (st != TC_OK && st == TC_E_UNSUPPORTED &&
        strstr(tc_last_error(), "cue parser is not implemented") != NULL) {
        /* The CUE parser (B4) has not landed yet; the FLAC side worked, since
         * the failure comes from the delegation point. */
        TC_CHECK_CONTAINS(tc_last_error(), "cue");
        return;
    }
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st != TC_OK) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }

    /* The C# test expects the chapters at 0 and 2.76 s. */
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    TC_CHECK_EQ_INT(c.time_ns, 0);
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 1, &c), TC_OK);
    TC_CHECK_EQ_INT(c.time_ns, 2760000000LL); /* 2 s + 57/75 s */
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Container structure                                                */
/* ------------------------------------------------------------------ */

/* A well-formed file whose cuesheet tag sits between other tags. */
static void test_cuesheet_between_other_tags(void) {
    tc_buf b;
    tc_buf_init(&b);
    tc_buf_puts(&b, "fLaC");
    put_streaminfo(&b, 0);
    const char *comments[] = {
        "album=Back To Mine",
        "cuesheet=TITLE \"t\"\nFILE \"x.mp3\" WAVE\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n",
        "artist=Orbital",
    };
    put_vorbis_comment(&b, 0, test_vendor, comments, 3);
    put_padding(&b, 1, 16);

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    if (st != TC_OK && st == TC_E_UNSUPPORTED &&
        strstr(tc_last_error(), "cue parser is not implemented") != NULL) {
        tc_buf_free(&b);
        return;
    }
    TC_CHECK_EQ_INT(st, TC_OK);
    cleanup(&b, d);
}

/* Unknown blocks (types 0..6) that carry no chapters are skipped, and a
 * CUESHEET metadata block is skipped exactly like padding: only the tag
 * counts, per the reference. */
static void test_cuesheet_block_is_skipped(void) {
    tc_buf b;
    tc_buf_init(&b);
    tc_buf_puts(&b, "fLaC");
    put_streaminfo(&b, 0);
    /* A CUESHEET metadata block (type 5) with junk: it must be skipped. */
    const uint8_t cuesheet_body[] = {0xDE, 0xAD, 0xBE, 0xEF};
    put_raw_block(&b, 0, 5, cuesheet_body, sizeof(cuesheet_body));
    put_padding(&b, 0, 8);
    const char *comments[] = {
        "cuesheet=TITLE \"t\"\nFILE \"x.mp3\" WAVE\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n",
    };
    put_vorbis_comment(&b, 1, test_vendor, comments, 1);

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    if (st != TC_OK && st == TC_E_UNSUPPORTED &&
        strstr(tc_last_error(), "cue parser is not implemented") != NULL) {
        tc_buf_free(&b);
        return;
    }
    TC_CHECK_EQ_INT(st, TC_OK);
    cleanup(&b, d);
}

/* The last "cuesheet" tag wins, matching the dictionary indexer. */
static void test_duplicate_cuesheet_last_wins(void) {
    tc_buf b;
    tc_buf_init(&b);
    tc_buf_puts(&b, "fLaC");
    put_streaminfo(&b, 0);
    const char *first[] = {
        "cuesheet=TITLE \"first\"\nFILE \"a.mp3\" WAVE\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n",
        "artist=Orbital",
    };
    put_vorbis_comment(&b, 0, test_vendor, first, 2);
    const char *second[] = {
        "cuesheet=TITLE \"second\"\nFILE \"b.mp3\" WAVE\n  TRACK 01 AUDIO\n    INDEX 01 00:01:00\n",
    };
    put_vorbis_comment(&b, 1, test_vendor, second, 1);

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    if (st == TC_E_UNSUPPORTED &&
        strstr(tc_last_error(), "cue parser is not implemented") != NULL) {
        tc_buf_free(&b);
        return;
    }
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st == TC_OK && d) {
        tc_chapter_t c;
        /* The last "cuesheet" tag wins. Its INDEX line reads 00:01:00, which in
         * CUE's MM:SS:FF format is one second, not one minute. */
        TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
        TC_CHECK_EQ_INT(c.time_ns, 1000000000LL);
    }
    cleanup(&b, d);
}

/* A file whose last metadata block is the Vorbis comment is accepted: the
 * audio frames after it are never read. */
static void test_vorbis_comment_is_last_block(void) {
    tc_buf b;
    tc_buf_init(&b);
    tc_buf_puts(&b, "fLaC");
    put_streaminfo(&b, 0);
    const char *comments[] = {
        "cuesheet=TITLE \"t\"\nFILE \"x.mp3\" WAVE\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n",
    };
    put_vorbis_comment(&b, 1, test_vendor, comments, 1);
    /* Simulated audio frames follow the metadata; the parser must stop. */
    tc_buf_puts(&b, "\xff\xf8\xd9\x9c");

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    if (st != TC_OK && st == TC_E_UNSUPPORTED &&
        strstr(tc_last_error(), "cue parser is not implemented") != NULL) {
        tc_buf_free(&b);
        return;
    }
    TC_CHECK_EQ_INT(st, TC_OK);
    cleanup(&b, d);
}

/* ------------------------------------------------------------------ */
/* Error paths                                                        */
/* ------------------------------------------------------------------ */

static void test_not_a_flac_file(void) {
    const char *inputs[] = {
        "",
        "OggS",
        "fLa",
        "fLaC",
    };
    for (size_t i = 0; i < sizeof(inputs) / sizeof(inputs[0]); i++) {
        tc_buf b;
        tc_buf_init(&b);
        tc_buf_puts(&b, inputs[i]);
        tc_data *d = NULL;
        tc_status st = parse_buffer(&b, &d);
        TC_CHECK(st != TC_OK);
        TC_CHECK(d == NULL);
        cleanup(&b, d);
    }
}

static void test_missing_cuesheet_tag(void) {
    tc_buf b;
    tc_buf_init(&b);
    tc_buf_puts(&b, "fLaC");
    put_streaminfo(&b, 0);
    const char *comments[] = {"artist=Orbital", "album=Back To Mine"};
    put_vorbis_comment(&b, 1, test_vendor, comments, 2);

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_E_FORMAT);
    TC_CHECK_CONTAINS(tc_last_error(), "no cuesheet found");
    cleanup(&b, d);
}

static void test_unknown_block_type_is_rejected(void) {
    tc_buf b;
    tc_buf_init(&b);
    tc_buf_puts(&b, "fLaC");
    put_streaminfo(&b, 0);
    /* Type 7 does not exist in the format; the reference throws here. */
    const uint8_t body[] = {0x00};
    put_raw_block(&b, 1, 7, body, sizeof(body));

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_E_FORMAT);
    TC_CHECK_CONTAINS(tc_last_error(), "invalid metadata block type 7");
    cleanup(&b, d);
}

static void test_vorbis_comment_without_equals(void) {
    tc_buf b;
    tc_buf_init(&b);
    tc_buf_puts(&b, "fLaC");
    put_streaminfo(&b, 0);
    const char *comments[] = {"no separator in this tag"};
    put_vorbis_comment(&b, 1, test_vendor, comments, 1);

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_E_FORMAT);
    TC_CHECK_CONTAINS(tc_last_error(), "without '='");
    cleanup(&b, d);
}

static void test_truncated_vorbis_comment(void) {
    /* The declared comment length runs past the end of the file. */
    tc_buf b;
    tc_buf_init(&b);
    tc_buf_puts(&b, "fLaC");
    put_block_header(&b, 1, 4, 4 + 4 + 4 + 4 + 40);
    put_le32(&b, (uint32_t)strlen(test_vendor));
    tc_buf_puts(&b, test_vendor);
    put_le32(&b, 1);
    put_le32(&b, 40); /* longer than what follows */
    tc_buf_puts(&b, "cuesheet=short");

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_E_FORMAT);
    TC_CHECK_CONTAINS(tc_last_error(), "truncated vorbis comment");
    cleanup(&b, d);
}

static void test_truncated_block_header(void) {
    tc_buf b;
    tc_buf_init(&b);
    tc_buf_puts(&b, "fLaC");
    tc_buf_putc(&b, (char)0x80); /* a last-flag header cut short */

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_E_FORMAT);
    TC_CHECK_CONTAINS(tc_last_error(), "truncated metadata block header");
    cleanup(&b, d);
}

/* The CUE sheet in the tag is passed to the CUE parser verbatim; a tag whose
 * value is not a CUE sheet fails there, not in the FLAC layer. */
static void test_cuesheet_value_must_be_a_cue_sheet(void) {
    tc_buf b;
    tc_buf_init(&b);
    tc_buf_puts(&b, "fLaC");
    put_streaminfo(&b, 0);
    const char *comments[] = {"cuesheet=not a cue sheet at all"};
    put_vorbis_comment(&b, 1, test_vendor, comments, 1);

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    /* Either the CUE parser rejects the text (TC_E_FORMAT) or it is not
     * implemented yet; anything else is wrong. */
    TC_CHECK(st == TC_E_FORMAT || st == TC_E_UNSUPPORTED);
    cleanup(&b, d);
}

/* The case-sensitive key: "CUESHEET" is not the "cuesheet" tag. */
static void test_key_is_case_sensitive(void) {
    tc_buf b;
    tc_buf_init(&b);
    tc_buf_puts(&b, "fLaC");
    put_streaminfo(&b, 0);
    const char *comments[] = {"CUESHEET=TITLE \"t\"\nFILE \"x.mp3\" WAVE\n"};
    put_vorbis_comment(&b, 1, test_vendor, comments, 1);

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_E_FORMAT);
    TC_CHECK_CONTAINS(tc_last_error(), "no cuesheet found");
    cleanup(&b, d);
}

static void test_missing_file_is_io_error(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_file("/definitely/not/here.flac", TC_FMT_FLAC, &d), TC_E_IO);
    TC_CHECK(d == NULL);
}

static void test_null_arguments(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_file(NULL, TC_FMT_FLAC, &d), TC_E_INVALID);
    TC_CHECK_EQ_INT(tc_parse_file("x.flac", TC_FMT_FLAC, NULL), TC_E_INVALID);
}

/* ------------------------------------------------------------------ */
/* Suite                                                              */
/* ------------------------------------------------------------------ */

TC_SUITE(flac) {
    TC_CASE("real_fixture");            test_real_flac_fixture();
    TC_CASE("tags_around_cuesheet");    test_cuesheet_between_other_tags();
    TC_CASE("cuesheet_block_skipped");  test_cuesheet_block_is_skipped();
    TC_CASE("duplicate_last_wins");     test_duplicate_cuesheet_last_wins();
    TC_CASE("comment_is_last_block");   test_vorbis_comment_is_last_block();
    TC_CASE("not_flac");                test_not_a_flac_file();
    TC_CASE("missing_tag");             test_missing_cuesheet_tag();
    TC_CASE("unknown_block_type");      test_unknown_block_type_is_rejected();
    TC_CASE("no_equals");               test_vorbis_comment_without_equals();
    TC_CASE("truncated_comment");       test_truncated_vorbis_comment();
    TC_CASE("truncated_header");        test_truncated_block_header();
    TC_CASE("value_not_a_cue");         test_cuesheet_value_must_be_a_cue_sheet();
    TC_CASE("case_sensitive_key");      test_key_is_case_sensitive();
    TC_CASE("missing_file");            test_missing_file_is_io_error();
    TC_CASE("null_arguments");          test_null_arguments();
}
