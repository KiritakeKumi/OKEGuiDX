/*
 * CUE parser tests (B4).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * The fixtures in testdata/ are byte-exact and their expectations were produced
 * by running the original TChapter.Parsing.CUEParser (net6.0) over them, so the
 * assertions below pin the reference behaviour rather than an interpretation of
 * it. cue-example.cue is the reference project's own asset
 * (TChapter.Test/Assets/CUE/example-cue-sheet.cue), copied verbatim.
 *
 * The encoding cases document a property of the reference that is easy to
 * assume otherwise: StreamReader(stream, true) only honours byte order marks,
 * everything else is UTF-8 with replacement characters. It does *not* detect
 * Shift_JIS or GBK, so cue-sjis.cue and cue-gbk.cue lose their non-ASCII text
 * in the original too. The tests assert that loss on purpose.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_test.h"

/* ------------------------------------------------------------------ */
/* Helpers                                                            */
/* ------------------------------------------------------------------ */

/* Parses a testdata fixture and reports the first chapter, so the failure
 * messages carry the parser's own error text. */
static tc_data *parse_fixture(const char *name) {
    char path[512];
    tc_test_data_path(path, sizeof(path), name);
    tc_data *d = NULL;
    tc_status st = tc_parse_file(path, TC_FMT_CUE, &d);
    if (st != TC_OK) {
        fprintf(stderr, "  parse error for %s: %s\n", name, tc_last_error());
        return NULL;
    }
    return d;
}

static void expect_chapter(const tc_data *d, size_t index, const char *name, int64_t ms) {
    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, index, &c), TC_OK);
    TC_CHECK_EQ_STR(c.name, name);
    TC_CHECK_EQ_INT(c.time_ns, ms * 1000000LL);
    /* CUE sheets carry no frame numbers; the field stays unknown. */
    TC_CHECK_EQ_INT(c.frames, -1);
}

/* ------------------------------------------------------------------ */
/* The reference fixture                                              */
/* ------------------------------------------------------------------ */

/* TChapter.Test's own example, with the 19 timestamps its CUEParserTest
 * asserts (0, 169.76, 354.307, ... 4277.707 s). */
static void test_example_cue_sheet(void) {
    tc_data *d = parse_fixture("cue-example.cue");
    TC_CHECK(d != NULL);
    if (!d) {
        return;
    }

    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.title, "Back To Mine");
    TC_CHECK_EQ_STR(ei.source, "Orbital - Back To Mine.mp3");
    TC_CHECK_EQ_INT(ei.chapter_count, 19);
    /* The reference sets the duration to the last chapter's time. */
    TC_CHECK_EQ_INT(ei.duration_ns, 4277707000000LL);
    /* CUE has no frame rate of its own; the ABI keeps the neutral default. */
    TC_CHECK_EQ_INT(ei.fps_num, 0);
    TC_CHECK_EQ_INT(ei.fps_den, 1);

    static const int64_t expected_ms[] = {
        0, 169760, 354307, 594773, 761000, 915787, 1148120, 1368533, 1558627,
        1690920, 1946000, 2305307, 2579120, 2906547, 3270013, 3456507,
        3652667, 3950387, 4277707,
    };
    for (size_t i = 0; i < sizeof(expected_ms) / sizeof(expected_ms[0]); i++) {
        tc_chapter_t c;
        TC_CHECK_EQ_INT(tc_chapter_at(d, 0, i, &c), TC_OK);
        TC_CHECK_EQ_INT(c.time_ns, expected_ms[i] * 1000000LL);
    }

    /* The file-level PERFORMER is not used; each track's own PERFORMER is
     * appended to the track title. */
    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    TC_CHECK_EQ_STR(c.name, "John Barry & His Orchestra - The Knack [Orbital]");

    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Time code format (MM:SS:FF)                                        */
/* ------------------------------------------------------------------ */

/* CUE timestamps count 1/75 s frames, not milliseconds. The expectations come
 * from the reference: millis = round(FF * 1000 / 75). */
static void test_frame_timestamps(void) {
    tc_data *d = parse_fixture("cue-multitrack.cue");
    TC_CHECK(d != NULL);
    if (!d) {
        return;
    }

    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.title, "Biao Ti");
    TC_CHECK_EQ_STR(ei.source, "yin yue.wav");
    TC_CHECK_EQ_INT(ei.chapter_count, 3);
    TC_CHECK_EQ_INT(ei.duration_ns, 120987000000LL);

    /* INDEX 00 00:00:00 is a pre-gap and must not create a chapter. */
    expect_chapter(d, 0, "Kai Chang [Ge Shou]", 0);
    /* 01:23:45 -> 83 s + round(45 * 1000 / 75) = 83600 ms. */
    expect_chapter(d, 1, "Dian Ying", 83600);
    /* 02:00:74 -> 120 s + round(74 * 1000 / 75) = 120987 ms. */
    expect_chapter(d, 2, "Wei Sheng", 120987);
    tc_free(d);
}

/* A synthetic sweep of the frame field, so every rounding step is pinned
 * against the reference formula. */
static void test_frame_field_rounding(void) {
    static const struct {
        int frame;
        int64_t ms;
    } cases[] = {
        {0, 0}, {1, 13}, {2, 27}, {23, 307}, {37, 493}, {38, 507},
        {45, 600}, {57, 760}, {74, 987}, {75, 1000}, {99, 1320},
    };
    for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
        char text[256];
        int n = snprintf(text, sizeof(text),
                         "FILE \"a.wav\" WAVE\r\n  TRACK 01 AUDIO\r\n"
                         "    INDEX 01 00:00:%02d\r\n",
                         cases[i].frame);
        TC_CHECK(n > 0);
        tc_data *d = NULL;
        TC_CHECK_EQ_INT(tc_parse_mem(text, (size_t)n, TC_FMT_CUE, "t.cue", &d), TC_OK);
        if (!d) {
            continue;
        }
        tc_chapter_t c;
        TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
        TC_CHECK_EQ_INT(c.time_ns, cases[i].ms * 1000000LL);
        tc_free(d);
    }
}

/* ------------------------------------------------------------------ */
/* Encoding                                                           */
/* ------------------------------------------------------------------ */

static void test_utf8_without_bom(void) {
    tc_data *d = parse_fixture("cue-utf8.cue");
    TC_CHECK(d != NULL);
    if (!d) {
        return;
    }
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.title, "\xE4\xB8\xAD\xE6\x96\x87\xE6\xA0\x87\xE9\xA2\x98");
    TC_CHECK_EQ_STR(ei.source, "\xE9\x9F\xB3\xE4\xB9\x90.wav");
    TC_CHECK_EQ_INT(ei.chapter_count, 2);
    TC_CHECK_EQ_INT(ei.duration_ns, 30493000000LL);
    expect_chapter(d, 0, "\xE5\xBC\x80\xE5\x9C\xBA", 0);
    expect_chapter(d, 1, "\xE7\xBB\x93\xE5\xB0\xBE", 30493);
    tc_free(d);
}

static void test_utf8_with_bom(void) {
    tc_data *d = parse_fixture("cue-utf8-bom.cue");
    TC_CHECK(d != NULL);
    if (!d) {
        return;
    }
    /* The BOM must not leak into the title. */
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.title, "\xE4\xB8\xAD\xE6\x96\x87\xE6\xA0\x87\xE9\xA2\x98");
    TC_CHECK_EQ_INT(ei.chapter_count, 2);
    expect_chapter(d, 1, "\xE7\xBB\x93\xE5\xB0\xBE", 30493);
    tc_free(d);
}

/* UTF-16LE with a BOM is one of the few encodings StreamReader does detect. */
static void test_utf16le_with_bom(void) {
    tc_data *d = parse_fixture("cue-utf16le.cue");
    TC_CHECK(d != NULL);
    if (!d) {
        return;
    }
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.title, "\xE4\xB8\xAD\xE6\x96\x87");
    TC_CHECK_EQ_STR(ei.source, "a.wav");
    TC_CHECK_EQ_INT(ei.chapter_count, 1);
    expect_chapter(d, 0, "\xE5\xBC\x80\xE5\xB0\xBA", 1000);
    tc_free(d);
}

/* Shift_JIS and GBK are *not* detected by the reference: it decodes them as
 * UTF-8 and every multi-byte character becomes U+FFFD. Pinning that here keeps
 * the port honest about what the original does. */
static void test_sjis_and_gbk_are_replacement_characters(void) {
    static const char *const files[] = {"cue-sjis.cue", "cue-gbk.cue"};
    for (size_t i = 0; i < sizeof(files) / sizeof(files[0]); i++) {
        tc_data *d = parse_fixture(files[i]);
        TC_CHECK(d != NULL);
        if (!d) {
            continue;
        }
        tc_entry_info_t ei;
        TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
        TC_CHECK(ei.title[0] != '\0');
        /* Only U+FFFD code points (EF BF BD) may appear in the title. */
        for (const unsigned char *p = (const unsigned char *)ei.title; *p;) {
            if (p[0] == 0xEF && p[1] == 0xBF && p[2] == 0xBD) {
                p += 3;
                continue;
            }
            TC_CHECK(*p < 0x80);
            p++;
        }
        TC_CHECK(strstr(ei.title, "\xEF\xBF\xBD") != NULL);
        TC_CHECK_EQ_INT(ei.chapter_count, 2);
        tc_free(d);
    }
}

/* The decoder is exercised directly, because the fixtures only cover whole
 * files and the replacement rules are the part most likely to drift. */
static void test_utf8_replacement_rules(void) {
    static const struct {
        const char *bytes;
        size_t len;
        const char *want; /* UTF-8 */
    } cases[] = {
        {"A", 1, "A"},
        {"\xC2\x80", 2, "\xC2\x80"},
        {"\xDF\xBF", 2, "\xDF\xBF"},
        {"\xE0\xA0\x80", 3, "\xE0\xA0\x80"},
        {"\xED\x9F\xBF", 3, "\xED\x9F\xBF"},
        {"\xEE\x80\x80", 3, "\xEE\x80\x80"},
        {"\xF0\x90\x80\x80", 4, "\xF0\x90\x80\x80"},
        {"\xF4\x8F\xBF\xBF", 4, "\xF4\x8F\xBF\xBF"},
        /* Overlong and out-of-range forms. */
        {"\xC0\x80", 2, "\xEF\xBF\xBD\xEF\xBF\xBD"},
        {"\xC1\xBF", 2, "\xEF\xBF\xBD\xEF\xBF\xBD"},
        {"\xE0\x80\x80", 3, "\xEF\xBF\xBD\xEF\xBF\xBD\xEF\xBF\xBD"},
        {"\xF0\x80\x80\x80", 4, "\xEF\xBF\xBD\xEF\xBF\xBD\xEF\xBF\xBD\xEF\xBF\xBD"},
        {"\xF4\x90\x80\x80", 4, "\xEF\xBF\xBD\xEF\xBF\xBD\xEF\xBF\xBD\xEF\xBF\xBD"},
        {"\xF5\x80\x80\x80", 4, "\xEF\xBF\xBD\xEF\xBF\xBD\xEF\xBF\xBD\xEF\xBF\xBD"},
        /* Surrogate range. */
        {"\xED\xA0\x80", 3, "\xEF\xBF\xBD\xEF\xBF\xBD\xEF\xBF\xBD"},
        /* A lone continuation byte and a stray lead byte. */
        {"\x80", 1, "\xEF\xBF\xBD"},
        {"\xFF", 1, "\xEF\xBF\xBD"},
        /* A truncated sequence consumes the maximal subpart. */
        {"\xE1\x80", 2, "\xEF\xBF\xBD"},
        {"\xE1\x80\x80", 3, "\xE1\x80\x80"},
        /* A valid sequence followed by junk keeps the valid part. */
        {"\xE4\xBD\xA0\x80", 4, "\xE4\xBD\xA0\xEF\xBF\xBD"},
    };
    for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
        char text[256];
        int n = snprintf(text, sizeof(text),
                         "FILE \"a.wav\" WAVE\r\n  TRACK 01 AUDIO\r\n"
                         "    TITLE \"%s\"\r\n    INDEX 01 00:00:00\r\n",
                         cases[i].bytes);
        TC_CHECK(n > 0);
        tc_data *d = NULL;
        TC_CHECK_EQ_INT(tc_parse_mem(text, (size_t)n, TC_FMT_CUE, "t.cue", &d), TC_OK);
        if (!d) {
            continue;
        }
        tc_chapter_t c;
        TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
        TC_CHECK_EQ_STR(c.name, cases[i].want);
        tc_free(d);
    }
}

/* ------------------------------------------------------------------ */
/* Structure and ordering                                             */
/* ------------------------------------------------------------------ */

static void test_minimal_sheet(void) {
    tc_data *d = parse_fixture("cue-minimal.cue");
    TC_CHECK(d != NULL);
    if (!d) {
        return;
    }
    /* TITLE is optional; the ABI reports an empty string rather than NULL. */
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.title, "");
    TC_CHECK_EQ_STR(ei.source, "a.wav");
    TC_CHECK_EQ_INT(ei.chapter_count, 1);
    TC_CHECK_EQ_INT(ei.duration_ns, 0);
    tc_free(d);
}

/* The reference sorts the chapters by track number before returning them. */
static void test_tracks_are_sorted_by_number(void) {
    tc_data *d = parse_fixture("cue-unsorted.cue");
    TC_CHECK(d != NULL);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 3);
    expect_chapter(d, 0, "first", 0);
    expect_chapter(d, 1, "second", 10000);
    expect_chapter(d, 2, "third", 20000);
    tc_free(d);
}

/* A track whose INDEX 01 never appears is not a chapter in the reference. */
static void test_track_without_index_is_dropped(void) {
    tc_data *d = parse_fixture("cue-dropped.cue");
    TC_CHECK(d != NULL);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    expect_chapter(d, 0, "kept", 0);
    tc_free(d);
}

/* The reference splits on '\n' only, so a file with no trailing newline still
 * has its last line parsed. */
static void test_missing_final_newline(void) {
    tc_data *d = parse_fixture("cue-noeol.cue");
    TC_CHECK(d != NULL);
    if (!d) {
        return;
    }
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.title, "t");
    TC_CHECK_EQ_INT(ei.chapter_count, 1);
    expect_chapter(d, 0, "one", 1000);
    tc_free(d);
}

/* PERFORMER appends " [name]" to the track name; with no preceding TITLE the
 * name becomes just the bracketed part. */
static void test_performer_appends_to_name(void) {
    const char *text =
        "FILE \"a.wav\" WAVE\r\n"
        "  TRACK 01 AUDIO\r\n"
        "    PERFORMER \"Solo\"\r\n"
        "    INDEX 01 00:00:00\r\n"
        "  TRACK 02 AUDIO\r\n"
        "    TITLE \"Song\"\r\n"
        "    PERFORMER \"One\"\r\n"
        "    PERFORMER \"Two\"\r\n"
        "    INDEX 01 00:01:00\r\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(text, strlen(text), TC_FMT_CUE, "t.cue", &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    expect_chapter(d, 0, " [Solo]", 0);
    expect_chapter(d, 1, "Song [One] [Two]", 1000);
    tc_free(d);
}

/* The capture is greedy, so quotes inside the value are kept. */
static void test_greedy_quoted_capture(void) {
    const char *text =
        "TITLE \"A\" \"B\"\r\n"
        "FILE \"a.wav\" WAVE\r\n"
        "  TRACK 01 AUDIO\r\n"
        "    TITLE \"A\" \"B\"\r\n"
        "    INDEX 01 00:00:00\r\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(text, strlen(text), TC_FMT_CUE, "t.cue", &d), TC_OK);
    if (!d) {
        return;
    }
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.title, "A\" \"B");
    expect_chapter(d, 0, "A\" \"B", 0);
    tc_free(d);
}

/* An empty quoted value does not match ".+", so it is ignored. */
static void test_empty_quoted_value_is_ignored(void) {
    const char *text =
        "TITLE \"\"\r\n"
        "FILE \"a.wav\" WAVE\r\n"
        "  TRACK 01 AUDIO\r\n"
        "    TITLE \"\"\r\n"
        "    INDEX 01 00:00:00\r\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(text, strlen(text), TC_FMT_CUE, "t.cue", &d), TC_OK);
    if (!d) {
        return;
    }
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.title, "");
    /* The track keeps the empty default name. */
    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    TC_CHECK_EQ_STR(c.name, "");
    tc_free(d);
}

/* The state machine ends at the first blank line after FILE. */
static void test_blank_line_ends_the_parse(void) {
    const char *text =
        "FILE \"a.wav\" WAVE\r\n"
        "  TRACK 01 AUDIO\r\n"
        "    TITLE \"one\"\r\n"
        "    INDEX 01 00:00:00\r\n"
        "\r\n"
        "  TRACK 02 AUDIO\r\n"
        "    TITLE \"after the blank\"\r\n"
        "    INDEX 01 00:05:00\r\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(text, strlen(text), TC_FMT_CUE, "t.cue", &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    expect_chapter(d, 0, "one", 0);
    tc_free(d);
}

/* Lines before the FILE statement may carry the disc title; TITLE lines after
 * it belong to tracks. */
static void test_title_before_file_is_the_disc_title(void) {
    const char *text =
        "PERFORMER \"Disc Artist\"\r\n"
        "TITLE \"Disc Title\"\r\n"
        "FILE \"a.wav\" WAVE\r\n"
        "  TRACK 01 AUDIO\r\n"
        "    TITLE \"Track Title\"\r\n"
        "    INDEX 01 00:00:00\r\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(text, strlen(text), TC_FMT_CUE, "t.cue", &d), TC_OK);
    if (!d) {
        return;
    }
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.title, "Disc Title");
    expect_chapter(d, 0, "Track Title", 0);
    tc_free(d);
}

/* Every FILE keyword in the list is recognised, and the capture runs to the
 * last quote that is followed by one of them. */
static void test_file_keywords(void) {
    static const char *const types[] = {"WAVE", "MP3", "AIFF", "BINARY", "MOTOROLA"};
    for (size_t i = 0; i < sizeof(types) / sizeof(types[0]); i++) {
        char text[256];
        int n = snprintf(text, sizeof(text),
                         "FILE \"a\" \"b.mp3\" %s\r\n  TRACK 01 AUDIO\r\n"
                         "    INDEX 01 00:00:00\r\n",
                         types[i]);
        TC_CHECK(n > 0);
        tc_data *d = NULL;
        TC_CHECK_EQ_INT(tc_parse_mem(text, (size_t)n, TC_FMT_CUE, "t.cue", &d), TC_OK);
        if (!d) {
            continue;
        }
        tc_entry_info_t ei;
        TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
        TC_CHECK_EQ_STR(ei.source, "a\" \"b.mp3");
        tc_free(d);
    }
}

/* An INDEX keyword does not have to start the line, and lowercase input is not
 * recognised: the reference regexes are case-sensitive. */
static void test_keywords_are_case_sensitive(void) {
    const char *upper =
        "file \"a.wav\" WAVE\r\n"
        "  track 01 AUDIO\r\n"
        "    index 01 00:00:00\r\n";
    tc_data *d = NULL;
    /* Lowercase keywords never reach the NewTrack state, so the sheet is
     * empty. */
    TC_CHECK_EQ_INT(tc_parse_mem(upper, strlen(upper), TC_FMT_CUE, "t.cue", &d), TC_E_FORMAT);
    TC_CHECK(d == NULL);

    const char *embedded =
        "FILE \"a.wav\" WAVE\r\n"
        "  TRACK 01 AUDIO\r\n"
        "    REM XINDEX 01 00:00:00\r\n";
    TC_CHECK_EQ_INT(tc_parse_mem(embedded, strlen(embedded), TC_FMT_CUE, "t.cue", &d), TC_OK);
    if (d) {
        /* The keyword was found mid-line, exactly like the unanchored regex. */
        TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
        tc_free(d);
    }
}

/* The reference's \d matches every Unicode Nd character, and int.Parse then
 * rejects the non-ASCII ones with a FormatException, so a non-ASCII digit makes
 * the parse fail rather than being treated as ordinary text. */
static void test_non_ascii_digits_are_rejected(void) {
    static const char *const cases[] = {
        /* Arabic-Indic digits in the track number. */
        "FILE \"a.wav\" WAVE\r\n  TRACK \xD9\xA0\xD9\xA1 AUDIO\r\n    INDEX 01 00:00:00\r\n",
        /* ... in the index. */
        "FILE \"a.wav\" WAVE\r\n  TRACK 01 AUDIO\r\n    INDEX \xD9\xA0\xD9\xA1 00:00:00\r\n",
        /* ... in each of the three time fields. */
        "FILE \"a.wav\" WAVE\r\n  TRACK 01 AUDIO\r\n    INDEX 01 \xD9\xA0\xD9\xA0:00:00\r\n",
        "FILE \"a.wav\" WAVE\r\n  TRACK 01 AUDIO\r\n    INDEX 01 00:\xD9\xA0\xD9\xA0:00\r\n",
        "FILE \"a.wav\" WAVE\r\n  TRACK 01 AUDIO\r\n    INDEX 01 00:00:\xD9\xA0\xD9\xA0\r\n",
        /* Mixed ASCII and non-ASCII digits in one number. */
        "FILE \"a.wav\" WAVE\r\n  TRACK 01 AUDIO\r\n    INDEX 1\xD9\xA1 00:00:00\r\n",
        /* Fullwidth digits. */
        "FILE \"a.wav\" WAVE\r\n  TRACK \xEF\xBC\x90\xEF\xBC\x91 AUDIO\r\n    INDEX 01 00:00:00\r\n",
    };
    for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
        tc_data *d = NULL;
        TC_CHECK_EQ_INT(tc_parse_mem(cases[i], strlen(cases[i]), TC_FMT_CUE, "t.cue", &d),
                        TC_E_FORMAT);
        TC_CHECK(d == NULL);
        TC_CHECK_CONTAINS(tc_last_error(), "not an ASCII number");
    }

    /* An INDEX 00 line is skipped before its time fields are parsed, so a
     * non-ASCII digit there is harmless. */
    const char *pregap =
        "FILE \"a.wav\" WAVE\r\n"
        "  TRACK 01 AUDIO\r\n"
        "    INDEX 0 \xD9\xA0\xD9\xA0:\xD9\xA0\xD9\xA0:\xD9\xA0\xD9\xA0\r\n"
        "    INDEX 01 00:00:00\r\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(pregap, strlen(pregap), TC_FMT_CUE, "t.cue", &d), TC_OK);
    if (d) {
        TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
        tc_free(d);
    }
}

/* \d{2} consumes exactly two digits; a third digit is ordinary trailing text,
 * which the unanchored regex tolerates. */
static void test_extra_digits_are_trailing_text(void) {
    const char *text =
        "FILE \"a.wav\" WAVE\r\n"
        "  TRACK 01 AUDIO\r\n"
        "    INDEX 01 00:00:000\r\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(text, strlen(text), TC_FMT_CUE, "t.cue", &d), TC_OK);
    if (!d) {
        return;
    }
    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    TC_CHECK_EQ_INT(c.time_ns, 0);
    tc_free(d);
}

/* A track number that does not fit in Int32 throws in the reference, and it
 * throws as soon as the TRACK line matches rather than on the next line. */
static void test_track_number_overflow(void) {
    const char *text =
        "FILE \"a.wav\" WAVE\r\n"
        "  TRACK 01 AUDIO\r\n"
        "    INDEX 01 00:00:00\r\n"
        "  TRACK 2147483648 AUDIO\r\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(text, strlen(text), TC_FMT_CUE, "t.cue", &d), TC_E_FORMAT);
    TC_CHECK(d == NULL);
    TC_CHECK_CONTAINS(tc_last_error(), "out of range");

    /* Int32.MaxValue itself is fine. */
    const char *ok =
        "FILE \"a.wav\" WAVE\r\n"
        "  TRACK 01 AUDIO\r\n"
        "    INDEX 01 00:00:00\r\n"
        "  TRACK 2147483647 AUDIO\r\n"
        "    INDEX 01 00:02:00\r\n";
    TC_CHECK_EQ_INT(tc_parse_mem(ok, strlen(ok), TC_FMT_CUE, "t.cue", &d), TC_OK);
    if (d) {
        TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
        tc_free(d);
    }
}

/* ------------------------------------------------------------------ */
/* Error paths                                                        */
/* ------------------------------------------------------------------ */

/* "Empty cue file" covers everything that yields no chapter: an empty file,
 * text without a FILE statement, or a track without INDEX 01. */
static void test_empty_cue_file(void) {
    static const char *const cases[] = {
        "",
        "this is not a cue file at all\nsecond line\n",
        "TITLE \"x\"\r\nFILE \"a.wav\" WAVE\r\n\r\n",
        "TITLE \"x\"\r\nFILE \"a.wav\" WAVE\r\n  TRACK 01 AUDIO\r\n    TITLE \"a\"\r\n\r\n",
        "FILE \"a.wav\" WAVE\r\n  TRACK 01 AUDIO\r\n    INDEX 00 00:00:00\r\n",
    };
    for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
        tc_data *d = NULL;
        tc_status st = tc_parse_mem(cases[i], strlen(cases[i]), TC_FMT_CUE, "t.cue", &d);
        TC_CHECK_EQ_INT(st, TC_E_FORMAT);
        TC_CHECK(d == NULL);
        TC_CHECK_CONTAINS(tc_last_error(), "empty cue file");
    }
}

/* An INDEX other than 00 or 01 puts the state machine into its error state. */
static void test_index_other_than_zero_or_one(void) {
    const char *text =
        "TITLE \"x\"\r\n"
        "FILE \"a.wav\" WAVE\r\n"
        "  TRACK 01 AUDIO\r\n"
        "    INDEX 02 00:00:00\r\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(text, strlen(text), TC_FMT_CUE, "t.cue", &d), TC_E_FORMAT);
    TC_CHECK(d == NULL);
    TC_CHECK_CONTAINS(tc_last_error(), "unable to parse");
}

/* The error state is only reached while scanning a track; a stray INDEX in the
 * Start state is just an unrecognised line. */
static void test_error_state_requires_a_track(void) {
    const char *text =
        "INDEX 02 00:00:00\r\n"
        "FILE \"a.wav\" WAVE\r\n"
        "  TRACK 01 AUDIO\r\n"
        "    INDEX 01 00:00:00\r\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(text, strlen(text), TC_FMT_CUE, "t.cue", &d), TC_OK);
    if (d) {
        TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
        tc_free(d);
    }
}

/* An INDEX line without the MM:SS:FF tail does not match, so the track simply
 * has no chapter. */
static void test_malformed_index_is_not_a_match(void) {
    static const char *const rejected[] = {
        "INDEX 01 00:00\r\n",
        "INDEX 01 0:00:00\r\n",
        "INDEX 01 00:00:0a\r\n",
        "INDEX 01\r\n",
        "INDEX 01 100:49:57\r\n",
    };
    for (size_t i = 0; i < sizeof(rejected) / sizeof(rejected[0]); i++) {
        char text[256];
        int n = snprintf(text, sizeof(text),
                         "FILE \"a.wav\" WAVE\r\n  TRACK 01 AUDIO\r\n    %s", rejected[i]);
        TC_CHECK(n > 0);
        tc_data *d = NULL;
        tc_status st = tc_parse_mem(text, (size_t)n, TC_FMT_CUE, "t.cue", &d);
        TC_CHECK_EQ_INT(st, TC_E_FORMAT);
        TC_CHECK(d == NULL);
    }

    /* Accepted forms: \d+ for the index, exactly two digits per time field,
     * and any trailing text. */
    static const struct {
        const char *line;
        int64_t ms;
    } accepted[] = {
        {"INDEX 01 00:00:000\r\n", 0},
        {"INDEX 1 00:00:00\r\n", 0},
        {"INDEX 01 02:49:57 extra\r\n", 169760},
        {"INDEX  01   02:49:57\r\n", 169760},
        {"XINDEX 01 00:00:00\r\n", 0},
        /* Out-of-range minutes and seconds are passed straight to the
         * TimeSpan constructor, which normalises them. */
        {"INDEX 01 99:99:99\r\n", 6040320},
    };
    for (size_t i = 0; i < sizeof(accepted) / sizeof(accepted[0]); i++) {
        char text[256];
        int n = snprintf(text, sizeof(text),
                         "FILE \"a.wav\" WAVE\r\n  TRACK 01 AUDIO\r\n    %s",
                         accepted[i].line);
        TC_CHECK(n > 0);
        tc_data *d = NULL;
        TC_CHECK_EQ_INT(tc_parse_mem(text, (size_t)n, TC_FMT_CUE, "t.cue", &d), TC_OK);
        if (!d) {
            continue;
        }
        tc_chapter_t c;
        TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
        TC_CHECK_EQ_INT(c.time_ns, accepted[i].ms * 1000000LL);
        tc_free(d);
    }
}

static void test_missing_file_is_io_error(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_file("/definitely/not/here.cue", TC_FMT_CUE, &d), TC_E_IO);
    TC_CHECK(d == NULL);
    TC_CHECK(tc_last_error() != NULL);
}

static void test_null_arguments(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_file(NULL, TC_FMT_CUE, &d), TC_E_INVALID);
    TC_CHECK_EQ_INT(tc_parse_file("x.cue", TC_FMT_CUE, NULL), TC_E_INVALID);
    TC_CHECK_EQ_INT(tc_parse_mem(NULL, 0, TC_FMT_CUE, "t.cue", &d), TC_E_INVALID);
    TC_CHECK_EQ_INT(tc_parse_mem("x", 1, TC_FMT_CUE, "t.cue", NULL), TC_E_INVALID);
}

/* A CUE file is also recognised by its extension when the format is auto. */
static void test_auto_detection(void) {
    tc_data *d = NULL;
    char path[512];
    tc_test_data_path(path, sizeof(path), "cue-multitrack.cue");
    TC_CHECK_EQ_INT(tc_parse_file(path, TC_FMT_AUTO, &d), TC_OK);
    if (d) {
        TC_CHECK_EQ_INT(tc_entry_count(d), 1);
        tc_free(d);
    }
}

/* ------------------------------------------------------------------ */
/* Suite                                                              */
/* ------------------------------------------------------------------ */

TC_SUITE(cue) {
    TC_CASE("example_cue_sheet");      test_example_cue_sheet();
    TC_CASE("frame_timestamps");       test_frame_timestamps();
    TC_CASE("frame_rounding");         test_frame_field_rounding();
    TC_CASE("utf8");                   test_utf8_without_bom();
    TC_CASE("utf8_bom");               test_utf8_with_bom();
    TC_CASE("utf16le_bom");            test_utf16le_with_bom();
    TC_CASE("sjis_gbk_lossy");         test_sjis_and_gbk_are_replacement_characters();
    TC_CASE("utf8_replacements");      test_utf8_replacement_rules();
    TC_CASE("minimal");                test_minimal_sheet();
    TC_CASE("sorted_tracks");          test_tracks_are_sorted_by_number();
    TC_CASE("dropped_track");          test_track_without_index_is_dropped();
    TC_CASE("no_final_newline");       test_missing_final_newline();
    TC_CASE("performer");              test_performer_appends_to_name();
    TC_CASE("greedy_quotes");          test_greedy_quoted_capture();
    TC_CASE("empty_quotes");           test_empty_quoted_value_is_ignored();
    TC_CASE("blank_line_ends");        test_blank_line_ends_the_parse();
    TC_CASE("disc_title");             test_title_before_file_is_the_disc_title();
    TC_CASE("file_keywords");          test_file_keywords();
    TC_CASE("case_sensitive");         test_keywords_are_case_sensitive();
    TC_CASE("non_ascii_digits");       test_non_ascii_digits_are_rejected();
    TC_CASE("extra_digits");           test_extra_digits_are_trailing_text();
    TC_CASE("track_overflow");         test_track_number_overflow();
    TC_CASE("empty");                  test_empty_cue_file();
    TC_CASE("bad_index");              test_index_other_than_zero_or_one();
    TC_CASE("stray_index");            test_error_state_requires_a_track();
    TC_CASE("malformed_index");        test_malformed_index_is_not_a_match();
    TC_CASE("missing_file");           test_missing_file_is_io_error();
    TC_CASE("null_arguments");         test_null_arguments();
    TC_CASE("auto_detection");         test_auto_detection();
}
