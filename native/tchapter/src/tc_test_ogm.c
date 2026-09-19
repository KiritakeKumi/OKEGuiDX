/*
 * Tests for the OGM chapter parser (B10).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * The fixture 00001.txt is the reference implementation's own test asset
 * (TChapter.Test/Assets/OGM/00001.txt); the expected times below are derived
 * from it by hand. The other fixtures are small inputs that isolate the states
 * of the reference state machine.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_test.h"

/* ------------------------------------------------------------------ */
/* Helpers                                                            */
/* ------------------------------------------------------------------ */

static tc_status parse_text(const char *text, tc_data **out) {
    return tc_parse_mem(text, strlen(text), TC_FMT_OGM, NULL, out);
}

static tc_status parse_fixture(const char *name, tc_data **out) {
    char *buf = NULL;
    size_t len = 0;
    tc_status st = tc_test_read_data(name, &buf, &len);
    if (st != TC_OK) {
        return st;
    }
    st = tc_parse_mem(buf, len, TC_FMT_OGM, NULL, out);
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
/* Happy paths                                                        */
/* ------------------------------------------------------------------ */

static void test_reference_fixture(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("OGM/00001.txt", &d), TC_OK);
    if (!d) {
        return;
    }

    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 13);

    /* The fixture has a UTF-8 BOM and three leading whitespace-only lines. */
    TC_CHECK_EQ_STR(chapter_name(d, 0), "Chapter 01");
    TC_CHECK_EQ_INT(chapter_time(d, 0), 0);
    TC_CHECK_EQ_STR(chapter_name(d, 1), "Chapter 02");
    TC_CHECK_EQ_INT(chapter_time(d, 1), 41041000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 2), "Chapter 03");
    TC_CHECK_EQ_INT(chapter_time(d, 2), 132799000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 3), "Chapter 04");
    TC_CHECK_EQ_INT(chapter_time(d, 3), 216258000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 4), "Chapter 05");
    TC_CHECK_EQ_INT(chapter_time(d, 4), 277944000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 5), "Chapter 06");
    TC_CHECK_EQ_INT(chapter_time(d, 5), 344928000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 6), "Chapter 07");
    TC_CHECK_EQ_INT(chapter_time(d, 6), 539247000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 7), "Chapter 08");
    TC_CHECK_EQ_INT(chapter_time(d, 7), 719802000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 8), "Chapter 09");
    TC_CHECK_EQ_INT(chapter_time(d, 8), 805263000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 9), "Chapter 10");
    TC_CHECK_EQ_INT(chapter_time(d, 9), 885468000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 10), "Chapter 11");
    TC_CHECK_EQ_INT(chapter_time(d, 10), 1054887000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 11), "Chapter 12");
    TC_CHECK_EQ_INT(chapter_time(d, 11), 1161911000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 12), "Chapter 13");
    TC_CHECK_EQ_INT(chapter_time(d, 12), 1361860000000LL);

    /* The first chapter is the base, so the entry duration is the last time. */
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_INT(ei.duration_ns, 1361860000000LL);
    TC_CHECK_EQ_INT(ei.chapter_count, 13);

    /* This format has no frame information. */
    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    TC_CHECK_EQ_INT(c.frames, -1);

    tc_free(d);
}

static void test_basic(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("OGM/basic.txt", &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 3);
    TC_CHECK_EQ_STR(chapter_name(d, 0), "Chapter 01");
    TC_CHECK_EQ_INT(chapter_time(d, 0), 0);
    TC_CHECK_EQ_INT(chapter_time(d, 1), 10000000000LL);
    TC_CHECK_EQ_INT(chapter_time(d, 2), 20000000000LL);
    tc_free(d);
}

static void test_single_chapter(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("OGM/single.txt", &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0), "Chapter 01");
    TC_CHECK_EQ_INT(chapter_time(d, 0), 0);

    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_INT(ei.duration_ns, 0);
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Time code variants                                                 */
/* ------------------------------------------------------------------ */

static void test_comma_separator(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("OGM/comma.txt", &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    TC_CHECK_EQ_STR(chapter_name(d, 0), "Chapter 01");
    TC_CHECK_EQ_INT(chapter_time(d, 0), 0);
    TC_CHECK_EQ_STR(chapter_name(d, 1), "Chapter 02");
    TC_CHECK_EQ_INT(chapter_time(d, 1), 41041000000LL);
    tc_free(d);
}

static void test_fraction_digits(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("OGM/fractions.txt", &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 3);
    TC_CHECK_EQ_INT(chapter_time(d, 0), 0);
    /* Six fractional digits are microseconds. */
    TC_CHECK_EQ_INT(chapter_time(d, 1), 1234567000LL);
    /* The reference's pattern needs at least three fraction digits, so ".5"
     * has no match at all; ToTimeSpan then yields zero and the chapter is kept
     * at the base time. */
    TC_CHECK_EQ_STR(chapter_name(d, 2), "Chapter 03");
    TC_CHECK_EQ_INT(chapter_time(d, 2), 0);
    tc_free(d);
}

/* The reference converts the fraction through double arithmetic, so a fraction
 * that is not exactly representable is truncated to 100 ns ticks. These values
 * are what the real implementation produces. */
static void test_fraction_double_truncation(void) {
    const char *texts[] = {
        "CHAPTER01=00:00:00.000\nCHAPTER01NAME=a\nCHAPTER02=00:00:00.1234567890\nCHAPTER02NAME=b\n",
        "CHAPTER01=00:00:00.000\nCHAPTER01NAME=a\nCHAPTER02=00:00:00.12345678\nCHAPTER02NAME=b\n",
        "CHAPTER01=00:00:00.000\nCHAPTER01NAME=a\nCHAPTER02=00:00:00.12345\nCHAPTER02NAME=b\n",
    };
    const int64_t want[] = {123456700, 123456700, 123450000};
    for (size_t i = 0; i < sizeof(texts) / sizeof(texts[0]); i++) {
        tc_data *d = NULL;
        TC_CHECK_EQ_INT(parse_text(texts[i], &d), TC_OK);
        if (!d) {
            continue;
        }
        TC_CHECK_EQ_INT(chapter_time(d, 1), want[i]);
        tc_free(d);
    }
}

/* Minutes and seconds beyond 59 are carried by TimeSpan(int,int,int) rather
 * than rejected. */
static void test_out_of_range_components(void) {
    const char *text =
        "CHAPTER01=00:00:00.000\n"
        "CHAPTER01NAME=Chapter 01\n"
        "CHAPTER02=00:00:61.000\n"
        "CHAPTER02NAME=Chapter 02\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(chapter_time(d, 1), 61000000000LL);
    tc_free(d);

    text =
        "CHAPTER01=00:00:00.000\n"
        "CHAPTER01NAME=Chapter 01\n"
        "CHAPTER02=00:99:00.000\n"
        "CHAPTER02NAME=Chapter 02\n";
    d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(chapter_time(d, 1), 5940000000000LL);
    tc_free(d);
}

/* A component that does not fit the reference's Int32 makes it throw, so the
 * parse fails instead of wrapping. */
static void test_component_overflow_is_rejected(void) {
    const char *texts[] = {
        "CHAPTER01=99999999999999999999:00:00.000\nCHAPTER01NAME=Chapter 01\n",
        "CHAPTER01=00:00:00.000\nCHAPTER01NAME=Chapter 01\nCHAPTER02=1234567890:00:00.000\nCHAPTER02NAME=Chapter 02\n",
    };
    for (size_t i = 0; i < sizeof(texts) / sizeof(texts[0]); i++) {
        tc_data *d = NULL;
        TC_CHECK(parse_text(texts[i], &d) != TC_OK);
        TC_CHECK(d == NULL);
        tc_free(d);
    }
}

static void test_leading_whitespace_and_padding(void) {
    const char *text =
        "   \r\n"
        "\t\r\n"
        "  CHAPTER01  =  00:00:00.000\r\n"
        "CHAPTER01NAME=Chapter 01 \r\n"
        "  CHAPTER2 = 00:00:05.500 \r\n"
        "CHAPTER2NAME=Chapter 02 \r\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    /* Only the file's last line is affected by the reference's trailing trim,
     * so the first name keeps its trailing space. */
    TC_CHECK_EQ_STR(chapter_name(d, 0), "Chapter 01 ");
    TC_CHECK_EQ_INT(chapter_time(d, 1), 5500000000LL);
    /* Trailing spaces in a name line survive; only '\r' is trimmed. */
    TC_CHECK_EQ_STR(chapter_name(d, 1), "Chapter 02");
    tc_free(d);
}

static void test_no_final_newline(void) {
    const char *text = "CHAPTER01=00:00:00.000\nCHAPTER01NAME=Chapter 01";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0), "Chapter 01");
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* State machine paths                                                */
/* ------------------------------------------------------------------ */

/* The first chapter becomes the base, so later times are relative to it. */
static void test_times_are_rebased_on_first_chapter(void) {
    const char *text =
        "CHAPTER01=00:01:00.000\n"
        "CHAPTER01NAME=Chapter 01\n"
        "CHAPTER02=00:02:00.000\n"
        "CHAPTER02NAME=Chapter 02\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    TC_CHECK_EQ_INT(chapter_time(d, 0), 0);
    TC_CHECK_EQ_INT(chapter_time(d, 1), 60000000000LL);
    tc_free(d);
}

/* A missing NAME line stops the scan and keeps what was parsed. */
static void test_missing_name_stops_scan(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("OGM/missing_name.txt", &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0), "Chapter 01");

    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_INT(ei.duration_ns, 0);
    tc_free(d);
}

/* A stray line in LTimeCode also stops the scan; the chapters before it are
 * kept, and the ones after it are never reached. */
static void test_stray_line_stops_scan(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("OGM/truncated.txt", &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0), "Chapter 01");
    tc_free(d);
}

/* Blank lines are skipped in both states. */
static void test_blank_lines_are_skipped(void) {
    const char *text =
        "CHAPTER01=00:00:00.000\n"
        "\n"
        "CHAPTER01NAME=Chapter 01\n"
        "\n"
        "\n"
        "CHAPTER02=00:00:10.000\n"
        "\n"
        "CHAPTER02NAME=Chapter 02\n"
        "\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    tc_free(d);
}

/* A NAME line may carry an empty name. */
static void test_empty_chapter_name(void) {
    const char *text =
        "CHAPTER01=00:00:00.000\n"
        "CHAPTER01NAME=\n"
        "CHAPTER02=00:00:10.000\n"
        "CHAPTER02NAME=Chapter 02\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    TC_CHECK_EQ_STR(chapter_name(d, 0), "");
    TC_CHECK_EQ_STR(chapter_name(d, 1), "Chapter 02");
    tc_free(d);
}

/* A NAME line without a preceding time line is not accepted: the state machine
 * is in LTimeCode and stops. */
static void test_name_line_in_timecode_state_stops(void) {
    const char *text =
        "CHAPTER01=00:00:00.000\n"
        "CHAPTER01NAME=Chapter 01\n"
        "CHAPTER02NAME=Chapter 02\n"
        "CHAPTER02=00:00:10.000\n"
        "CHAPTER02NAME=Chapter 02\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    tc_free(d);
}

/* A CHAPTER line with an unparseable time code keeps the state machine going:
 * the reference's RTimeCodeLine only checks the shape, and ToTimeSpan returns
 * zero for a text it cannot read. */
static void test_unparseable_time_becomes_zero(void) {
    const char *text =
        "CHAPTER01=not a time at all\n"
        "CHAPTER01NAME=Chapter 01\n"
        "CHAPTER02=00:00:10.000\n"
        "CHAPTER02NAME=Chapter 02\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(text, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    TC_CHECK_EQ_INT(chapter_time(d, 0), 0);
    TC_CHECK_EQ_INT(chapter_time(d, 1), 10000000000LL);
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Error paths                                                        */
/* ------------------------------------------------------------------ */

static void test_first_line_must_be_time(void) {
    const char *texts[] = {
        "not a chapter file\n",
        "CHAPTER01NAME=Chapter 01\n",
        "",
        "   \n\t\n",
        "CHAPTER =00:00:00.000\nCHAPTER NAME=Chapter 01\n",
        "CHAPTERX=00:00:00.000\nCHAPTERXNAME=Chapter 01\n",
    };
    for (size_t i = 0; i < sizeof(texts) / sizeof(texts[0]); i++) {
        tc_data *d = NULL;
        tc_status st = parse_text(texts[i], &d);
        TC_CHECK(st != TC_OK);
        TC_CHECK(d == NULL);
        tc_free(d);
    }
}

static void test_empty_input_is_rejected(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem("", 0, TC_FMT_OGM, NULL, &d), TC_E_FORMAT);
    TC_CHECK(d == NULL);
}

/* A time line followed by the end of the file has no chapter to return, so the
 * parse fails rather than producing an empty entry. */
static void test_time_line_without_name_is_rejected(void) {
    const char *text = "CHAPTER01=00:00:00.000\n";
    tc_data *d = NULL;
    TC_CHECK(parse_text(text, &d) != TC_OK);
    TC_CHECK(d == NULL);
    tc_free(d);
}

/* Absurdly long digit runs do not fit the reference's Int32 and make it throw,
 * so they are rejected rather than wrapped. */
static void test_long_digit_runs_do_not_overflow(void) {
    const char *text =
        "CHAPTER01=99999999999999999999:00:00.000\n"
        "CHAPTER01NAME=Chapter 01\n"
        "CHAPTER02=00:00:10.000\n"
        "CHAPTER02NAME=Chapter 02\n";
    tc_data *d = NULL;
    TC_CHECK(parse_text(text, &d) != TC_OK);
    TC_CHECK(d == NULL);
    tc_free(d);
}

static void test_bad_arguments(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(NULL, 0, TC_FMT_OGM, NULL, &d), TC_E_INVALID);
    TC_CHECK_EQ_INT(tc_parse_mem("x", 1, TC_FMT_OGM, NULL, NULL), TC_E_INVALID);
}

/* ------------------------------------------------------------------ */
/* Round trip with the writer                                         */
/* ------------------------------------------------------------------ */

static void test_write_read_round_trip(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("OGM/basic.txt", &d), TC_OK);
    if (!d) {
        return;
    }

    size_t need = 0;
    TC_CHECK_EQ_INT(tc_render(d, 0, TC_FMT_OGM, NULL, NULL, NULL, &need), TC_E_RANGE);
    char *buf = malloc(need);
    TC_CHECK(buf != NULL);
    if (buf) {
        size_t len = need;
        TC_CHECK_EQ_INT(tc_render(d, 0, TC_FMT_OGM, NULL, NULL, buf, &len), TC_OK);

        tc_data *again = NULL;
        TC_CHECK_EQ_INT(tc_parse_mem(buf, len, TC_FMT_OGM, NULL, &again), TC_OK);
        if (again) {
            TC_CHECK_EQ_INT(tc_chapter_count(again, 0), 3);
            TC_CHECK_EQ_STR(chapter_name(again, 0), "Chapter 01");
            TC_CHECK_EQ_STR(chapter_name(again, 2), "Chapter 03");
            TC_CHECK_EQ_INT(chapter_time(again, 2), 20000000000LL);
            tc_free(again);
        }
        free(buf);
    }
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Suite                                                              */
/* ------------------------------------------------------------------ */

TC_SUITE(ogm) {
    TC_CASE("reference_fixture");   test_reference_fixture();
    TC_CASE("basic");               test_basic();
    TC_CASE("single_chapter");      test_single_chapter();
    TC_CASE("comma_separator");     test_comma_separator();
    TC_CASE("fraction_digits");     test_fraction_digits();
    TC_CASE("fraction_truncation"); test_fraction_double_truncation();
    TC_CASE("component_range");     test_out_of_range_components();
    TC_CASE("component_overflow");  test_component_overflow_is_rejected();
    TC_CASE("whitespace_padding");  test_leading_whitespace_and_padding();
    TC_CASE("no_final_newline");    test_no_final_newline();
    TC_CASE("rebase");              test_times_are_rebased_on_first_chapter();
    TC_CASE("missing_name");        test_missing_name_stops_scan();
    TC_CASE("stray_line");          test_stray_line_stops_scan();
    TC_CASE("blank_lines");         test_blank_lines_are_skipped();
    TC_CASE("empty_name");          test_empty_chapter_name();
    TC_CASE("name_without_time");   test_name_line_in_timecode_state_stops();
    TC_CASE("unparseable_time");    test_unparseable_time_becomes_zero();
    TC_CASE("first_line_error");    test_first_line_must_be_time();
    TC_CASE("empty_input");         test_empty_input_is_rejected();
    TC_CASE("time_without_name");   test_time_line_without_name_is_rejected();
    TC_CASE("long_digits");         test_long_digit_runs_do_not_overflow();
    TC_CASE("bad_arguments");       test_bad_arguments();
    TC_CASE("round_trip");          test_write_read_round_trip();
}
