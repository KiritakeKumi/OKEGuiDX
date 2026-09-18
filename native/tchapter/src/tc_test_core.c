/*
 * Core test suite for libtchapter: utilities, the XML reader, the text writers
 * and the public API.
 *
 * Parser-specific suites live in their own files and register themselves
 * independently (see tc_test.h), so this file is not touched when a parser is
 * added.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_test.h"

/* ------------------------------------------------------------------ */
/* Version and status                                                 */
/* ------------------------------------------------------------------ */

static void test_version(void) {
    TC_CHECK_EQ_STR(tc_version(), "1.0.0");
    TC_CHECK_EQ_STR(tc_status_name(TC_OK), "OK");
    TC_CHECK_EQ_STR(tc_status_name(TC_E_FORMAT), "E_FORMAT");
    TC_CHECK_EQ_STR(tc_status_name((tc_status)999), "E_UNKNOWN");
    TC_CHECK_EQ_STR(tc_format_extension(TC_FMT_OGM), ".txt");
    TC_CHECK_EQ_STR(tc_format_extension(TC_FMT_AUTO), "");
}

/* ------------------------------------------------------------------ */
/* Byte readers                                                       */
/* ------------------------------------------------------------------ */

static void test_byte_readers(void) {
    const uint8_t buf[] = {0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08};
    TC_CHECK_EQ_INT(tc_be8(buf), 0x01);
    TC_CHECK_EQ_INT(tc_be16(buf), 0x0102);
    TC_CHECK_EQ_INT(tc_be24(buf), 0x010203);
    TC_CHECK_EQ_INT(tc_be32(buf), 0x01020304);
    TC_CHECK_EQ_INT(tc_be64(buf), 0x0102030405060708LL);
    TC_CHECK_EQ_INT(tc_le16(buf), 0x0201);
    TC_CHECK_EQ_INT(tc_le32(buf), 0x04030201);
}

/* ------------------------------------------------------------------ */
/* Text helpers                                                       */
/* ------------------------------------------------------------------ */

static void test_text_helpers(void) {
    char a[] = "  hello  ";
    TC_CHECK_EQ_STR(tc_trim(a), "hello");

    char b[] = "line\r\n";
    tc_chomp(b);
    TC_CHECK_EQ_STR(b, "line");

    TC_CHECK(tc_ieq("ABC", "abc"));
    TC_CHECK(!tc_ieq("ABC", "abd"));
    TC_CHECK(tc_istarts_with("ChapterAtom", "chapter"));
    TC_CHECK(!tc_istarts_with("ChapterAtom", "atom"));
}

static void test_timestamp_parsing(void) {
    int64_t ns = 0;

    TC_CHECK_EQ_INT(tc_parse_timestamp("00:00:00.000", &ns), TC_OK);
    TC_CHECK_EQ_INT(ns, 0);

    TC_CHECK_EQ_INT(tc_parse_timestamp("00:00:01.000", &ns), TC_OK);
    TC_CHECK_EQ_INT(ns, 1000000000LL);

    TC_CHECK_EQ_INT(tc_parse_timestamp("01:02:03.456", &ns), TC_OK);
    TC_CHECK_EQ_INT(ns, ((1 * 3600 + 2 * 60 + 3) * 1000000000LL) + 456000000LL);

    /* Comma is the SRT/VTT decimal separator. */
    TC_CHECK_EQ_INT(tc_parse_timestamp("00:00:05,500", &ns), TC_OK);
    TC_CHECK_EQ_INT(ns, 5500000000LL);

    /* Microsecond precision must not be truncated to milliseconds. */
    TC_CHECK_EQ_INT(tc_parse_timestamp("00:00:00.123456", &ns), TC_OK);
    TC_CHECK_EQ_INT(ns, 123456000LL);

    /* Hours may exceed 24 for long playlists. */
    TC_CHECK_EQ_INT(tc_parse_timestamp("100:00:00.000", &ns), TC_OK);
    TC_CHECK_EQ_INT(ns, 360000000000000LL);

    /* Malformed inputs are rejected rather than guessed at. */
    TC_CHECK_EQ_INT(tc_parse_timestamp("", &ns), TC_E_FORMAT);
    TC_CHECK_EQ_INT(tc_parse_timestamp("abc", &ns), TC_E_FORMAT);
    TC_CHECK_EQ_INT(tc_parse_timestamp("00:00", &ns), TC_E_FORMAT);
    TC_CHECK_EQ_INT(tc_parse_timestamp("00:00:00", &ns), TC_OK);
    TC_CHECK_EQ_INT(tc_parse_timestamp("00:61:00.000", &ns), TC_E_FORMAT);
    TC_CHECK_EQ_INT(tc_parse_timestamp("00:00:61.000", &ns), TC_E_FORMAT);
    TC_CHECK_EQ_INT(tc_parse_timestamp("00:00:00.x", &ns), TC_E_FORMAT);
}

static void test_timestamp_formatting(void) {
    char buf[32];

    tc_format_timestamp(0, buf, sizeof(buf));
    TC_CHECK_EQ_STR(buf, "00:00:00.000");

    tc_format_timestamp(1000000000LL, buf, sizeof(buf));
    TC_CHECK_EQ_STR(buf, "00:00:01.000");

    tc_format_timestamp(((1 * 3600 + 2 * 60 + 3) * 1000000000LL) + 456000000LL, buf, sizeof(buf));
    TC_CHECK_EQ_STR(buf, "01:02:03.456");

    tc_format_timestamp(3661000000000LL, buf, sizeof(buf));
    TC_CHECK_EQ_STR(buf, "01:01:01.000");
}

static void test_timestamp_round_trip(void) {
    const char *samples[] = {
        "00:00:00.000", "00:00:01.000", "01:02:03.456", "23:59:59.999",
    };
    for (size_t i = 0; i < sizeof(samples) / sizeof(samples[0]); i++) {
        int64_t ns = 0;
        char buf[32];
        TC_CHECK_EQ_INT(tc_parse_timestamp(samples[i], &ns), TC_OK);
        tc_format_timestamp(ns, buf, sizeof(buf));
        TC_CHECK_EQ_STR(buf, samples[i]);
    }
}

/* ------------------------------------------------------------------ */
/* Buffer                                                             */
/* ------------------------------------------------------------------ */

static void test_buffer_growth(void) {
    tc_buf b;
    tc_buf_init(&b);
    for (int i = 0; i < 1000; i++) {
        tc_buf_puts(&b, "0123456789");
    }
    TC_CHECK(!b.oom);
    TC_CHECK_EQ_INT(b.len, 10000);
    TC_CHECK_EQ_INT(strlen(b.data), 10000);
    TC_CHECK(b.cap >= 10001);

    tc_buf_printf(&b, "-%d-%s", 42, "tail");
    TC_CHECK_CONTAINS(b.data, "-42-tail");
    tc_buf_free(&b);
    TC_CHECK(b.data == NULL);
}

static void test_integer_formatting(void) {
    /* %zu and %lld are not portable to MSVCRT, so these helpers carry the
     * decimal conversions the writers need. */
    tc_buf b;
    tc_buf_init(&b);
    tc_buf_put_u64(&b, 0, 0);
    tc_buf_putc(&b, ',');
    tc_buf_put_u64(&b, 7, 3);
    tc_buf_putc(&b, ',');
    tc_buf_put_u64(&b, 1234567890ULL, 0);
    tc_buf_putc(&b, ',');
    tc_buf_put_i64(&b, -42, 0);
    tc_buf_putc(&b, ',');
    tc_buf_put_i64(&b, -7, 3);
    TC_CHECK_EQ_STR(b.data, "0,007,1234567890,-42,-07");
    tc_buf_free(&b);

    /* The most negative value must not overflow when negated. */
    tc_buf_init(&b);
    tc_buf_put_i64(&b, (int64_t)(-9223372036854775807LL - 1), 0);
    TC_CHECK_EQ_STR(b.data, "-9223372036854775808");
    tc_buf_free(&b);
}

/* ------------------------------------------------------------------ */
/* Mini XML parser                                                    */
/* ------------------------------------------------------------------ */

static void test_xml_basic(void) {
    const char *doc =
        "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"
        "<Chapters>\n"
        "  <EditionEntry>\n"
        "    <ChapterAtom>\n"
        "      <ChapterTimeStart>00:00:00.000</ChapterTimeStart>\n"
        "      <ChapterDisplay>\n"
        "        <ChapterString>Opening</ChapterString>\n"
        "      </ChapterDisplay>\n"
        "    </ChapterAtom>\n"
        "  </EditionEntry>\n"
        "</Chapters>\n";

    tc_xml_node *root = NULL;
    TC_CHECK_EQ_INT(tc_xml_parse(doc, strlen(doc), &root), TC_OK);
    if (!root) {
        return;
    }

    const tc_xml_node *chapters = tc_xml_child(root, "Chapters");
    TC_CHECK(chapters != NULL);

    const tc_xml_node *edition = tc_xml_child(chapters, "EditionEntry");
    TC_CHECK(edition != NULL);

    const tc_xml_node *atom = tc_xml_child(edition, "ChapterAtom");
    TC_CHECK(atom != NULL);

    const tc_xml_node *start = tc_xml_child(atom, "ChapterTimeStart");
    TC_CHECK(start != NULL);

    tc_buf text;
    tc_buf_init(&text);
    tc_xml_text(start, &text);
    TC_CHECK_EQ_STR(text.data, "00:00:00.000");
    tc_buf_free(&text);

    const tc_xml_node *display = tc_xml_child(atom, "ChapterDisplay");
    const tc_xml_node *str = tc_xml_child(display, "ChapterString");
    tc_buf_init(&text);
    tc_xml_text(str, &text);
    TC_CHECK_EQ_STR(text.data, "Opening");
    tc_buf_free(&text);

    tc_xml_free(root);
}

static void test_xml_attributes_and_entities(void) {
    const char *doc =
        "<XPL>\n"
        "  <title name=\"Show &amp; Tell\">\n"
        "    <chapter name=\"A &lt; B\" time=\"00:00:10.000\" />\n"
        "    <chapter name=\"&#65;&#x42;C\" time=\"00:00:20.000\" />\n"
        "  </title>\n"
        "</XPL>\n";

    tc_xml_node *root = NULL;
    TC_CHECK_EQ_INT(tc_xml_parse(doc, strlen(doc), &root), TC_OK);
    if (!root) {
        return;
    }
    const tc_xml_node *xpl = tc_xml_child(root, "XPL");
    TC_CHECK(xpl != NULL);
    if (!xpl) {
        tc_xml_free(root);
        return;
    }
    const tc_xml_node *title = tc_xml_child(xpl, "title");
    TC_CHECK(title != NULL);
    if (!title) {
        tc_xml_free(root);
        return;
    }
    TC_CHECK_EQ_STR(tc_xml_attr(title, "name"), "Show & Tell");

    const tc_xml_node *ch0 = tc_xml_child(title, "chapter");
    TC_CHECK(ch0 != NULL);
    if (!ch0) {
        tc_xml_free(root);
        return;
    }
    TC_CHECK_EQ_STR(tc_xml_attr(ch0, "name"), "A < B");
    TC_CHECK_EQ_STR(tc_xml_attr(ch0, "time"), "00:00:10.000");
    TC_CHECK(tc_xml_attr(ch0, "missing") == NULL);
    TC_CHECK_EQ_STR(tc_xml_attr_or(ch0, "missing", "fallback"), "fallback");

    /* The second chapter is found by scanning the title's children, because
     * text nodes are interleaved in the child array. */
    const tc_xml_node *ch1 = NULL;
    int seen_first = 0;
    for (size_t i = 0; i < title->child_count; i++) {
        const tc_xml_node *c = &title->children[i];
        if (c->kind != TC_XML_ELEMENT || !c->name || strcmp(c->name, "chapter") != 0) {
            continue;
        }
        if (seen_first) {
            ch1 = c;
            break;
        }
        seen_first = 1;
    }
    TC_CHECK(ch1 != NULL);
    if (ch1) {
        TC_CHECK_EQ_STR(tc_xml_attr(ch1, "name"), "ABC");
    }

    tc_xml_free(root);
}

static void test_xml_comments_cdata_and_doctype(void) {
    const char *doc =
        "<?xml version=\"1.0\"?>\n"
        "<!DOCTYPE chapters [<!ELEMENT chapters ANY>]>\n"
        "<!-- a comment -->\n"
        "<chapters>\n"
        "  <!-- another -->\n"
        "  <name><![CDATA[Raw <text> & stuff]]></name>\n"
        "</chapters>\n";

    tc_xml_node *root = NULL;
    TC_CHECK_EQ_INT(tc_xml_parse(doc, strlen(doc), &root), TC_OK);
    if (!root) {
        return;
    }
    const tc_xml_node *chapters = tc_xml_child(root, "chapters");
    TC_CHECK(chapters != NULL);
    const tc_xml_node *name = tc_xml_child(chapters, "name");
    TC_CHECK(name != NULL);

    tc_buf text;
    tc_buf_init(&text);
    tc_xml_text(name, &text);
    TC_CHECK_EQ_STR(text.data, "Raw <text> & stuff");
    tc_buf_free(&text);
    tc_xml_free(root);
}

static void test_xml_utf8_bom(void) {
    const char *doc = "\xEF\xBB\xBF<root><a>x</a></root>";
    tc_xml_node *root = NULL;
    TC_CHECK_EQ_INT(tc_xml_parse(doc, strlen(doc), &root), TC_OK);
    if (!root) {
        return;
    }
    TC_CHECK(tc_xml_child(root, "root") != NULL);
    tc_xml_free(root);
}

static void test_xml_rejects_malformed(void) {
    const char *bad[] = {
        "<a><b></a>",        /* mismatched close */
        "<a>",               /* unterminated */
        "<a attr>",          /* attribute without value */
        "<a attr=>",         /* unquoted value */
        "no root element",   /* content before root */
        "<a attr=\"x\"></a", /* unterminated tag */
    };
    for (size_t i = 0; i < sizeof(bad) / sizeof(bad[0]); i++) {
        tc_xml_node *root = NULL;
        tc_status st = tc_xml_parse(bad[i], strlen(bad[i]), &root);
        TC_CHECK(st != TC_OK);
        TC_CHECK(root == NULL);
        if (root) {
            tc_xml_free(root);
        }
    }
}

static void test_xml_empty_is_rejected(void) {
    tc_xml_node *root = NULL;
    TC_CHECK(tc_xml_parse("", 0, &root) != TC_OK);
    TC_CHECK(root == NULL);
}

/* ------------------------------------------------------------------ */
/* Format detection                                                   */
/* ------------------------------------------------------------------ */

static void test_format_detection(void) {
    /* A file that cannot be opened falls back to the extension hint, which is
     * what lets TC_FMT_AUTO work for text formats that have no signature. */
    TC_CHECK_EQ_INT(tc_detect_from_file("/definitely/not/here.mpls"), TC_FMT_MPLS);
    TC_CHECK_EQ_INT(tc_detect_from_file("/definitely/not/here.cue"), TC_FMT_CUE);
    TC_CHECK_EQ_INT(tc_detect_from_file("/definitely/not/here.unknown"), TC_FMT_AUTO);
    TC_CHECK_EQ_INT(tc_detect_from_file(NULL), TC_FMT_AUTO);
}

/* ------------------------------------------------------------------ */
/* API error handling                                                 */
/* ------------------------------------------------------------------ */

static void test_api_rejects_bad_arguments(void) {
    tc_data *d = NULL;

    TC_CHECK_EQ_INT(tc_parse_file(NULL, TC_FMT_AUTO, &d), TC_E_INVALID);
    TC_CHECK_EQ_INT(tc_parse_file("x", TC_FMT_AUTO, NULL), TC_E_INVALID);

    TC_CHECK_EQ_INT(tc_entry_count(NULL), 0);
    TC_CHECK_EQ_INT(tc_chapter_count(NULL, 0), 0);

    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(NULL, 0, &ei), TC_E_INVALID);

    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(NULL, 0, 0, &c), TC_E_INVALID);
}

static void test_last_error_is_set(void) {
    tc_data *d = NULL;
    (void)tc_parse_file(NULL, TC_FMT_AUTO, &d);
    TC_CHECK(tc_last_error() != NULL);
    TC_CHECK(strlen(tc_last_error()) > 0);
}

/* ------------------------------------------------------------------ */
/* Writers                                                            */
/* ------------------------------------------------------------------ */

/* Builds a small entry by hand so the writers can be tested independently of
 * any parser. */
static tc_data *make_sample(void) {
    tc_data *d = tc_calloc(1, sizeof(*d));
    if (!d) {
        return NULL;
    }
    tc_entry *e = tc_data_add_entry(d);
    if (!e) {
        tc_data_clear(d);
        return NULL;
    }
    e->title = tc_strdup("Episode 01");
    e->source = tc_strdup("00000.m2ts");
    e->fps_num = 24000;
    e->fps_den = 1001;
    e->duration_ns = 1440000000000LL; /* 24 minutes */

    tc_entry_add_chapter(e, tc_strdup("Opening"), 0, 0);
    tc_entry_add_chapter(e, tc_strdup("Part A"), 90000000000LL, 0);
    tc_entry_add_chapter(e, tc_strdup("Ending"), 1380000000000LL, 0);
    return d;
}

static void test_writer_ogm(void) {
    tc_data *d = make_sample();
    TC_CHECK(d != NULL);
    if (!d) {
        return;
    }

    size_t need = 0;
    TC_CHECK_EQ_INT(tc_render(d, 0, TC_FMT_OGM, NULL, NULL, NULL, &need), TC_E_RANGE);
    TC_CHECK(need > 1);

    char *buf = malloc(need);
    TC_CHECK(buf != NULL);
    if (buf) {
        size_t len = need;
        TC_CHECK_EQ_INT(tc_render(d, 0, TC_FMT_OGM, NULL, NULL, buf, &len), TC_OK);
        TC_CHECK_CONTAINS(buf, "CHAPTER01=00:00:00.000");
        TC_CHECK_CONTAINS(buf, "CHAPTER01NAME=Opening");
        TC_CHECK_CONTAINS(buf, "CHAPTER02=00:01:30.000");
        TC_CHECK_CONTAINS(buf, "CHAPTER03=00:23:00.000");
        free(buf);
    }
    tc_free(d);
}

static void test_writer_ogm_names_default(void) {
    tc_data *d = tc_calloc(1, sizeof(*d));
    tc_entry *e = tc_data_add_entry(d);
    e->fps_den = 1;
    tc_entry_add_chapter(e, NULL, 0, 0);

    size_t need = 0;
    (void)tc_render(d, 0, TC_FMT_OGM, NULL, NULL, NULL, &need);
    char *buf = malloc(need);
    if (buf) {
        size_t len = need;
        TC_CHECK_EQ_INT(tc_render(d, 0, TC_FMT_OGM, NULL, NULL, buf, &len), TC_OK);
        TC_CHECK_CONTAINS(buf, "NAME=Chapter 01");
        free(buf);
    }
    tc_free(d);
}

static void test_writer_xml_escapes(void) {
    tc_data *d = tc_calloc(1, sizeof(*d));
    tc_entry *e = tc_data_add_entry(d);
    e->fps_den = 1;
    tc_entry_add_chapter(e, tc_strdup("A & B < C"), 0, 0);

    size_t need = 0;
    (void)tc_render(d, 0, TC_FMT_XML, "jpn", NULL, NULL, &need);
    char *buf = malloc(need);
    if (buf) {
        size_t len = need;
        TC_CHECK_EQ_INT(tc_render(d, 0, TC_FMT_XML, "jpn", NULL, buf, &len), TC_OK);
        TC_CHECK_CONTAINS(buf, "<ChapterString>A &amp; B &lt; C</ChapterString>");
        TC_CHECK_CONTAINS(buf, "<ChapterLanguage>jpn</ChapterLanguage>");
        TC_CHECK_CONTAINS(buf, "<Chapters>");
        free(buf);
    }
    tc_free(d);
}

static void test_writer_vtt_cue_durations(void) {
    tc_data *d = make_sample();
    if (!d) {
        TC_CHECK(0);
        return;
    }
    size_t need = 0;
    (void)tc_render(d, 0, TC_FMT_VTT, NULL, NULL, NULL, &need);
    char *buf = malloc(need);
    if (buf) {
        size_t len = need;
        TC_CHECK_EQ_INT(tc_render(d, 0, TC_FMT_VTT, NULL, NULL, buf, &len), TC_OK);
        TC_CHECK(strncmp(buf, "WEBVTT", 6) == 0);
        /* Cue 1 runs from chapter 1's start to chapter 2's start. */
        TC_CHECK_CONTAINS(buf, "00:00:00.000 --> 00:01:30.000");
        /* The last cue ends at the entry duration. */
        TC_CHECK_CONTAINS(buf, "00:23:00.000 --> 00:24:00.000");
        free(buf);
    }
    tc_free(d);
}

static void test_writer_xpl(void) {
    tc_data *d = make_sample();
    if (!d) {
        TC_CHECK(0);
        return;
    }
    size_t need = 0;
    (void)tc_render(d, 0, TC_FMT_XPL, NULL, "00000.m2ts", NULL, &need);
    char *buf = malloc(need);
    if (buf) {
        size_t len = need;
        TC_CHECK_EQ_INT(tc_render(d, 0, TC_FMT_XPL, NULL, "00000.m2ts", buf, &len), TC_OK);
        TC_CHECK_CONTAINS(buf, "<XPL>");
        TC_CHECK_CONTAINS(buf, "name=\"00000.m2ts\"");
        TC_CHECK_CONTAINS(buf, "<chapter name=\"Opening\" time=\"00:00:00.000\" />");
        free(buf);
    }
    tc_free(d);
}

static void test_writer_cue(void) {
    tc_data *d = make_sample();
    if (!d) {
        TC_CHECK(0);
        return;
    }
    size_t need = 0;
    (void)tc_render(d, 0, TC_FMT_CUE, "jpn", "track.wav", NULL, &need);
    char *buf = malloc(need);
    if (buf) {
        size_t len = need;
        TC_CHECK_EQ_INT(tc_render(d, 0, TC_FMT_CUE, "jpn", "track.wav", buf, &len), TC_OK);
        TC_CHECK_CONTAINS(buf, "FILE \"track.wav\" WAVE");
        TC_CHECK_CONTAINS(buf, "TRACK 01 AUDIO");
        TC_CHECK_CONTAINS(buf, "INDEX 01 00:00:00.000");
        free(buf);
    }
    tc_free(d);
}

static void test_writer_rejects_binary_targets(void) {
    tc_data *d = make_sample();
    if (!d) {
        TC_CHECK(0);
        return;
    }
    size_t need = 0;
    TC_CHECK_EQ_INT(tc_render(d, 0, TC_FMT_MPLS, NULL, NULL, NULL, &need), TC_E_UNSUPPORTED);
    TC_CHECK_EQ_INT(tc_render(d, 0, TC_FMT_MP4, NULL, NULL, NULL, &need), TC_E_UNSUPPORTED);
    TC_CHECK_EQ_INT(tc_render(d, 0, TC_FMT_TAK, NULL, NULL, NULL, &need), TC_E_UNSUPPORTED);
    tc_free(d);
}

static void test_render_small_buffer_reports_size(void) {
    tc_data *d = make_sample();
    if (!d) {
        TC_CHECK(0);
        return;
    }
    char small[4];
    size_t len = sizeof(small);
    TC_CHECK_EQ_INT(tc_render(d, 0, TC_FMT_OGM, NULL, NULL, small, &len), TC_E_RANGE);
    TC_CHECK(len > sizeof(small)); /* the required size is reported back */
    tc_free(d);
}

static void test_render_rejects_bad_entry(void) {
    tc_data *d = make_sample();
    if (!d) {
        TC_CHECK(0);
        return;
    }
    size_t need = 0;
    TC_CHECK_EQ_INT(tc_render(d, 99, TC_FMT_OGM, NULL, NULL, NULL, &need), TC_E_RANGE);
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Save to disk                                                       */
/* ------------------------------------------------------------------ */

static void test_save_writes_a_file(void) {
    tc_data *d = make_sample();
    if (!d) {
        TC_CHECK(0);
        return;
    }
    const char *path = "build/test_output.txt";
    TC_CHECK_EQ_INT(tc_save(d, 0, TC_FMT_OGM, path, NULL, NULL), TC_OK);

    FILE *f = fopen(path, "rb");
    TC_CHECK(f != NULL);
    if (f) {
        char head[8] = {0};
        size_t n = fread(head, 1, 3, f);
        /* Chapter files are written with a UTF-8 BOM for Windows consumers. */
        TC_CHECK_EQ_INT(n, 3);
        TC_CHECK((unsigned char)head[0] == 0xEF);
        TC_CHECK((unsigned char)head[1] == 0xBB);
        TC_CHECK((unsigned char)head[2] == 0xBF);
        fclose(f);
    }
    remove(path);
    tc_free(d);
}

static void test_save_rejects_unwritable_path(void) {
    tc_data *d = make_sample();
    if (!d) {
        TC_CHECK(0);
        return;
    }
    TC_CHECK_EQ_INT(tc_save(d, 0, TC_FMT_OGM, "/definitely/not/a/dir/out.txt", NULL, NULL), TC_E_IO);
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Entry / chapter accessors                                          */
/* ------------------------------------------------------------------ */

static void test_accessors(void) {
    tc_data *d = make_sample();
    if (!d) {
        TC_CHECK(0);
        return;
    }

    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    TC_CHECK_EQ_INT(tc_chapter_count(d, 0), 3);

    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.title, "Episode 01");
    TC_CHECK_EQ_STR(ei.source, "00000.m2ts");
    TC_CHECK_EQ_INT(ei.fps_num, 24000);
    TC_CHECK_EQ_INT(ei.fps_den, 1001);
    TC_CHECK_EQ_INT(ei.duration_ns, 1440000000000LL);
    TC_CHECK_EQ_INT(ei.chapter_count, 3);

    tc_chapter_t c;
    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    TC_CHECK_EQ_STR(c.name, "Opening");
    TC_CHECK_EQ_INT(c.time_ns, 0);

    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 1, &c), TC_OK);
    TC_CHECK_EQ_STR(c.name, "Part A");
    TC_CHECK_EQ_INT(c.time_ns, 90000000000LL);

    TC_CHECK_EQ_INT(tc_chapter_at(d, 0, 3, &c), TC_E_RANGE);
    TC_CHECK_EQ_INT(tc_entry_info(d, 1, &ei), TC_E_RANGE);

    tc_free(d);
}

static void test_free_accepts_null(void) {
    tc_free(NULL); /* must not crash */
    TC_CHECK(1);
}

/* ------------------------------------------------------------------ */
/* Suite                                                              */
/* ------------------------------------------------------------------ */

TC_SUITE(core) {
    TC_CASE("version");               test_version();
    TC_CASE("byte_readers");          test_byte_readers();
    TC_CASE("text_helpers");          test_text_helpers();
    TC_CASE("timestamp_parsing");     test_timestamp_parsing();
    TC_CASE("timestamp_formatting");  test_timestamp_formatting();
    TC_CASE("timestamp_round_trip");  test_timestamp_round_trip();
    TC_CASE("buffer_growth");         test_buffer_growth();
    TC_CASE("integer_formatting");    test_integer_formatting();
    TC_CASE("xml_basic");             test_xml_basic();
    TC_CASE("xml_attributes");        test_xml_attributes_and_entities();
    TC_CASE("xml_oddities");          test_xml_comments_cdata_and_doctype();
    TC_CASE("xml_bom");               test_xml_utf8_bom();
    TC_CASE("xml_malformed");         test_xml_rejects_malformed();
    TC_CASE("xml_empty");             test_xml_empty_is_rejected();
    TC_CASE("format_detection");      test_format_detection();
    TC_CASE("api_arguments");         test_api_rejects_bad_arguments();
    TC_CASE("last_error");            test_last_error_is_set();
    TC_CASE("writer_ogm");            test_writer_ogm();
    TC_CASE("writer_ogm_default_name"); test_writer_ogm_names_default();
    TC_CASE("writer_xml");            test_writer_xml_escapes();
    TC_CASE("writer_vtt");            test_writer_vtt_cue_durations();
    TC_CASE("writer_xpl");            test_writer_xpl();
    TC_CASE("writer_cue");            test_writer_cue();
    TC_CASE("writer_unsupported");    test_writer_rejects_binary_targets();
    TC_CASE("render_small_buffer");   test_render_small_buffer_reports_size();
    TC_CASE("render_bad_entry");      test_render_rejects_bad_entry();
    TC_CASE("save_file");             test_save_writes_a_file();
    TC_CASE("save_bad_path");         test_save_rejects_unwritable_path();
    TC_CASE("accessors");             test_accessors();
    TC_CASE("free_null");             test_free_accepts_null();
}
