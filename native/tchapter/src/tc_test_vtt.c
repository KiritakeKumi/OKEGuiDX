/*
 * WebVTT chapter parser tests (B11).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * vtt-00001.vtt is the reference implementation's own test asset
 * (TChapter.Test/Assets/VTT/00001.vtt). The remaining inputs are built in
 * memory to isolate the reference's behaviour, and every expected value was
 * produced by replaying VTTParser.GetChapterInfo with .NET Framework 4.8 - the
 * framework the test project actually targets - so these tests assert what the
 * reference does, not what WebVTT permits.
 *
 * The reference keeps a cue's start time and throws the end time away; the end
 * is parsed all the same, so a malformed or missing end time fails the file.
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

/* Same, for a buffer that is not NUL-terminated. */
static tc_status parse_text_len(const char *text, size_t len, tc_data **out) {
    return tc_parse_mem(text, len, TC_FMT_VTT, NULL, out);
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
    const char *texts[] = {
        "WEBVTT\n\n00:00:00.000 --> 00:00:26.000\nFirst\n\n00:00:30.000\nNo end\n",
        "WEBVTT\n\n00:00:30.000 -->\nNo end\n",
        "WEBVTT\n\n00:00:30.000\nNo arrow\n",
    };
    for (size_t i = 0; i < sizeof(texts) / sizeof(texts[0]); i++) {
        tc_data *d = NULL;
        TC_CHECK(parse_text(texts[i], &d) != TC_OK);
        TC_CHECK(d == NULL);
        tc_free(d);
    }
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

/* Three newlines between cues leave a single '\n' at the start of the next
 * block, which the reference skips while looking for the time line - so this
 * still parses. Four newlines split into a genuinely empty block, which has no
 * time line and fails. */
static void test_blank_line_blocks(void) {
    const char *three =
        "WEBVTT\n"
        "\n"
        "00:00:00.000 --> 00:00:10.000\n"
        "First\n"
        "\n"
        "\n"
        "00:00:10.000 --> 00:00:20.000\n"
        "Second\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(three, &d), TC_OK);
    if (d) {
        TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
        TC_CHECK_EQ_STR(chapter_name(d, 0), "First");
        TC_CHECK_EQ_STR(chapter_name(d, 1), "Second");
        tc_free(d);
    }

    const char *four =
        "WEBVTT\n"
        "\n"
        "00:00:00.000 --> 00:00:10.000\n"
        "First\n"
        "\n"
        "\n"
        "\n"
        "00:00:10.000 --> 00:00:20.000\n"
        "Second\n";
    d = NULL;
    TC_CHECK(parse_text(four, &d) != TC_OK);
    TC_CHECK(d == NULL);
    tc_free(d);
}

/* A file that ends with a blank line splits into a trailing empty block, which
 * the reference cannot turn into a cue and therefore rejects. This includes the
 * output of the library's own VTT writer, which always ends a cue with a blank
 * line. */
static void test_trailing_blank_line_is_rejected(void) {
    const char *texts[] = {
        "WEBVTT\n\n00:00:00.000 --> 00:00:10.000\nA\n\n",
        "WEBVTT\n\n00:00:00.000 --> 00:00:10.000\nA\n\n\n",
    };
    for (size_t i = 0; i < sizeof(texts) / sizeof(texts[0]); i++) {
        tc_data *d = NULL;
        TC_CHECK(parse_text(texts[i], &d) != TC_OK);
        TC_CHECK(d == NULL);
        tc_free(d);
    }
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

/* An empty name is possible when the time line is followed by a newline that
 * ends the file: the block then has a second, empty line. When the time line is
 * the block's last line instead, the reference throws. */
static void test_empty_name(void) {
    const char *with_newline = "WEBVTT\n\n00:00:00.000 --> 00:00:10.000\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(with_newline, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0), "");
    TC_CHECK_EQ_INT(chapter_time(d, 0), 0);
    tc_free(d);

    /* No trailing newline: the time line is the only line of the block. */
    const char *no_newline = "WEBVTT\n\n00:00:00.000 --> 00:00:10.000";
    d = NULL;
    TC_CHECK(parse_text(no_newline, &d) != TC_OK);
    TC_CHECK(d == NULL);
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

/* The reference removes CR before splitting, so CRLF files work. */
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

/* TimeSpan.Parse accepts a missing fraction, one-digit fields and surrounding
 * whitespace, and keeps fractions up to seven digits. Only the start is kept. */
static void test_timecode_forms(void) {
    const char *texts[] = {
        "WEBVTT\n\n00:00:26 --> 00:00:30\nA\n",
        "WEBVTT\n\n0:0:26.000 --> 00:00:30.000\nA\n",
        "WEBVTT\n\n 00:00:26.000 --> 00:00:30.000\nA\n",
        "WEBVTT\n\n00:00:26.000 --> 00:00:30.000 \nA\n",
        "WEBVTT\n\n00:00:26.1234567 --> 00:00:30.000\nA\n",
        "WEBVTT\n\n000:00:26.000 --> 00:00:30.000\nA\n",
    };
    const int64_t want[] = {
        26000000000LL,
        26000000000LL,
        26000000000LL,
        26000000000LL,
        26123456700LL,
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

/* TimeSpan.Parse is stricter than the regex path: a comma fraction, more than
 * seven fraction digits, and an hour at or above 24 all fail. */
static void test_strict_timecode_rejections(void) {
    const char *texts[] = {
        "WEBVTT\n\n00:00:26,500 --> 00:00:30,000\nA\n",
        "WEBVTT\n\n00:00:26.12345678 --> 00:00:30.000\nA\n",
        "WEBVTT\n\n24:00:00.000 --> 24:00:30.000\nA\n",
        "WEBVTT\n\n25:00:00.000 --> 25:00:10.000\nA\n",
        "WEBVTT\n\n00:60:00.000 --> 00:60:10.000\nA\n",
        "WEBVTT\n\n00:00:60.000 --> 00:00:70.000\nA\n",
        "WEBVTT\n\n00:00:26. --> 00:00:30.000\nA\n",
    };
    for (size_t i = 0; i < sizeof(texts) / sizeof(texts[0]); i++) {
        tc_data *d = NULL;
        tc_status st = parse_text(texts[i], &d);
        TC_CHECK(st != TC_OK);
        TC_CHECK(d == NULL);
        tc_free(d);
    }
}

/* A negative start time is legal for TimeSpan.Parse. */
static void test_negative_start(void) {
    const char *text = "WEBVTT\n\n-00:00:05.000 --> 00:00:10.000\nA\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(chapter_time(d, 0), -5000000000LL);
    tc_free(d);
}

/* The reference splits the time line on every "-->" and parses each field, so a
 * third field has to parse as well; extra arrows are not a format error. */
static void test_double_arrow_is_parsed(void) {
    const char *text =
        "WEBVTT\n"
        "\n"
        "00:00:00.000 --> 00:00:10.000 --> 00:00:20.000\n"
        "A\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_INT(chapter_time(d, 0), 0);
    tc_free(d);

    /* A malformed extra field fails the file all the same. */
    const char *bad =
        "WEBVTT\n"
        "\n"
        "00:00:00.000 --> 00:00:10.000 --> nonsense\n"
        "A\n";
    d = NULL;
    TC_CHECK(parse_text(bad, &d) != TC_OK);
    TC_CHECK(d == NULL);
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Error paths                                                        */
/* ------------------------------------------------------------------ */

static void test_invalid_header_is_rejected(void) {
    const char *cases[] = {
        "",                 /* empty */
        "not a vtt file\n", /* no WEBVTT */
        "\n\nWEBVTT\n",     /* the first block is empty */
        "WEBVTT\n\n\n",     /* a block that is only a newline */
        "WEBVTT\n\nno time line here\n",
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
    const char *texts[] = {
        "WEBVTT\n\n00:00:00.000 --> 00:00:10.000",
        "WEBVTT\n\n00:00:00.000 --> 00:00:10.000\n\n",
    };
    for (size_t i = 0; i < sizeof(texts) / sizeof(texts[0]); i++) {
        tc_data *d = NULL;
        TC_CHECK(parse_text(texts[i], &d) != TC_OK);
        TC_CHECK(d == NULL);
        tc_free(d);
    }
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
/* Writer interop                                                     */
/* ------------------------------------------------------------------ */

/* The VTT writer ends every cue - including the last - with a blank line, so its
 * output carries a trailing empty block. The reference parser rejects that
 * (Regex.Split produces an empty node which has no time line), so the writer's
 * bytes are not readable by a faithful port of VTTParser unless the final blank
 * line is removed. That is asserted here rather than worked around, because
 * changing either side would be a deviation from the reference. */
static void test_writer_output_needs_final_blank_line_removed(void) {
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
        TC_CHECK(len >= 4);
        /* The writer's trailing "\r\n\r\n". */
        TC_CHECK(memcmp(buf + len - 4, "\r\n\r\n", 4) == 0);

        tc_data *again = NULL;
        TC_CHECK(parse_text_len(buf, len, &again) != TC_OK);
        TC_CHECK(again == NULL);
        tc_free(again);

        /* Dropping the last line terminator leaves the same shape as the
         * reference's own fixture, which parses. */
        again = NULL;
        TC_CHECK_EQ_INT(tc_parse_mem(buf, len - 2, TC_FMT_VTT, NULL, &again), TC_OK);
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
    TC_CASE("blank_line_blocks");  test_blank_line_blocks();
    TC_CASE("trailing_blank");     test_trailing_blank_line_is_rejected();
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
    TC_CASE("timecode_strict");    test_strict_timecode_rejections();
    TC_CASE("negative_start");     test_negative_start();
    TC_CASE("double_arrow");       test_double_arrow_is_parsed();
    TC_CASE("invalid_header");     test_invalid_header_is_rejected();
    TC_CASE("time_without_name");  test_time_line_without_name_is_rejected();
    TC_CASE("bad_arguments");      test_bad_arguments();
    TC_CASE("writer_interop");     test_writer_output_needs_final_blank_line_removed();
}
