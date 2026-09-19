/*
 * Chapter XML parser tests (B11).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * xml-sub-chapter.xml and xml-ordered-chapter.xml are the reference
 * implementation's own assets (TChapter.Test/Assets/XML). The remaining inputs
 * are built in memory, and every expected value was produced by replaying
 * TChapter.Parsing.XMLParser with .NET Framework 4.8 - the framework the test
 * project actually targets - including the cases where the reference throws a
 * NullReferenceException and this parser reports a format error instead.
 *
 * XMLParser is also the back end of MATROSKAParser, so the traversal here is
 * the same as the one in parsers/tc_matroska.c; see tc_test_matroska.c for the
 * mkvextract-specific fixtures.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_test.h"

/* ------------------------------------------------------------------ */
/* Helpers                                                            */
/* ------------------------------------------------------------------ */

static tc_status parse_text(const char *text, tc_data **out) {
    return tc_parse_mem(text, strlen(text), TC_FMT_XML, NULL, out);
}

static tc_status parse_fixture(const char *name, tc_data **out) {
    char *buf = NULL;
    size_t len = 0;
    tc_status st = tc_test_read_data(name, &buf, &len);
    if (st != TC_OK) {
        return st;
    }
    st = tc_parse_mem(buf, len, TC_FMT_XML, NULL, out);
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
/* Reference assets                                                   */
/* ------------------------------------------------------------------ */

static void test_sub_chapter_asset(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("xml-sub-chapter.xml", &d), TC_OK);
    if (!d) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }

    /* One <EditionEntry>, two acts with three nested pieces each. Every atom
     * carries both ends, so it contributes a start and an end chapter. */
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 16);

    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "erster Akt");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 0), 0);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 1), 0);
    /* The file is UTF-8 despite its ISO-8859-1 declaration. */
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

static void test_ordered_chapter_asset(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_fixture("xml-ordered-chapter.xml", &d), TC_OK);
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

    TC_CHECK_EQ_INT(tc_chapter_count(d, 13), 6);
    TC_CHECK_EQ_STR(chapter_name(d, 13, 3), "Part B - SP02");
    TC_CHECK_EQ_INT(chapter_time(d, 13, 3), 158158000000LL);

    for (size_t i = 0; i < tc_entry_count(d); i++) {
        TC_CHECK_EQ_INT(tc_chapter_count(d, i), 6);
    }

    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Chapter names                                                      */
/* ------------------------------------------------------------------ */

/* The name is the first <ChapterDisplay>'s first <ChapterString>. Later
 * displays - the reference's serializer binds a non-repeated property - and
 * later strings inside one display are ignored. */
static void test_multiple_displays(void) {
    const char *doc =
        "<Chapters><EditionEntry><ChapterAtom>"
        "<ChapterTimeStart>00:00:00.000</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>eng name</ChapterString><ChapterLanguage>eng</ChapterLanguage></ChapterDisplay>"
        "<ChapterDisplay><ChapterString>jpn name</ChapterString><ChapterLanguage>jpn</ChapterLanguage></ChapterDisplay>"
        "</ChapterAtom></EditionEntry></Chapters>";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(doc, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "eng name");
    tc_free(d);

    /* A second <ChapterString> in the same display is ignored too. */
    const char *two_strings =
        "<Chapters><EditionEntry><ChapterAtom>"
        "<ChapterTimeStart>00:00:00.000</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>first</ChapterString><ChapterString>second</ChapterString></ChapterDisplay>"
        "</ChapterAtom></EditionEntry></Chapters>";
    d = NULL;
    TC_CHECK_EQ_INT(parse_text(two_strings, &d), TC_OK);
    if (d) {
        TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "first");
        tc_free(d);
    }
}

/* A display with a language but no string has an empty name; a missing display
 * entirely is a null dereference in the reference, reported here as an empty
 * name. */
static void test_missing_chapter_string(void) {
    const char *no_string =
        "<Chapters><EditionEntry><ChapterAtom>"
        "<ChapterTimeStart>00:00:00.000</ChapterTimeStart>"
        "<ChapterDisplay><ChapterLanguage>eng</ChapterLanguage></ChapterDisplay>"
        "</ChapterAtom></EditionEntry></Chapters>";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(no_string, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "");
    tc_free(d);

    /* An empty string element is also an empty name. */
    const char *empty =
        "<Chapters><EditionEntry><ChapterAtom>"
        "<ChapterTimeStart>00:00:00.000</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString></ChapterString></ChapterDisplay>"
        "</ChapterAtom></EditionEntry></Chapters>";
    d = NULL;
    TC_CHECK_EQ_INT(parse_text(empty, &d), TC_OK);
    if (d) {
        TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "");
        tc_free(d);
    }
}

/* An atom without a display at all makes the reference throw; this parser
 * returns an empty name, which is the only usable behaviour for the atoms
 * mkvextract emits for unnamed chapters (see tc_test_matroska.c). */
static void test_no_display_is_empty_name(void) {
    const char *doc =
        "<Chapters><EditionEntry><ChapterAtom>"
        "<ChapterTimeStart>00:00:00.000</ChapterTimeStart>"
        "</ChapterAtom></EditionEntry></Chapters>";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(doc, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "");
    tc_free(d);
}

/* Names are entity- and CDATA-decoded. */
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

/* ------------------------------------------------------------------ */
/* Time codes                                                         */
/* ------------------------------------------------------------------ */

/* The time is taken through Config.TIME_FORMAT, so the fraction is truncated to
 * milliseconds, oversized minute and second fields carry, and a value without a
 * match becomes zero. */
static void test_timecode_forms(void) {
    const char *doc =
        "<Chapters><EditionEntry>"
        "<ChapterAtom><ChapterTimeStart>00:00:00.000000000</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>a</ChapterString></ChapterDisplay></ChapterAtom>"
        "<ChapterAtom><ChapterTimeStart>00:00:01.500000000</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>b</ChapterString></ChapterDisplay></ChapterAtom>"
        "<ChapterAtom><ChapterTimeStart>00:00:02,250</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>c</ChapterString></ChapterDisplay></ChapterAtom>"
        "<ChapterAtom><ChapterTimeStart>00:00:03.1234</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>d</ChapterString></ChapterDisplay></ChapterAtom>"
        "<ChapterAtom><ChapterTimeStart>text 00:00:04.000 text</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>e</ChapterString></ChapterDisplay></ChapterAtom>"
        "<ChapterAtom><ChapterTimeStart> 00 : 00 : 05.500 </ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>f</ChapterString></ChapterDisplay></ChapterAtom>"
        "<ChapterAtom><ChapterTimeStart>00:99:00.000</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>g</ChapterString></ChapterDisplay></ChapterAtom>"
        "<ChapterAtom><ChapterTimeStart>00:00:99.000</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>h</ChapterString></ChapterDisplay></ChapterAtom>"
        "<ChapterAtom><ChapterTimeStart>00:00:01.12</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>i</ChapterString></ChapterDisplay></ChapterAtom>"
        "<ChapterAtom><ChapterTimeStart>not a time</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>j</ChapterString></ChapterDisplay></ChapterAtom>"
        "</EditionEntry></Chapters>";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(doc, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 10);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 0), 0);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 1), 1500000000LL);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 2), 2250000000LL); /* comma separator */
    /* ".1234" is 123.4 ms, which the reference's double arithmetic keeps to the
     * tick; truncating to three digits would lose the 0.4 ms. */
    TC_CHECK_EQ_INT(chapter_time(d, 0, 3), 3123400000LL);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 4), 4000000000LL); /* unanchored match */
    TC_CHECK_EQ_INT(chapter_time(d, 0, 5), 5500000000LL); /* padded separators */
    TC_CHECK_EQ_INT(chapter_time(d, 0, 6), 5940000000000LL);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 7), 99000000000LL);
    /* Fewer than three fraction digits do not match; the field is zero. */
    TC_CHECK_EQ_INT(chapter_time(d, 0, 8), 0);
    TC_CHECK_EQ_INT(chapter_time(d, 0, 9), 0);
    tc_free(d);
}

/* An atom emits its start, then its sub-atoms, then its end. */
static void test_chapter_time_end(void) {
    const char *doc =
        "<Chapters><EditionEntry><ChapterAtom>"
        "<ChapterTimeStart>00:00:00.000</ChapterTimeStart>"
        "<ChapterTimeEnd>00:11:20.000</ChapterTimeEnd>"
        "<ChapterDisplay><ChapterString>Act 1</ChapterString></ChapterDisplay>"
        "<ChapterAtom>"
        "<ChapterTimeStart>00:06:24.000</ChapterTimeStart>"
        "<ChapterTimeEnd>00:11:10.000</ChapterTimeEnd>"
        "<ChapterDisplay><ChapterString>Piece 1</ChapterString></ChapterDisplay>"
        "</ChapterAtom>"
        "</ChapterAtom></EditionEntry></Chapters>";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(doc, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 4);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "Act 1");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 0), 0);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 1), "Piece 1");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 1), 384000000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 2), "Piece 1");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 2), 670000000000LL);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 3), "Act 1");
    TC_CHECK_EQ_INT(chapter_time(d, 0, 3), 680000000000LL);
    tc_free(d);
}

/* An atom with only an end time still contributes a chapter, and one with
 * neither time contributes nothing. */
static void test_time_end_only(void) {
    const char *doc =
        "<Chapters><EditionEntry>"
        "<ChapterAtom><ChapterTimeEnd>00:00:42.000</ChapterTimeEnd>"
        "<ChapterDisplay><ChapterString>EndOnly</ChapterString></ChapterDisplay></ChapterAtom>"
        "<ChapterAtom><ChapterUID>7</ChapterUID>"
        "<ChapterDisplay><ChapterString>Nothing</ChapterString></ChapterDisplay></ChapterAtom>"
        "</EditionEntry></Chapters>";
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
/* Editions                                                           */
/* ------------------------------------------------------------------ */

static void test_multiple_editions(void) {
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

    /* No title, source or duration is carried by this format. */
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.title, "");
    TC_CHECK_EQ_STR(ei.source, "");
    TC_CHECK_EQ_INT(ei.duration_ns, 0);

    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    TC_CHECK_EQ_INT(c.frames, -1);

    tc_free(d);
}

/* An edition without atoms is still an entry; the reference yields a ChapterInfo
 * per edition regardless of its content. (Its ToChapterInfo then dereferences
 * the null ChapterAtom array, so a real file with such an edition cannot be
 * consumed by the reference; producing an empty entry is the only usable
 * behaviour and matches B9.) */
static void test_edition_without_atoms_is_an_entry(void) {
    const char *doc = "<Chapters><EditionEntry></EditionEntry></Chapters>";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(doc, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 0);
    tc_free(d);
}

/* <Chapters> with no edition makes the reference walk a null array and throw. */
static void test_empty_chapters_is_rejected(void) {
    const char *cases[] = {
        "<Chapters></Chapters>",
        "<Chapters/>",
    };
    for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
        tc_data *d = NULL;
        TC_CHECK(parse_text(cases[i], &d) != TC_OK);
        TC_CHECK(d == NULL);
        tc_free(d);
    }
}

/* ------------------------------------------------------------------ */
/* Structure and errors                                               */
/* ------------------------------------------------------------------ */

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

/* Unknown elements are ignored, like the serialiser does for elements it has no
 * property for. */
static void test_unknown_elements_are_ignored(void) {
    const char *doc =
        "<Chapters><EditionEntry><ChapterAtom>"
        "<ChapterTimeStart>00:00:00.000</ChapterTimeStart>"
        "<Bogus>x</Bogus>"
        "<ChapterDisplay><ChapterString>n</ChapterString></ChapterDisplay>"
        "</ChapterAtom></EditionEntry></Chapters>";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(doc, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "n");
    tc_free(d);
}

static void test_malformed_xml_is_rejected(void) {
    const char *cases[] = {
        "",                                        /* empty input */
        "not xml at all",                          /* no element */
        "<Chapters><EditionEntry>",                /* unterminated */
        "<Chapters><EditionEntry></Chapters>",     /* mismatched close */
        "<Chapters attr></Chapters>",              /* attribute without value */
        "<Chapters><EditionEntry><ChapterAtom>",   /* unterminated atom */
    };
    for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
        tc_data *d = NULL;
        tc_status st = parse_text(cases[i], &d);
        TC_CHECK(st != TC_OK);
        TC_CHECK(d == NULL);
        tc_free(d);
    }
}

/* The serialiser is case-sensitive, does not accept a namespaced root, and
 * binds the document element itself: a wrapper around <Chapters> is a different
 * document. */
static void test_wrong_root_is_rejected(void) {
    const char *cases[] = {
        "<XPL><title name=\"x\"/></XPL>",
        "<MatroskaChapters><EditionEntry/></MatroskaChapters>",
        "<chapters><EditionEntry/></chapters>",
        "<Chapters xmlns=\"http://www.matroska.org/ns\"><EditionEntry/></Chapters>",
        "<ns:Chapters xmlns:ns=\"urn:x\"><EditionEntry/></ns:Chapters>",
        "<root><Chapters><EditionEntry/></Chapters></root>",
    };
    for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
        tc_data *d = NULL;
        tc_status st = parse_text(cases[i], &d);
        TC_CHECK(st != TC_OK);
        TC_CHECK(d == NULL);
        tc_free(d);
    }
}

/* A namespace declaration that is never used does not put the elements in a
 * namespace, so the serialiser still binds them. The mini XML reader keeps the
 * declaration as an attribute and the root check only looks at "xmlns". */
static void test_unused_namespace_declaration(void) {
    const char *doc =
        "<Chapters xmlns:ns=\"urn:x\"><EditionEntry>"
        "<ChapterAtom><ChapterTimeStart>00:00:01.000</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>x</ChapterString></ChapterDisplay></ChapterAtom>"
        "</EditionEntry></Chapters>";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(doc, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "x");
    tc_free(d);
}

/* The reference rejects a document that carries anything after the root
 * element. The mini XML reader stops at the root and ignores the rest, so this
 * parser accepts those files; the divergence is in the frozen B1 reader and is
 * recorded here rather than worked around. */
static void test_trailing_content_after_root(void) {
    const char *doc =
        "<Chapters><EditionEntry>"
        "<ChapterAtom><ChapterTimeStart>00:00:01.000</ChapterTimeStart>"
        "<ChapterDisplay><ChapterString>x</ChapterString></ChapterDisplay></ChapterAtom>"
        "</EditionEntry></Chapters><junk/>";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(doc, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 1);
    TC_CHECK_EQ_STR(chapter_name(d, 0, 0), "x");
    tc_free(d);
}

/* Unknown attributes on the root are ignored, like the serialiser does for
 * attributes it has no member for. */
static void test_root_attributes_are_ignored(void) {
    const char *doc = "<Chapters foo=\"bar\"><EditionEntry/></Chapters>";
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(parse_text(doc, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    tc_free(d);
}

static void test_bad_arguments(void) {
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_mem(NULL, 0, TC_FMT_XML, NULL, &d), TC_E_INVALID);
    TC_CHECK_EQ_INT(tc_parse_mem("x", 1, TC_FMT_XML, NULL, NULL), TC_E_INVALID);
    TC_CHECK(d == NULL);

    /* A missing file is an I/O failure, not a format failure. */
    TC_CHECK_EQ_INT(tc_parse_file("/definitely/not/here.xml", TC_FMT_XML, &d), TC_E_IO);
    TC_CHECK(d == NULL);
}

/* ------------------------------------------------------------------ */
/* Suite                                                              */
/* ------------------------------------------------------------------ */

TC_SUITE(xmlchapters) {
    TC_CASE("sub_chapter_asset");  test_sub_chapter_asset();
    TC_CASE("ordered_asset");      test_ordered_chapter_asset();
    TC_CASE("multi_display");      test_multiple_displays();
    TC_CASE("no_chapter_string");  test_missing_chapter_string();
    TC_CASE("no_display");         test_no_display_is_empty_name();
    TC_CASE("entities");           test_entities_and_cdata();
    TC_CASE("timecode_forms");     test_timecode_forms();
    TC_CASE("time_end");           test_chapter_time_end();
    TC_CASE("time_end_only");      test_time_end_only();
    TC_CASE("editions");           test_multiple_editions();
    TC_CASE("edition_empty");      test_edition_without_atoms_is_an_entry();
    TC_CASE("empty_chapters");     test_empty_chapters_is_rejected();
    TC_CASE("comments");           test_comments_and_declaration();
    TC_CASE("nested_atoms");       test_deeply_nested_atoms();
    TC_CASE("unknown_elements");   test_unknown_elements_are_ignored();
    TC_CASE("malformed");          test_malformed_xml_is_rejected();
    TC_CASE("wrong_root");         test_wrong_root_is_rejected();
    TC_CASE("unused_xmlns");       test_unused_namespace_declaration();
    TC_CASE("trailing_content");   test_trailing_content_after_root();
    TC_CASE("root_attributes");    test_root_attributes_are_ignored();
    TC_CASE("bad_arguments");      test_bad_arguments();
}
