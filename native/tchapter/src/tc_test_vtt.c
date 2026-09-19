/*
 * WebVTT chapter parser tests (B11).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * vtt-00001.vtt is the reference implementation's own test asset
 * (TChapter.Test/Assets/VTT/00001.vtt). The remaining inputs are built in
 * memory to isolate the reference's behaviour, and every expected value was
 * verified by replaying VTTParser.GetChapterInfo with .NET Framework 4.8 - the
 * framework the test project actually targets - so the tests assert what the
 * reference produces, not what WebVTT permits.
 *
 * The reference keeps a cue's start time and throws the end time away; the cue
 * end is parsed all the same, so a malformed or missing end time fails the file.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_test.h"

/* ------------------------------------------------------------------ */
/* Helpers                                                            */
/* ------------------------------------------------------------------ */

static tc_status parse_text(const char *text, tc_data **out) {
    return tc_parse_mem(text, strlen(text), TC_FMT_VTT, NULL, out);
}

static tc_status parse_fixture(const char *name, tc_data **out) {
    char *buf = NULL;
    size_t len = 0;
    tc_status st = tc_test_read_data(name, &buf, &len);
    if (st != TC_OK) {
        return st;
    }
    st = tc_parse_mem(buf, len, TC_FMT_VTT, NULL, out);
    free(buf);
    return st;
}

static int64_t chapter_time(tc_data *d, size_t index) {
    tc_chapter_t c;
    if (tc_chapter_at(d, 0, index, &c) != TC_OK) {
        return -1;
    }
    return c.time_ns;
}

static const char *chapter_name(tc_data *d, size_t index) {
    tc_chapter_t c;
    if (tc_chapter_at(d, 0, index, &c) != TC_OK) {
        return NULL;
    }
    return c.name;
}

/* ------------------------------------------------------------------ */
/* The reference asset                                                */
/* ------------------------------------------------------------------ */

static void test_reference_fixture(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("vtt-00001.vtt", &d), TC_OK);
    if (!d) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }

    /* VTT is a single-entry format, whatever the file contains. */
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 7);

    TC_CHECK_EQ_STR(chapter_name(d, 0), "Introduction");
    TC_CHECK_EQ_INT(chapter_time(d, 0), 0);
    TC_CHECK_EQ_STR(chapter_name(d, 1), "Watch out!");
    TC_CHECK_EQ_INT(chapter_time(d, 1), 28206000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 2), "Let's go");
    TC_CHECK_EQ_INT(chapter_time(d, 2), 62034000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 3), "The machine");
    TC_CHECK_EQ_INT(chapter_time(d, 3), 190014000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 4), "Close your eyes");
    TC_CHECK_EQ_INT(chapter_time(d, 4), 341208000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 5), "There's nothing there");
    TC_CHECK_EQ_INT(chapter_time(d, 5), 447125000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 6), "The Colossus of Rhodes");
    TC_CHECK_EQ_INT(chapter_time(d, 6), 493000000000LL);

    /* The reference never sets a duration for VTT. */
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_INT(ei.chapter_count, 7);
    TC_CHECK_EQ_INT(ei.duration_ns, 0);
    TC_CHECK_EQ_STR(ei.title, "");
    TC_CHECK_EQ_STR(ei.source, "");

    /* This format carries no frame information. */
    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    TC_CHECK_EQ_INT(c.frames, -1);

    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Cue end times                                                      */
/* ------------------------------------------------------------------ */

/* The chapter time is the cue's start; the end is parsed and discarded, and it
 * is not taken from the following cue either. */
static void test_end_time_is_ignored(void) {
    const char *text =
        "WEBVTT\n"
        "\n"
        "00:00:00.000 --> 00:00:26.000\n"
        "First\n"
        "\n"
        "00:00:28.206 --> 00:01:02.000\n"
        "Second\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    /* Not 26000000000 (the cue's end) and not 28206000000 (the next start). */
    TC_CHECK_EQ_INT(chapter_time(d, 0), 0);
    TC_CHECK_EQ_INT(chapter_time(d, 1), 28206000000LL);
    tc_free(d);
}

/* A cue without an end time has an empty second field; the reference still
 * parses it and TimeSpan.Parse rejects it, so the whole file fails. */
static void test_missing_end_time_is_rejected(void) {
    const char *text =
        "WEBVTT\n"
        "\n"
        "00:00:00.000 --> 00:00:26.000\n"
        "First\n"
        "\n"
        "00:00:30.000\n"
        "No end\n";
    tc_data *d = NULL;
    TC_CHECK(parse_text(text, &d) != TC_OK);
    TC_CHECK(d == NULL);
    tc_free(d);
}

/* The end time may not be malformed either, even though it is not kept. */
static void test_bad_end_time_is_rejected(void) {
    const char *text =
        "WEBVTT\n"
        "\n"
        "00:00:00.000 --> nonsense\n"
        "First\n";
    tc_data *d = NULL;
    TC_CHECK(parse_text(text, &d) != TC_OK);
    TC_CHECK(d == NULL);
    tc_free(d);
}

/* A cue settings string after the end time is not valid for TimeSpan.Parse. */
static void test_cue_settings_are_rejected(void) {
    const char *text =
        "WEBVTT\n"
        "\n"
        "00:00:00.000 --> 00:00:26.000 align:start\n"
        "First\n";
    tc_data *d = NULL;
    TC_CHECK(parse_text(text, &d) != TC_OK);
    TC_CHECK(d == NULL);
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Block structure                                                    */
/* ------------------------------------------------------------------ */

/* A cue identifier line before the time line is discarded. */
static void test_cue_identifier_is_skipped(void) {
    const char *text =
        "WEBVTT\n"
        "\n"
        "chapter-1\n"
        "00:00:10.000 --> 00:00:20.000\n"
        "Named\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0), "Named");
    TC_CHECK_EQ_INT(chapter_time(d, 0), 10000000000LL);
    tc_free(d);
}

/* The name is the line after the time line; any further cue text is ignored. */
static void test_multiline_cue_text(void) {
    const char *text =
        "WEBVTT\n"
        "\n"
        "00:00:00.000 --> 00:00:10.000\n"
        "Line one\n"
        "Line two\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0), "Line one");
    tc_free(d);
}

/* Three newlines between cues leave a block that holds only a newline; the
 * reference's SkipWhile then runs off the end and throws. */
static void test_triple_newline_block_is_rejected(void) {
    const char *text =
        "WEBVTT\n"
        "\n"
        "00:00:00.000 --> 00:00:10.000\n"
        "First\n"
        "\n"
        "\n"
        "00:00:10.000 --> 00:00:20.000\n"
        "Second\n";
    tc_data *d = NULL;
    TC_CHECK(parse_text(text, &d) != TC_OK);
    TC_CHECK(d == NULL);
    tc_free(d);
}

/* A header block may hold anything as long as it contains WEBVTT; the extra
 * lines are not cues. */
static void test_header_block(void) {
    const char *text =
        "WEBVTT - Some title\n"
        "Kind: captions\n"
        "Language: en\n"
        "\n"
        "00:00:00.000 --> 00:00:10.000\n"
        "A\n"
        "\n"
        "00:00:10.000 --> 00:00:20.000\n"
        "B\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    TC_CHECK_EQ_STR(chapter_name(d, 0), "A");
    TC_CHECK_EQ_STR(chapter_name(d, 1), "B");
    tc_free(d);
}

/* A NOTE block has no time line and fails, even though WebVTT defines it. */
static void test_note_block_is_rejected(void) {
    const char *text =
        "WEBVTT\n"
        "\n"
        "NOTE this is a comment\n"
        "\n"
        "00:00:00.000 --> 00:00:10.000\n"
        "A\n";
    tc_data *d = NULL;
    TC_CHECK(parse_text(text, &d) != TC_OK);
    TC_CHECK(d == NULL);
    tc_free(d);
}

/* The name line is taken verbatim; trailing spaces are not trimmed. */
static void test_name_is_not_trimmed(void) {
    const char *text =
        "WEBVTT\n"
        "\n"
        "00:00:00.000 --> 00:00:10.000\n"
        "Padded   \n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_STR(chapter_name(d, 0), "Padded   ");
    tc_free(d);
}

/* An empty name line is a chapter with an empty name. */
static void test_empty_name(void) {
    const char *text =
        "WEBVTT\n"
        "\n"
        "00:00:00.000 --> 00:00:10.000\n"
        "\n"
        "00:00:10.000 --> 00:00:20.000\n"
        "Second\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    TC_CHECK_EQ_STR(chapter_name(d, 0), "");
    TC_CHECK_EQ_STR(chapter_name(d, 1), "Second");
    tc_free(d);
}

/* The final cue needs no trailing newline. */
static void test_no_final_newline(void) {
    const char *text = "WEBVTT\n\n00:00:00.000 --> 00:00:10.000\nLast";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0), "Last");
    tc_free(d);
}

/* A header-only file is valid and yields an entry with no chapters. */
static void test_header_only(void) {
    const char *text = "WEBVTT\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 0);
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Encoding                                                           */
/* ------------------------------------------------------------------ */

/* A UTF-8 BOM is consumed by the reference's StreamReader. */
static void test_utf8_bom(void) {
    const char *text = "\xEF\xBB\xBFWEBVTT\n\n00:00:00.000 --> 00:00:10.000\nA\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0), "A");
    tc_free(d);
}

/* The reference removes CR before splitting, so CRLF files work and a lone CR
 * is not a line break. */
static void test_crlf(void) {
    const char *text = "WEBVTT\r\n\r\n00:00:00.000 --> 00:00:10.000\r\nA\r\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0), "A");
    TC_CHECK_EQ_INT(chapter_time(d, 0), 0);
    tc_free(d);
}

/* Non-ASCII names are byte-transparent. */
static void test_utf8_names(void) {
    const char *text =
        "WEBVTT\n"
        "\n"
        "00:00:00.000 --> 00:00:10.000\n"
        "\xE5\xBA\x8F\xE7\xAB\xA0\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_STR(chapter_name(d, 0), "\xE5\xBA\x8F\xE7\xAB\xA0");
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Time code forms                                                    */
/* ------------------------------------------------------------------ */

/* The reference splits on "-->" and parses with TimeSpan.Parse, which accepts
 * no-fraction, comma and whitespace-padded forms. Only the start is kept. */
static void test_timecode_forms(void) {
    const char *texts[] = {
        "WEBVTT\n\n00:00:26 --> 00:00:30\nA\n",
        "WEBVTT\n\n00:00:26,500 --> 00:00:30,000\nA\n",
        "WEBVTT\n\n00 : 00 : 26.000 --> 00:00:30.000\nA\n",
        "WEBVTT\n\n0:0:26.000 --> 00:00:30.000\nA\n",
    };
    const int64_t want[] = {
        26000000000LL,
        26500000000LL,
        26000000000LL,
        26000000000LL,
    };
    for (size_t i = 0; i < sizeof(texts) / sizeof(texts[0]); i++) {
        tc_data *d = NULL;
        TC_CHECK_EQ_INT(parse_text(texts[i], &d), TC_OK);
        if (!d) {
            continue;
        }
        TC_CHECK_EQ_INT(chapter_time(d, 0), want[i]);
        tc_free(d);
    }
}

/* The split happens on every arrow, and each field is parsed; the second field
 * therefore fails the file. */
static void test_double_arrow_is_rejected(void) {
    const char *text =
        "WEBVTT\n"
        "\n"
        "00:00:00.000 --> 00:00:10.000 --> 00:00:20.000\n"
        "A\n";
    tc_data *d = NULL;
    TC_CHECK(parse_text(text, &d) != TC_OK);
    TC_CHECK(d == NULL);
    tc_free(d);
}

/* Hours are not capped at 24, unlike TimeSpan.Parse. */
static void test_large_hours(void) {
    const char *text = "WEBVTT\n\n25:00:00.000 --> 26:00:10.000\nA\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(chapter_time(d, 0), 90000000000LL);
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Error paths                                                        */
/* ------------------------------------------------------------------ */

static void test_invalid_header_is_rejected(void) {
    const char *cases[] = {
        "",                       /* empty */
        "not a vtt file\n",       /* no WEBVTT */
        "WEBVTTX\n",              /* the marker is in the first block only */
        "\nWEBVTT\n",             /* ... and the first block is empty here */
        "WEBVTT\n\n\n",           /* a block that is only a newline */
    };
    for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
        tc_data *d = NULL;
        tc_status st = parse_text(cases[i], &d);
        TC_CHECK(st != TC_OK);
        TC_CHECK(d == NULL);
        tc_free(d);
    }
}

/* A block whose time line is the last line has no name to take. */
static void test_time_line_without_name_is_rejected(void) {
    const char *text = "WEBVTT\n\n00:00:00.000 --> 00:00:10.000\n";
    tc_data *d = NULL;
    TC_CHECK(parse_text(text, &d) != TC_OK);
    TC_CHECK(d == NULL);
    tc_free(d);
}

static void test_bad_arguments(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(NULL, 0, TC_FMT_VTT, NULL, &d), TC_E_INVALID);
    TC_CHECK_EQ_INT(tc_parse_mem("x", 1, TC_FMT_VTT, NULL, NULL), TC_E_INVALID);
    TC_CHECK(d == NULL);

    /* A missing file is an I/O failure, not a format failure. */
    TC_CHECK_EQ_INT(tc_parse_file("/definitely/not/here.vtt", TC_FMT_VTT, &d), TC_E_IO);
    TC_CHECK(d == NULL);
}

/* ------------------------------------------------------------------ */
/* Round trip with the writer                                         */
/* ------------------------------------------------------------------ */

/* The writer emits cue end times, which the parser throws away; the round trip
 * therefore preserves names and start times, not end times. */
static void test_write_read_round_trip(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("vtt-00001.vtt", &d), TC_OK);
    if (!d) {
        return;
    }

    size_t need = 0;
    TC_CHECK_EQ_INT(tc_render(d, 0, TC_FMT_VTT, NULL, NULL, NULL, &need), TC_E_RANGE);
    char *buf = malloc(need);
    TC_CHECK(buf != NULL);
    if (buf) {
        size_t len = need;
        TC_CHECK_EQ_INT(tc_render(d, 0, TC_FMT_VTT, NULL, NULL, buf, &len), TC_OK);
        TC_CHECK(strncmp(buf, "WEBVTT", 6) == 0);

        tc_data *again = NULL;
        TC_CHECK_EQ_INT(tc_parse_mem(buf, len, TC_FMT_VTT, NULL, &again), TC_OK);
        if (again) {
            TC_CHECK_EQ_INT(tc_chapter_count(again, 0), 7);
            TC_CHECK_EQ_STR(chapter_name(again, 0), "Introduction");
            TC_CHECK_EQ_STR(chapter_name(again, 6), "The Colossus of Rhodes");
            TC_CHECK_EQ_INT(chapter_time(again, 6), 493000000000LL);
            tc_free(again);
        }
        free(buf);
    }
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Suite                                                              */
/* ------------------------------------------------------------------ */

TC_SUITE(vtt) {
    TC_CASE("reference_fixture");  test_reference_fixture();
    TC_CASE("end_time_ignored");   test_end_time_is_ignored();
    TC_CASE("missing_end_time");   test_missing_end_time_is_rejected();
    TC_CASE("bad_end_time");       test_bad_end_time_is_rejected();
    TC_CASE("cue_settings");       test_cue_settings_are_rejected();
    TC_CASE("cue_identifier");     test_cue_identifier_is_skipped();
    TC_CASE("multiline_text");     test_multiline_cue_text();
    TC_CASE("triple_newline");     test_triple_newline_block_is_rejected();
    TC_CASE("header_block");       test_header_block();
    TC_CASE("note_block");         test_note_block_is_rejected();
    TC_CASE("name_padding");       test_name_is_not_trimmed();
    TC_CASE("empty_name");         test_empty_name();
    TC_CASE("no_final_newline");   test_no_final_newline();
    TC_CASE("header_only");        test_header_only();
    TC_CASE("utf8_bom");           test_utf8_bom();
    TC_CASE("crlf");               test_crlf();
    TC_CASE("utf8_names");         test_utf8_names();
    TC_CASE("timecode_forms");     test_timecode_forms();
    TC_CASE("double_arrow");       test_double_arrow_is_rejected();
    TC_CASE("large_hours");        test_large_hours();
    TC_CASE("invalid_header");     test_invalid_header_is_rejected();
    TC_CASE("time_without_name");  test_time_line_without_name_is_rejected();
    TC_CASE("bad_arguments");      test_bad_arguments();
    TC_CASE("round_trip");         test_write_read_round_trip();
}
