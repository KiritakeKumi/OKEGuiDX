/*
 * TAK parser tests (B6).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * The fixture testdata/CUE/example-cue-sheet.tak is the reference project's own
 * test asset (TChapter.Test/Assets/CUE/example-cue-sheet.tak), copied verbatim.
 * Its tail carries an APE tag whose "Cuesheet" item holds the same CUE sheet as
 * example-cue-sheet.cue; TChapter.Test's CUEParserTest expects the chapters at
 * 0 and 2.76 s for both files.
 *
 * The reference TAK parser does not read the APE tag structure: it scans the
 * last 20 KiB for the text "cuesheet" and treats what follows as a CUE sheet.
 * The synthetic files below pin the quirks of that scan, which a structural
 * reader would not share: the two-byte skip after the key, the six-NUL
 * terminator, the forward scan that makes the first match win, and the
 * window/fallback behaviour.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_test.h"

/* ------------------------------------------------------------------ */
/* Synthetic TAK files                                                */
/* ------------------------------------------------------------------ */

/* Appends the four-byte TAK magic and filler that contains neither the key nor
 * a NUL run. */
static void put_magic_and_filler(tc_buf *b) {
    tc_buf_puts(b, "tBaK");
    for (int i = 0; i < 64; i++) {
        tc_buf_putc(b, (char)0xAA);
    }
}

/* Appends an APE-tag-like item: the key, a NUL separator, the value and the
 * six-NUL terminator the reference looks for. */
static void put_item(tc_buf *b, const char *key, const char *value) {
    tc_buf_puts(b, key);
    tc_buf_putc(b, '\0');
    tc_buf_puts(b, value);
    for (int i = 0; i < 6; i++) {
        tc_buf_putc(b, '\0');
    }
}

/* Writes a buffer to a file and parses it. */
static tc_status parse_buffer(const tc_buf *b, tc_data **out) {
    const char *path = "build/tak_synthetic.tak";
    FILE *f = fopen(path, "wb");
    if (!f) {
        return TC_E_IO;
    }
    size_t n = fwrite(b->data, 1, b->len, f);
    fclose(f);
    if (n != b->len) {
        return TC_E_IO;
    }
    return tc_parse_file(path, TC_FMT_TAK, out);
}

/* Two tracks, the second at CUE frame 57 (2.76 s), mirroring the fixture. */
static const char sheet_276[] =
    "PERFORMER \"Orbital\"\r\n"
    "TITLE \"Back To Mine\"\r\n"
    "FILE \"Orbital - Back To Mine.mp3\" WAVE\r\n"
    "  TRACK 01 AUDIO\r\n"
    "    TITLE \"First\"\r\n"
    "    INDEX 01 00:00:00\r\n"
    "  TRACK 02 AUDIO\r\n"
    "    TITLE \"Second\"\r\n"
    "    INDEX 01 00:02:57\r\n";

static const char sheet_1s[] =
    "TITLE \"First tag\"\r\n"
    "FILE \"a.mp3\" WAVE\r\n"
    "  TRACK 01 AUDIO\r\n"
    "    TITLE \"A\"\r\n"
    "    INDEX 01 00:01:00\r\n";

static const char sheet_2s[] =
    "TITLE \"Second tag\"\r\n"
    "FILE \"b.mp3\" WAVE\r\n"
    "  TRACK 01 AUDIO\r\n"
    "    TITLE \"B\"\r\n"
    "    INDEX 01 00:02:00\r\n";

/* ------------------------------------------------------------------ */
/* Real fixture                                                       */
/* ------------------------------------------------------------------ */

static void test_real_tak_fixture(void) {
    char *probe = NULL;
    size_t probe_len = 0;
    if (tc_test_read_data("CUE/example-cue-sheet.tak", &probe, &probe_len) != TC_OK) {
        /* The fixture is optional so that a checkout without it still builds;
         * report loudly rather than silently passing. */
        fprintf(stderr, "  (skipped: testdata/CUE/example-cue-sheet.tak not found)\n");
        return;
    }
    free(probe);

    tc_data *d = NULL;
    tc_status st = tc_parse_file("testdata/CUE/example-cue-sheet.tak", TC_FMT_TAK, &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st != TC_OK) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }

    /* The C# test expects the chapters at 0 and 2.76 s. */
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);

    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.title, "Back To Mine");
    TC_CHECK_EQ_STR(ei.source, "Orbital - Back To Mine.mp3");
    TC_CHECK_EQ_INT(ei.duration_ns, 2760000000LL);

    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    TC_CHECK_EQ_INT(c.time_ns, 0);
    TC_CHECK_EQ_STR(c.name, "John Barry & His Orchestra - The Knack");
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 1, &c), TC_OK);
    TC_CHECK_EQ_INT(c.time_ns, 2760000000LL);
    TC_CHECK_EQ_STR(c.name, "Lee Perry & The Upsetters - Justice To The People");
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* The scan                                                           */
/* ------------------------------------------------------------------ */

/* The reference skips two bytes after the last character of "cuesheet": one
 * for the byte it has read plus one for the unconditional `Position + 1`. With
 * the APE tag's NUL separator that lands exactly on the first byte of the
 * value. */
static void test_synthetic_tag(void) {
    tc_buf b;
    tc_buf_init(&b);
    put_magic_and_filler(&b);
    put_item(&b, "Cuesheet", sheet_276);

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st == TC_OK) {
        TC_CHECK_EQ_INT(tc_entry_count(d), 1);
        TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
        tc_entry_info_t ei;
        TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
        TC_CHECK_EQ_STR(ei.title, "Back To Mine");
        tc_chapter_t c;
        TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 1, &c), TC_OK);
        TC_CHECK_EQ_INT(c.time_ns, 2760000000LL);
    }
    tc_buf_free(&b);
    tc_free(d);
}

/* The state machine folds A-Z to a-z, so the key may be spelled any way. */
static void test_key_is_case_insensitive(void) {
    tc_buf b;
    tc_buf_init(&b);
    put_magic_and_filler(&b);
    put_item(&b, "CuEsHeEt", sheet_276);

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st == TC_OK) {
        TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    }
    tc_buf_free(&b);
    tc_free(d);
}

/* The two-byte skip is unconditional, so a value that starts immediately after
 * the key loses its first byte. Here the lost 'T' turns the TITLE line into
 * "ITLE ...", which the CUE parser does not recognise: the entry keeps no
 * title. */
static void test_skip_eats_value_byte(void) {
    tc_buf b;
    tc_buf_init(&b);
    put_magic_and_filler(&b);
    tc_buf_puts(&b, "Cuesheet");
    tc_buf_puts(&b,
                "TITLE \"lost\"\r\n"
                "PERFORMER \"Orbital\"\r\n"
                "FILE \"x.mp3\" WAVE\r\n"
                "  TRACK 01 AUDIO\r\n"
                "    TITLE \"First\"\r\n"
                "    INDEX 01 00:00:00\r\n");
    for (int i = 0; i < 6; i++) {
        tc_buf_putc(&b, '\0');
    }

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st == TC_OK) {
        tc_entry_info_t ei;
        TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
        TC_CHECK_EQ_STR(ei.title, ""); /* the 'T' was skipped */
        TC_CHECK_EQ_STR(ei.source, "x.mp3");
        TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    }
    tc_buf_free(&b);
    tc_free(d);
}

/* The scan runs forward from the window start, so the first "cuesheet" in the
 * window wins even when a later one exists. */
static void test_first_match_wins(void) {
    tc_buf b;
    tc_buf_init(&b);
    put_magic_and_filler(&b);
    put_item(&b, "Cuesheet", sheet_1s);
    put_item(&b, "Cuesheet", sheet_2s);

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st == TC_OK) {
        tc_chapter_t c;
        TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
        TC_CHECK_EQ_INT(c.time_ns, 1000000000LL);
    }
    tc_buf_free(&b);
    tc_free(d);
}

/* The real fixture's tail layout: the value ends in CR LF, one more byte
 * follows, then the six-NUL run. The reference backs endPos up by one when the
 * two bytes before the run are CR LF, so the copy ends on that extra byte. */
static void test_crlf_before_terminator(void) {
    tc_buf b;
    tc_buf_init(&b);
    put_magic_and_filler(&b);
    tc_buf_puts(&b, "Cuesheet");
    tc_buf_putc(&b, '\0');
    tc_buf_puts(&b, sheet_276);
    tc_buf_putc(&b, (char)0x19); /* the fixture's byte before the NULs */
    for (int i = 0; i < 6; i++) {
        tc_buf_putc(&b, '\0');
    }

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st == TC_OK) {
        TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    }
    tc_buf_free(&b);
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Error paths                                                        */
/* ------------------------------------------------------------------ */

/* No key in a file shorter than the window: the reference's `beginPos == 0`
 * guard returns Stream.Null, which the CUE parser reports as an empty sheet. */
static void test_no_tag_small_file(void) {
    tc_buf b;
    tc_buf_init(&b);
    put_magic_and_filler(&b);

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_E_FORMAT);
    TC_CHECK_CONTAINS(tc_last_error(), "empty cue file");
    TC_CHECK(d == NULL);
    tc_buf_free(&b);
}

/* A key that ends the file: the reference seeks one byte past the end and
 * copies nothing. */
static void test_key_at_end_of_file(void) {
    tc_buf b;
    tc_buf_init(&b);
    put_magic_and_filler(&b);
    tc_buf_puts(&b, "Cuesheet");

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_E_FORMAT);
    TC_CHECK_CONTAINS(tc_last_error(), "empty cue file");
    tc_buf_free(&b);
}

/* Five NULs are not a terminator; with none found the reference's endPos stays
 * 0 and the guard yields an empty stream. */
static void test_unterminated_value(void) {
    tc_buf b;
    tc_buf_init(&b);
    put_magic_and_filler(&b);
    tc_buf_puts(&b, "Cuesheet");
    tc_buf_putc(&b, '\0');
    tc_buf_puts(&b, sheet_276);
    for (int i = 0; i < 5; i++) {
        tc_buf_putc(&b, '\0');
    }

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_E_FORMAT);
    TC_CHECK_CONTAINS(tc_last_error(), "empty cue file");
    tc_buf_free(&b);
}

/* A key before the last 20 KiB is invisible. The fallback then copies from the
 * window start to the first six-NUL run; this file has none, so the sheet is
 * empty. */
static void test_key_outside_scan_window(void) {
    tc_buf b;
    tc_buf_init(&b);
    put_magic_and_filler(&b);
    put_item(&b, "Cuesheet", sheet_276);
    for (int i = 0; i < 25000; i++) {
        tc_buf_putc(&b, (char)0xAA);
    }

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_E_FORMAT);
    TC_CHECK_CONTAINS(tc_last_error(), "empty cue file");
    tc_buf_free(&b);
}

/* A value that is not a CUE sheet fails in the CUE parser, not here. */
static void test_value_is_not_a_cue_sheet(void) {
    tc_buf b;
    tc_buf_init(&b);
    put_magic_and_filler(&b);
    put_item(&b, "Cuesheet", "not a cue sheet at all\r\n");

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_E_FORMAT);
    TC_CHECK_CONTAINS(tc_last_error(), "empty cue file");
    tc_buf_free(&b);
}

/* A file longer than the window with no key at all: beginPos is the window
 * start, which is not 0, so the guard does not fire. The reference copies from
 * the window start to the first six-NUL run and hands binary padding to the
 * CUE parser; there is no "no cuesheet found" error path. */
static void test_no_match_long_file_fallback(void) {
    tc_buf b;
    tc_buf_init(&b);
    put_magic_and_filler(&b);
    for (int i = 0; i < 25000; i++) {
        tc_buf_putc(&b, (char)0xAA);
    }
    for (int i = 0; i < 6; i++) {
        tc_buf_putc(&b, '\0');
    }

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_E_FORMAT);
    TC_CHECK_CONTAINS(tc_last_error(), "empty cue file");
    tc_buf_free(&b);
}

/* The ordinary writer output: the value ends in CR LF immediately before the
 * six NULs. The CR LF check then reads the character before the CR and does
 * not fire, so the copy keeps the final line break. */
static void test_crlf_at_terminator(void) {
    tc_buf b;
    tc_buf_init(&b);
    put_magic_and_filler(&b);
    put_item(&b, "Cuesheet", sheet_276);

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st == TC_OK) {
        TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
        tc_chapter_t c;
        TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 1, &c), TC_OK);
        TC_CHECK_EQ_INT(c.time_ns, 2760000000LL);
    }
    tc_buf_free(&b);
    tc_free(d);
}

/* The fallback is observable: with the key outside the window, the reference
 * copies from the window start to the first six-NUL run, so whatever bytes lie
 * there reach the CUE parser. Here that is padding plus a cue sheet that uses
 * INDEX 02, which makes the CUE parser fail with its own error rather than the
 * TAK layer reporting a missing key. */
static void test_no_match_fallback_reaches_cue_parser(void) {
    tc_buf b;
    tc_buf_init(&b);
    put_magic_and_filler(&b);
    put_item(&b, "Cuesheet", sheet_276); /* pushed out of the window below */
    for (int i = 0; i < 25000; i++) {
        tc_buf_putc(&b, (char)0xAA);
    }
    /* Inside the window, and containing no "cuesheet" text of its own. */
    tc_buf_puts(&b, "FILE \"x.mp3\" WAVE\r\n  TRACK 01 AUDIO\r\n    INDEX 02 00:00:00\r\n");
    for (int i = 0; i < 6; i++) {
        tc_buf_putc(&b, '\0');
    }

    tc_data *d = NULL;
    tc_status st = parse_buffer(&b, &d);
    TC_CHECK_EQ_INT(st, TC_E_FORMAT);
    TC_CHECK_CONTAINS(tc_last_error(), "unable to parse this cue file");
    tc_buf_free(&b);
}

static void test_not_a_tak_file(void) {
    const char *inputs[] = {
        "",
        "fLaC",
        "tBa",
        "tBak",
        "TAK ",
    };
    for (size_t i = 0; i < sizeof(inputs) / sizeof(inputs[0]); i++) {
        tc_buf b;
        tc_buf_init(&b);
        tc_buf_puts(&b, inputs[i]);
        tc_data *d = NULL;
        tc_status st = parse_buffer(&b, &d);
        TC_CHECK_EQ_INT(st, TC_E_FORMAT);
        TC_CHECK_CONTAINS(tc_last_error(), "not a TAK file");
        TC_CHECK(d == NULL);
        tc_buf_free(&b);
    }
}

static void test_missing_file_is_io_error(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_file("/definitely/not/here.tak", TC_FMT_TAK, &d), TC_E_IO);
    TC_CHECK(d == NULL);
}

static void test_null_arguments(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_file(NULL, TC_FMT_TAK, &d), TC_E_INVALID);
    TC_CHECK_EQ_INT(tc_parse_file("x.tak", TC_FMT_TAK, NULL), TC_E_INVALID);
    TC_CHECK_EQ_INT(tc_tak_parse_file(NULL, NULL), TC_E_INVALID);
}

/* ------------------------------------------------------------------ */
/* Suite                                                              */
/* ------------------------------------------------------------------ */

TC_SUITE(tak) {
    TC_CASE("real_fixture");           test_real_tak_fixture();
    TC_CASE("synthetic_tag");          test_synthetic_tag();
    TC_CASE("case_insensitive_key");   test_key_is_case_insensitive();
    TC_CASE("skip_eats_value_byte");   test_skip_eats_value_byte();
    TC_CASE("first_match_wins");       test_first_match_wins();
    TC_CASE("crlf_before_terminator"); test_crlf_before_terminator();
    TC_CASE("no_tag_small_file");      test_no_tag_small_file();
    TC_CASE("key_at_end_of_file");     test_key_at_end_of_file();
    TC_CASE("unterminated_value");     test_unterminated_value();
    TC_CASE("key_outside_window");     test_key_outside_scan_window();
    TC_CASE("value_not_a_cue");        test_value_is_not_a_cue_sheet();
    TC_CASE("no_match_long_file");     test_no_match_long_file_fallback();
    TC_CASE("fallback_reaches_cue");   test_no_match_fallback_reaches_cue_parser();
    TC_CASE("crlf_at_terminator");     test_crlf_at_terminator();
    TC_CASE("not_tak");                test_not_a_tak_file();
    TC_CASE("missing_file");           test_missing_file_is_io_error();
    TC_CASE("null_arguments");         test_null_arguments();
}
