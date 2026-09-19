/*
 * Matroska chapter XML parser tests (B9).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * The fixtures and the expected values come from the reference test assets and
 * from mkvextract itself:
 *
 *   mkvextract-00001.xml  real `mkvextract chapters` output for
 *                         TChapter.Test/Assets/MKV/00001.mkv (mkvextract 34.0.0)
 *   ordered-chapter.xml   TChapter.Test/Assets/XML/ordered_chapter.xml, whose
 *                         14 editions carry both ChapterTimeStart and
 *                         ChapterTimeEnd
 *   sub-chapter.xml       TChapter.Test/Assets/XML/sub_chapter.xml, nested
 *                         ChapterAtoms with UTF-8 names
 *
 * Every expected number was produced by running the reference assembly
 * (TChapter.Parsing.XMLParser, built from the same sources) over the fixture;
 * see the report for the exact command. The reference throws a
 * NullReferenceException when an atom has no <ChapterDisplay>; this parser
 * returns an empty name instead, because mkvextract emits such atoms for
 * chapters that were written without a name.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_test.h"

/* ------------------------------------------------------------------ */
/* Helpers                                                            */
/* ------------------------------------------------------------------ */

static tc_status parse_text(const char *text, tc_data **out) {
    return tc_parse_mem(text, strlen(text), TC_FMT_MATROSKA_XML, NULL, out);
}

static tc_status parse_fixture(const char *name, tc_data **out) {
    char *buf = NULL;
    size_t len = 0;
    tc_status st = tc_test_read_data(name, &buf, &len);
    if (st != TC_OK) {
        return st;
    }
    st = tc_parse_mem(buf, len, TC_FMT_MATROSKA_XML, NULL, out);
    free(buf);
    return st;
}

static int64_t chapter_time(tc_data *d, size_t entry, size_t index) {
    tc_chapter_t c;
    if (tc_chapter_at(d, entry, index, &c) != TC_OK) {
        return -1;
    }
    return c.time_ns;
}

static const char *chapter_name(tc_data *d, size_t entry, size_t index) {
    tc_chapter_t c;
    if (tc_chapter_at(d, entry, index, &c) != TC_OK) {
        return NULL;
    }
    return c.name;
}

/* ------------------------------------------------------------------ */
/* Real mkvextract output                                             */
/* ------------------------------------------------------------------ */

static void test_real_mkvextract_output(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("matroska/mkvextract-00001.xml", &d), TC_OK);
    if (!d) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }

    /* One entry per <EditionEntry>; the file has exactly one. */
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 4);

    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "Chapter 01");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 0), 0);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 1), "Chapter 02");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 1), 10000000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 2), "Chapter 03");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 2), 20000000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 3), "Chapter 04");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 3), 30000000000LL);

    /* These atoms carry no ChapterTimeEnd, so each contributes one chapter. */
    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    TC_CHECK_EQ_INT(c.frames, -1);

    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_INT(ei.chapter_count, 4);
    /* This format carries no title, source or duration. */
    TC_CHECK_EQ_STR(ei.title, "");
    TC_CHECK_EQ_STR(ei.source, "");
    TC_CHECK_EQ_INT(ei.duration_ns, 0);

    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* ChapterTimeEnd                                                     */
/* ------------------------------------------------------------------ */

static void test_chapter_time_end(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("matroska/end-times.xml", &d), TC_OK);
    if (!d) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }

    /* The reference emits a chapter for the start, then the sub-atoms, then a
     * second chapter for the end. The first atom therefore contributes
     * start, sub-atom start, sub-atom end, its own end - in that order. */
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 6);

    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "Act 1");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 0), 0);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 1), "Piece 1");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 1), 0);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 2), "Piece 1");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 2), 384000000000LL); /* 00:06:24 */
    TC_CHECK_EQ_STR(chapter_name(d, 0, 3), "Piece 2");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 3), 384000000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 4), "Act 1");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 4), 680000000000LL); /* 00:11:20 */

    /* An atom with only ChapterTimeEnd still produces a chapter. */
    TC_CHECK_EQ_STR(chapter_name(d, 0, 5), "Act 2 end only");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 5), 1706000000000LL); /* 00:28:26 */

    tc_free(d);
}

static void test_time_end_only(void) {
    const char *doc =
        "<Chapters><EditionEntry><ChapterAtom>"
        "<ChapterTimeEnd>00:00:42.000000000</ChapterTimeEnd>"
        "<ChapterDisplay><ChapterString>EndOnly</ChapterString></ChapterDisplay>"
        "</ChapterAtom></EditionEntry></Chapters>";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(doc, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "EndOnly");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 0), 42000000000LL);
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Multiple ChapterDisplay elements                                   */
/* ------------------------------------------------------------------ */

static void test_multiple_displays(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("matroska/multi-display.xml", &d), TC_OK);
    if (!d) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }

    /* A non-repeated property binds the first element only, so the second
     * ChapterDisplay - and a second ChapterString inside one display - are
     * both ignored. */
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "First");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 0), 10000000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 1), "One");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 1), 20000000000LL);

    tc_free(d);
}

static void test_missing_display(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("matroska/no-display.xml", &d), TC_OK);
    if (!d) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }
    /* The reference throws a NullReferenceException here; see the file
     * comment. mkvextract really does emit display-less atoms. */
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 0), 5000000000LL);
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Flags                                                              */
/* ------------------------------------------------------------------ */

static void test_flags_are_ignored(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("matroska/hidden.xml", &d), TC_OK);
    if (!d) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }

    /* ChapterFlagHidden, ChapterFlagEnabled and EditionFlagHidden are
     * deserialised by the reference but never consulted. */
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "Hidden");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 0), 0);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 1), "Visible");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 1), 30000000000LL);
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Time code forms                                                    */
/* ------------------------------------------------------------------ */

static void test_timecode_forms(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("matroska/timecodes.xml", &d), TC_OK);
    if (!d) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }

    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 10);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 0), 0);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 1), 1500000000LL);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 2), 2250000000LL);  /* comma separator */
    /* ".1234" is 123.4 ms: the reference divides by a double, so the fraction
     * is not truncated to whole milliseconds. */
    TC_CHECK_EQ_INT(chapter_time(d, 0, 3), 3123400000LL);
    /* The regex is unanchored: surrounding text is ignored. */
    TC_CHECK_EQ_INT(chapter_time(d, 0, 4), 4000000000LL);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 5), 86400000000000LL); /* 24 hours */
    /* Whitespace around the separators is allowed. */
    TC_CHECK_EQ_INT(chapter_time(d, 0, 6), 5500000000LL);
    /* Fewer than three fraction digits do not match; the reference then uses
     * TimeSpan.Zero for the whole field. */
    TC_CHECK_EQ_INT(chapter_time(d, 0, 7), 0);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 8), 0);
    /* Only the first nine fraction digits take part, and the double division
     * leaves 123.4567 ms of the 123.456789 ms that were kept. */
    TC_CHECK_EQ_INT(chapter_time(d, 0, 9), 8123456700LL);

    tc_free(d);
}

static void test_fraction_rounding_matches_reference(void) {
    /* The reference computes the fraction as
     *   (long)(long.Parse(f) / Math.Pow(10, len - 3) * 10000)
     * and double rounding makes this differ from integer scaling by one tick
     * on some inputs: 75981 / 100 is 759.80999999999995 in IEEE-754, which
     * truncates to 7598099 ticks, not 7598100. */
    const char *doc =
        "<Chapters><EditionEntry>"
        "<ChapterAtom><ChapterTimeStart>00:00:00.75981</ChapterTimeStart></ChapterAtom>"
        "<ChapterAtom><ChapterTimeStart>00:00:00.40453</ChapterTimeStart></ChapterAtom>"
        "</EditionEntry></Chapters>";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(doc, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 0), 759809900LL);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 1), 404529900LL);
    tc_free(d);
}

static void test_out_of_range_minutes_and_seconds(void) {
    /* TimeSpan normalises these, so the reference accepts "00:99:00.000" as
     * 99 minutes. */
    const char *doc =
        "<Chapters><EditionEntry>"
        "<ChapterAtom><ChapterTimeStart>00:99:00.000</ChapterTimeStart></ChapterAtom>"
        "<ChapterAtom><ChapterTimeStart>00:00:99.000</ChapterTimeStart></ChapterAtom>"
        "</EditionEntry></Chapters>";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(doc, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 0), 5940000000000LL); /* 99 minutes */
    TC_CHECK_EQ_INT(chapter_time(d, 0, 1), 99000000000LL);   /* 99 seconds */
    tc_free(d);
}

static void test_no_time_is_not_a_chapter(void) {
    const char *doc =
        "<Chapters><EditionEntry><ChapterAtom>"
        "<ChapterUID>7</ChapterUID>"
        "<ChapterDisplay><ChapterString>Nothing</ChapterString></ChapterDisplay>"
        "</ChapterAtom></EditionEntry></Chapters>";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(doc, &d), TC_OK);
    if (!d) {
        return;
    }
    /* An atom without a time element contributes nothing; the reference
     * checks the deserialised property for null. */
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 0);
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Editions                                                           */
/* ------------------------------------------------------------------ */

static void test_multiple_editions(void) {
    /* Each edition is a separate entry, and the chapter numbers restart per
     * edition in the reference. The C ABI does not expose numbers. */
    const char *doc =
        "<Chapters>"
        "<EditionEntry><ChapterAtom>"
        "<ChapterTimeStart>00:00:00.000</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>E1C1</ChapterString></ChapterDisplay>"
        "</ChapterAtom></EditionEntry>"
        "<EditionEntry>"
        "<ChapterAtom><ChapterTimeStart>00:00:01.000</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>E2C1</ChapterString></ChapterDisplay></ChapterAtom>"
        "<ChapterAtom><ChapterTimeStart>00:00:02.000</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>E2C2</ChapterString></ChapterDisplay></ChapterAtom>"
        "</EditionEntry>"
        "</Chapters>";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(doc, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 2);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 1), 2);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "E1C1");
    TC_CHECK_EQ_STR(chapter_name(d, 1, 0), "E2C1");
    TC_CHECK_EQ_STR(chapter_name(d, 1, 1), "E2C2");
    TC_CHECK_EQ_INT(chapter_time(d, 1, 1), 2000000000LL);
    tc_free(d);
}

static void test_edition_without_atoms_is_an_entry(void) {
    const char *doc = "<Chapters><EditionEntry></EditionEntry></Chapters>";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(doc, &d), TC_OK);
    if (!d) {
        return;
    }
    /* The reference yields a ChapterInfo per edition regardless of its
     * content; the entry count is what tells the caller an edition exists. */
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 0);
    tc_free(d);
}

static void test_empty_chapters_is_rejected(void) {
    /* <Chapters> with no edition: the reference walks a null array and throws. */
    tc_data *d = NULL;
    TC_CHECK(parse_fixture("matroska/empty.xml", &d) != TC_OK);
    TC_CHECK(d == NULL);
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Reference test assets                                              */
/* ------------------------------------------------------------------ */

static void test_ordered_chapter_asset(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("matroska/ordered-chapter.xml", &d), TC_OK);
    if (!d) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }

    /* 14 editions, each with three atoms that carry start and end, so six
     * chapters per entry. */
    TC_CHECK_EQ_INT(tc_entry_count(d), 14);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 6);

    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "Part A");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 0), 0);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 1), 54596000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 2), "Part B - EP02");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 2), 54596000000LL);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 3), 59601000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 4), "Part C");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 4), 59601000000LL);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 5), 93093000000LL);

    /* The last edition ends at 00:02:38.158. */
    TC_CHECK_EQ_INT(tc_chapter_count(d, 13), 6);
    TC_CHECK_EQ_STR(chapter_name(d, 13, 3), "Part B - SP02");
    TC_CHECK_EQ_INT(chapter_time(d, 13, 3), 158158000000LL);

    /* Every entry has the same shape. */
    for (size_t i = 0; i < tc_entry_count(d); i++) {
        TC_CHECK_EQ_INT(tc_chapter_count(d, i), 6);
    }

    tc_free(d);
}

static void test_sub_chapter_asset(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("matroska/sub-chapter.xml", &d), TC_OK);
    if (!d) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }

    /* Two acts, each with three pieces; every atom carries both ends. */
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 16);

    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "erster Akt");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 0), 0);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 1), 0);
    /* The file is UTF-8 despite its ISO-8859-1 declaration, so the names must
     * come through as UTF-8. */
    TC_CHECK_EQ_STR(chapter_name(d, 0, 2), "Ouvert\xC3\xBCre");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 2), 384000000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 3), "Arie: Jetzt, Sch\xC3\xA4tzchen, jetzt sind wir allein");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 7), 680000000000LL);

    /* The second act is a sibling atom, not a sub-atom. */
    TC_CHECK_EQ_STR(chapter_name(d, 0, 8), "zweiter Akt");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 8), 680000000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 15), "zweiter Akt");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 15), 1706000000000LL);

    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Structural handling                                                */
/* ------------------------------------------------------------------ */

static void test_entities_and_cdata(void) {
    const char *doc =
        "<Chapters><EditionEntry><ChapterAtom>"
        "<ChapterTimeStart>00:00:10.000</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>A &amp; B &#65;&#x42; &lt;tag&gt;</ChapterString></ChapterDisplay>"
        "</ChapterAtom><ChapterAtom>"
        "<ChapterTimeStart>00:00:20.000</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString><![CDATA[Raw <name> & co]]></ChapterString></ChapterDisplay>"
        "</ChapterAtom></EditionEntry></Chapters>";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(doc, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 2);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "A & B AB <tag>");
    TC_CHECK_EQ_STR(chapter_name(d, 0, 1), "Raw <name> & co");
    tc_free(d);
}

static void test_comments_and_declaration(void) {
    const char *doc =
        "<?xml version=\"1.0\"?>\n"
        "<!-- <!DOCTYPE Chapters SYSTEM \"matroskachapters.dtd\"> -->\n"
        "<Chapters>\n"
        "  <EditionEntry>\n"
        "    <!-- an atom -->\n"
        "    <ChapterAtom>\n"
        "      <ChapterTimeStart>00:00:00.000000000</ChapterTimeStart>\n"
        "      <ChapterDisplay><ChapterString>Opening</ChapterString></ChapterDisplay>\n"
        "    </ChapterAtom>\n"
        "  </EditionEntry>\n"
        "</Chapters>\n";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(doc, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "Opening");
    tc_free(d);
}

static void test_deeply_nested_atoms(void) {
    const char *doc =
        "<Chapters><EditionEntry><ChapterAtom>"
        "<ChapterTimeStart>00:00:01.000</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>L1</ChapterString></ChapterDisplay>"
        "<ChapterAtom>"
        "<ChapterTimeStart>00:00:02.000</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>L2</ChapterString></ChapterDisplay>"
        "<ChapterAtom>"
        "<ChapterTimeStart>00:00:03.000</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>L3</ChapterString></ChapterDisplay>"
        "</ChapterAtom></ChapterAtom></ChapterAtom>"
        "</EditionEntry></Chapters>";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(doc, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 3);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "L1");
    TC_CHECK_EQ_STR(chapter_name(d, 0, 1), "L2");
    TC_CHECK_EQ_STR(chapter_name(d, 0, 2), "L3");
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Error paths                                                        */
/* ------------------------------------------------------------------ */

static void test_malformed_xml_is_rejected(void) {
    const char *cases[] = {
        "",                                              /* empty input */
        "not xml at all",                                /* no element */
        "<Chapters><EditionEntry>",                      /* unterminated */
        "<Chapters><EditionEntry></Chapters>",           /* mismatched close */
        "<Chapters><EditionEntry><ChapterAtom>",         /* unterminated atom */
        "<Chapters><EditionEntry></EditionEntry>",       /* unterminated root */
        "<Chapters attr></Chapters>",                    /* attribute without value */
    };
    for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
        tc_data *d = NULL;
        tc_status st = parse_text(cases[i], &d);
        TC_CHECK(st != TC_OK);
        TC_CHECK(d == NULL);
        tc_free(d);
    }
}

static void test_wrong_root_is_rejected(void) {
    const char *cases[] = {
        "<XPL><title name=\"x\"/></XPL>",
        "<MatroskaChapters><EditionEntry/></MatroskaChapters>",
        /* The reference's serializer is case-sensitive. */
        "<chapters><EditionEntry/></chapters>",
        /* ... and it does not accept a namespaced root. */
        "<Chapters xmlns=\"http://www.matroska.org/ns\"><EditionEntry/></Chapters>",
    };
    for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
        tc_data *d = NULL;
        tc_status st = parse_text(cases[i], &d);
        TC_CHECK(st != TC_OK);
        TC_CHECK(d == NULL);
        tc_free(d);
    }
}

static void test_bad_arguments(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(NULL, 0, TC_FMT_MATROSKA_XML, NULL, &d), TC_E_INVALID);
    TC_CHECK_EQ_INT(tc_parse_mem("x", 1, TC_FMT_MATROSKA_XML, NULL, NULL), TC_E_INVALID);
    TC_CHECK(d == NULL);

    /* A missing file is an I/O failure, not a format failure. */
    TC_CHECK_EQ_INT(tc_parse_file("/definitely/not/here.xml", TC_FMT_MATROSKA_XML, &d), TC_E_IO);
    TC_CHECK(d == NULL);
}

/* The dispatcher routes tc_parse_file through the memory parser for this
 * format, so reading a file must produce the same result as parsing its
 * bytes. */
static void test_parse_file_matches_memory(void) {
    char *raw = NULL;
    size_t len = 0;
    if (tc_test_read_data("matroska/mkvextract-00001.xml", &raw, &len) != TC_OK) {
        fprintf(stderr, "  (skipped: testdata/matroska/mkvextract-00001.xml not found)\n");
        return;
    }

    tc_data *from_file = NULL;
    char path[1024];
    tc_test_data_path(path, sizeof(path), "matroska/mkvextract-00001.xml");
    TC_CHECK_EQ_INT(tc_parse_file(path, TC_FMT_MATROSKA_XML, &from_file), TC_OK);

    tc_data *from_mem = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(raw, len, TC_FMT_MATROSKA_XML, path, &from_mem), TC_OK);
    free(raw);

    if (from_file && from_mem) {
        TC_CHECK_EQ_INT(tc_entry_count(from_file), tc_entry_count(from_mem));
        TC_CHECK_EQ_INT(tc_chapter_count(from_file, 0), tc_chapter_count(from_mem, 0));
        TC_CHECK_EQ_STR(chapter_name(from_file, 0, 1), chapter_name(from_mem, 0, 1));
        TC_CHECK_EQ_INT(chapter_time(from_file, 0, 3), chapter_time(from_mem, 0, 3));
    }
    tc_free(from_file);
    tc_free(from_mem);
}

/* ------------------------------------------------------------------ */
/* Round trip                                                         */
/* ------------------------------------------------------------------ */

static void test_write_read_round_trip(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("matroska/mkvextract-00001.xml", &d), TC_OK);
    if (!d) {
        return;
    }

    size_t need = 0;
    TC_CHECK_EQ_INT(tc_render(d, 0, TC_FMT_MATROSKA_XML, "eng", NULL, NULL, &need), TC_E_RANGE);
    char *buf = malloc(need);
    TC_CHECK(buf != NULL);
    if (buf) {
        size_t len = need;
        TC_CHECK_EQ_INT(tc_render(d, 0, TC_FMT_MATROSKA_XML, "eng", NULL, buf, &len), TC_OK);

        tc_data *again = NULL;
        TC_CHECK_EQ_INT(tc_parse_mem(buf, len, TC_FMT_MATROSKA_XML, NULL, &again), TC_OK);
        if (again) {
            TC_CHECK_EQ_INT(tc_chapter_count(again, 0), 4);
            TC_CHECK_EQ_STR(chapter_name(again, 0, 0), "Chapter 01");
            TC_CHECK_EQ_INT(chapter_time(again, 0, 3), 30000000000LL);
            tc_free(again);
        }
        free(buf);
    }
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Suite                                                              */
/* ------------------------------------------------------------------ */

TC_SUITE(matroska) {
    TC_CASE("mkvextract_output");  test_real_mkvextract_output();
    TC_CASE("time_end");           test_chapter_time_end();
    TC_CASE("time_end_only");      test_time_end_only();
    TC_CASE("multi_display");      test_multiple_displays();
    TC_CASE("no_display");         test_missing_display();
    TC_CASE("flags");              test_flags_are_ignored();
    TC_CASE("timecode_forms");     test_timecode_forms();
    TC_CASE("fraction_ticks");     test_fraction_rounding_matches_reference();
    TC_CASE("timecode_ranges");    test_out_of_range_minutes_and_seconds();
    TC_CASE("no_time");            test_no_time_is_not_a_chapter();
    TC_CASE("editions");           test_multiple_editions();
    TC_CASE("edition_empty");      test_edition_without_atoms_is_an_entry();
    TC_CASE("empty_chapters");     test_empty_chapters_is_rejected();
    TC_CASE("ordered_asset");      test_ordered_chapter_asset();
    TC_CASE("sub_chapter_asset");  test_sub_chapter_asset();
    TC_CASE("entities");           test_entities_and_cdata();
    TC_CASE("comments");           test_comments_and_declaration();
    TC_CASE("nested_atoms");       test_deeply_nested_atoms();
    TC_CASE("malformed");          test_malformed_xml_is_rejected();
    TC_CASE("wrong_root");         test_wrong_root_is_rejected();
    TC_CASE("bad_arguments");      test_bad_arguments();
    TC_CASE("parse_file");         test_parse_file_matches_memory();
    TC_CASE("round_trip");         test_write_read_round_trip();
}
