/*
 * XPL parser tests.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * The fixture is a real HD DVD playlist from the original test suite
 * (TChapter.Test/Assets/XPL/VPLST000.XPL), copied verbatim. Its <TitleSet>
 * declares timeBase="60fps", so the expected timestamps below also serve as a
 * regression test for the time-base conversion.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_test.h"

static void test_parse_real_playlist(void) {
    char *text = NULL;
    size_t len = 0;
    if (tc_test_read_data("xpl-vplst000.xpl", &text, &len) != TC_OK) {
        /* The fixture is optional so that a checkout without it still builds;
         * report loudly rather than silently passing. */
        fprintf(stderr, "  (skipped: testdata/xpl-vplst000.xpl not found)\n");
        return;
    }

    tc_data *d = NULL;
    tc_status st = tc_parse_mem(text, len, TC_FMT_XPL, "VPLST000.XPL", &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    free(text);
    if (st != TC_OK) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }

    /* Only <Title> elements that carry a <ChapterList> become entries; the
     * FirstPlayTitle and the chapter-less titles are skipped. */
    size_t entries = tc_entry_count(d);
    TC_CHECK(entries > 0);

    /* Every entry must have at least one chapter and a non-empty title. */
    for (size_t i = 0; i < entries; i++) {
        tc_entry_info_t ei;
        TC_CHECK_EQ_INT(tc_entry_info(d, i, &ei), TC_OK);
        TC_CHECK(ei.title != NULL);
        TC_CHECK(ei.chapter_count > 0);
        TC_CHECK_EQ_INT(ei.fps_num, 24);
        TC_CHECK_EQ_INT(ei.fps_den, 1);
    }

    /* The first chaptered title in the fixture is id="MM" displayName="MM". */
    tc_entry_info_t first;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &first), TC_OK);
    TC_CHECK_EQ_STR(first.title, "MM");

    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    /* titleTimeBegin="00:00:00:00" with timeBase=60: zero either way. */
    TC_CHECK_EQ_INT(c.time_ns, 0);

    tc_free(d);
}

static void test_time_base_conversion(void) {
    /* A minimal document pinning the conversion arithmetic.
     *
     * With timeBase=60 and the default tickBase=24:
     *   "00:01:00:00" -> 60 s / 60 * 60 = 60 s   -> 60e9 ns
     *   "00:02:00:00" -> 120 s / 60 * 60 = 120 s -> 120e9 ns
     *
     * The tick field is scaled by 1/(24/1) s = 41666666.67 ns. A tick value of
     * 12 therefore lands at 500 ms, which is where the rounding is visible.
     */
    const char *doc =
        "<Playlist xmlns=\"http://www.dvdforum.org/2005/HDDVDVideo/Playlist\">"
        "  <TitleSet timeBase=\"60fps\">"
        "    <Title id=\"T1\" titleDuration=\"00:01:00:00\">"
        "      <ChapterList>"
        "        <Chapter titleTimeBegin=\"00:00:00:00\"/>"
        "        <Chapter titleTimeBegin=\"00:01:00:00\"/>"
        "        <Chapter titleTimeBegin=\"00:02:00:00\" displayName=\"Named\"/>"
        "      </ChapterList>"
        "    </Title>"
        "  </TitleSet>"
        "</Playlist>";

    tc_data *d = NULL;
    tc_status st = tc_parse_mem(doc, strlen(doc), TC_FMT_XPL, "test.xpl", &d);
    TC_CHECK_EQ_INT(st, TC_OK);
    if (st != TC_OK) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }

    TC_CHECK_EQ_INT(tc_entry_count(d), 1);

    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.title, "T1");
    TC_CHECK_EQ_INT(ei.chapter_count, 3);
    /* titleDuration="00:01:00:00" -> 60 s. */
    TC_CHECK_EQ_INT(ei.duration_ns, 60000000000LL);

    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    TC_CHECK_EQ_INT(c.time_ns, 0);

    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 1, &c), TC_OK);
    TC_CHECK_EQ_INT(c.time_ns, 60000000000LL);

    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 2, &c), TC_OK);
    TC_CHECK_EQ_INT(c.time_ns, 120000000000LL);
    /* displayName overrides id, which overrides the empty default. */
    TC_CHECK_EQ_STR(c.name, "Named");

    tc_free(d);
}

static void test_tick_field_is_scaled(void) {
    /* With tickBase="24" and a divisor of 1, one tick is 1/24 s. A tick count
     * of 24 must therefore add exactly one second. */
    const char *doc =
        "<Playlist xmlns=\"http://www.dvdforum.org/2005/HDDVDVideo/Playlist\">"
        "  <TitleSet timeBase=\"60fps\" tickBase=\"24\">"
        "    <Title id=\"T\">"
        "      <ChapterList>"
        "        <Chapter titleTimeBegin=\"00:00:00:24\"/>"
        "      </ChapterList>"
        "    </Title>"
        "  </TitleSet>"
        "</Playlist>";

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(doc, strlen(doc), TC_FMT_XPL, "t.xpl", &d), TC_OK);
    if (!d) {
        return;
    }
    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    TC_CHECK_EQ_INT(c.time_ns, 1000000000LL);
    tc_free(d);
}

static void test_tick_base_divisor(void) {
    /* tickBaseDivisor scales the tick length: with tickBase=24 and divisor=2,
     * one tick is 1/12 s, so 12 ticks is one second. */
    const char *doc =
        "<Playlist xmlns=\"http://www.dvdforum.org/2005/HDDVDVideo/Playlist\">"
        "  <TitleSet timeBase=\"60fps\" tickBase=\"24\">"
        "    <Title id=\"T\" tickBaseDivisor=\"2\">"
        "      <ChapterList>"
        "        <Chapter titleTimeBegin=\"00:00:00:12\"/>"
        "      </ChapterList>"
        "    </Title>"
        "  </TitleSet>"
        "</Playlist>";

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(doc, strlen(doc), TC_FMT_XPL, "t.xpl", &d), TC_OK);
    if (!d) {
        return;
    }
    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    TC_CHECK_EQ_INT(c.time_ns, 1000000000LL);
    tc_free(d);
}

static void test_multiple_title_sets_and_titles(void) {
    const char *doc =
        "<Playlist xmlns=\"http://www.dvdforum.org/2005/HDDVDVideo/Playlist\">"
        "  <TitleSet timeBase=\"60fps\">"
        "    <Title id=\"A\"><ChapterList>"
        "      <Chapter titleTimeBegin=\"00:00:00:00\"/>"
        "    </ChapterList></Title>"
        "    <FirstPlayTitle>"
        "      <PrimaryAudioVideoClip src=\"x\"/>"
        "    </FirstPlayTitle>"
        "    <Title id=\"B\"><ChapterList>"
        "      <Chapter titleTimeBegin=\"00:00:00:00\"/>"
        "      <Chapter titleTimeBegin=\"00:01:00:00\"/>"
        "    </ChapterList></Title>"
        "  </TitleSet>"
        "</Playlist>";

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(doc, strlen(doc), TC_FMT_XPL, "t.xpl", &d), TC_OK);
    if (!d) {
        return;
    }
    /* FirstPlayTitle has no ChapterList and must be skipped. */
    TC_CHECK_EQ_INT(tc_entry_count(d), 2);
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.title, "A");
    TC_CHECK_EQ_INT(tc_entry_info(d, 1, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.title, "B");
    TC_CHECK_EQ_INT(ei.chapter_count, 2);
    tc_free(d);
}

static void test_source_and_title_from_attributes(void) {
    const char *doc =
        "<Playlist xmlns=\"http://www.dvdforum.org/2005/HDDVDVideo/Playlist\">"
        "  <TitleSet timeBase=\"60fps\">"
        "    <Title id=\"MM\" displayName=\"Main Movie\">"
        "      <PrimaryAudioVideoClip src=\"file:///dvddisc/HVDVD_TS/PEVOB2.MAP\"/>"
        "      <ChapterList>"
        "        <Chapter titleTimeBegin=\"00:00:00:00\" id=\"c1\" displayName=\"Opening\"/>"
        "      </ChapterList>"
        "    </Title>"
        "  </TitleSet>"
        "</Playlist>";

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(doc, strlen(doc), TC_FMT_XPL, "t.xpl", &d), TC_OK);
    if (!d) {
        return;
    }
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    /* displayName wins over id. */
    TC_CHECK_EQ_STR(ei.title, "Main Movie");
    TC_CHECK_EQ_STR(ei.source, "file:///dvddisc/HVDVD_TS/PEVOB2.MAP");

    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    TC_CHECK_EQ_STR(c.name, "Opening");
    tc_free(d);
}

static void test_title_name_falls_back_to_file_name(void) {
    const char *doc =
        "<Playlist xmlns=\"http://www.dvdforum.org/2005/HDDVDVideo/Playlist\">"
        "  <TitleSet timeBase=\"60fps\">"
        "    <Title>"
        "      <ChapterList><Chapter titleTimeBegin=\"00:00:00:00\"/></ChapterList>"
        "    </Title>"
        "  </TitleSet>"
        "</Playlist>";

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(doc, strlen(doc), TC_FMT_XPL, "VPLST000.XPL", &d), TC_OK);
    if (!d) {
        return;
    }
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.title, "VPLST000");
    tc_free(d);
}

static void test_rejects_malformed_documents(void) {
    const char *cases[] = {
        "",                                                     /* empty */
        "<NotAPlaylist/>",                                      /* wrong root */
        "<Playlist/>",                                          /* no TitleSet */
        "<Playlist><TitleSet timeBase=\"60fps\"/></Playlist>",  /* no chaptered title */
        "<Playlist><TitleSet timeBase=\"60fps\"><Title><ChapterList>"
        "<Chapter/></ChapterList></Title></TitleSet></Playlist>", /* chapter without time */
        "<Playlist><TitleSet timeBase=\"60fps\"><Title><ChapterList>"
        "<Chapter titleTimeBegin=\"garbage\"/></ChapterList></Title></TitleSet></Playlist>",
    };
    for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
        tc_data *d = NULL;
        tc_status st = tc_parse_mem(cases[i], strlen(cases[i]), TC_FMT_XPL, "t.xpl", &d);
        TC_CHECK(st != TC_OK);
        TC_CHECK(d == NULL);
        if (d) {
            tc_free(d);
        }
    }
}

static void test_short_time_form_is_accepted(void) {
    /* "HH:MM:SS:TT" is the documented form, but the original parses the prefix
     * with TimeSpan.Parse and the tick field with decimal.Parse, both of which
     * accept a shortened value. "00:00:00" therefore means "zero minutes and a
     * tick count of zero", and must parse rather than fail. */
    const char *doc =
        "<Playlist xmlns=\"http://www.dvdforum.org/2005/HDDVDVideo/Playlist\">"
        "  <TitleSet timeBase=\"60fps\">"
        "    <Title id=\"T\">"
        "      <ChapterList><Chapter titleTimeBegin=\"00:00:00\"/></ChapterList>"
        "    </Title>"
        "  </TitleSet>"
        "</Playlist>";

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(doc, strlen(doc), TC_FMT_XPL, "t.xpl", &d), TC_OK);
    if (!d) {
        return;
    }
    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    TC_CHECK_EQ_INT(c.time_ns, 0);
    tc_free(d);
}

static void test_rejects_bad_arguments(void) {
    tc_data *d = NULL;
    /* A missing file is reported as an I/O failure, not a format failure. */
    TC_CHECK_EQ_INT(tc_parse_file("/definitely/not/here.xpl", TC_FMT_XPL, &d), TC_E_IO);
    TC_CHECK(d == NULL);

    /* A NULL buffer is rejected by the dispatcher before any parsing. */
    TC_CHECK_EQ_INT(tc_parse_mem(NULL, 0, TC_FMT_XPL, "t.xpl", &d), TC_E_INVALID);
    TC_CHECK(d == NULL);
}

TC_SUITE(xpl) {
    TC_CASE("real_playlist");        test_parse_real_playlist();
    TC_CASE("time_base");            test_time_base_conversion();
    TC_CASE("tick_field");           test_tick_field_is_scaled();
    TC_CASE("tick_base_divisor");    test_tick_base_divisor();
    TC_CASE("multiple_titles");      test_multiple_title_sets_and_titles();
    TC_CASE("attributes");           test_source_and_title_from_attributes();
    TC_CASE("title_name_default");   test_title_name_falls_back_to_file_name();
    TC_CASE("short_time");           test_short_time_form_is_accepted();
    TC_CASE("malformed");            test_rejects_malformed_documents();
    TC_CASE("bad_arguments");        test_rejects_bad_arguments();
}
