/*
 * Test suite for libtchapter.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * A deliberately dependency-free harness: a real test framework would have to
 * be vendored or fetched, and the acceptance criterion for this library is
 * simply "these cases pass on all five targets". Each parser package adds its
 * own cases here, mirroring TChapter.Test.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_internal.h"

static int g_failures = 0;
static int g_checks = 0;
static const char *g_case = "";

#define CHECK(cond)                                                          \
    do {                                                                     \
        g_checks++;                                                          \
        if (!(cond)) {                                                       \
            g_failures++;                                                    \
            fprintf(stderr, "FAIL %s:%d [%s] %s\n", __FILE__, __LINE__,      \
                    g_case, #cond);                                          \
        }                                                                    \
    } while (0)

#define CHECK_EQ_INT(a, b)                                                   \
    do {                                                                     \
        long long va_ = (long long)(a), vb_ = (long long)(b);                \
        g_checks++;                                                          \
        if (va_ != vb_) {                                                    \
            g_failures++;                                                    \
            fprintf(stderr, "FAIL %s:%d [%s] %s = %lld, want %lld\n",        \
                    __FILE__, __LINE__, g_case, #a, va_, vb_);               \
        }                                                                    \
    } while (0)

#define CHECK_EQ_STR(a, b)                                                   \
    do {                                                                     \
        const char *sa_ = (a), *sb_ = (b);                                   \
        g_checks++;                                                          \
        if (!sa_ || !sb_ || strcmp(sa_, sb_) != 0) {                         \
            g_failures++;                                                    \
            fprintf(stderr, "FAIL %s:%d [%s] %s = \"%s\", want \"%s\"\n",    \
                    __FILE__, __LINE__, g_case, #a, sa_ ? sa_ : "(null)",    \
                    sb_ ? sb_ : "(null)");                                   \
        }                                                                    \
    } while (0)

/* ------------------------------------------------------------------ */
/* Version and status                                                 */
/* ------------------------------------------------------------------ */

static void test_version(void) {
    g_case = "version";
    CHECK_EQ_STR(tc_version(), "1.0.0");
    CHECK_EQ_STR(tc_status_name(TC_OK), "OK");
    CHECK_EQ_STR(tc_status_name(TC_E_FORMAT), "E_FORMAT");
    CHECK_EQ_STR(tc_status_name((tc_status)999), "E_UNKNOWN");
    CHECK_EQ_STR(tc_format_extension(TC_FMT_OGM), ".txt");
    CHECK_EQ_STR(tc_format_extension(TC_FMT_AUTO), "");
}

/* ------------------------------------------------------------------ */
/* Byte readers                                                       */
/* ------------------------------------------------------------------ */

static void test_byte_readers(void) {
    g_case = "byte_readers";
    const uint8_t buf[] = {0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08};
    CHECK_EQ_INT(tc_be8(buf), 0x01);
    CHECK_EQ_INT(tc_be16(buf), 0x0102);
    CHECK_EQ_INT(tc_be24(buf), 0x010203);
    CHECK_EQ_INT(tc_be32(buf), 0x01020304);
    CHECK_EQ_INT(tc_be64(buf), 0x0102030405060708LL);
    CHECK_EQ_INT(tc_le16(buf), 0x0201);
    CHECK_EQ_INT(tc_le32(buf), 0x04030201);
}

/* ------------------------------------------------------------------ */
/* Text helpers                                                       */
/* ------------------------------------------------------------------ */

static void test_text_helpers(void) {
    g_case = "text_helpers";

    char a[] = "  hello  ";
    CHECK_EQ_STR(tc_trim(a), "hello");

    char b[] = "line\r\n";
    tc_chomp(b);
    CHECK_EQ_STR(b, "line");

    CHECK(tc_ieq("ABC", "abc"));
    CHECK(!tc_ieq("ABC", "abd"));
    CHECK(tc_istarts_with("ChapterAtom", "chapter"));
    CHECK(!tc_istarts_with("ChapterAtom", "atom"));
}

static void test_timestamp_parsing(void) {
    g_case = "timestamp_parsing";
    int64_t ns = 0;

    CHECK_EQ_INT(tc_parse_timestamp("00:00:00.000", &ns), TC_OK);
    CHECK_EQ_INT(ns, 0);

    CHECK_EQ_INT(tc_parse_timestamp("00:00:01.000", &ns), TC_OK);
    CHECK_EQ_INT(ns, 1000000000LL);

    CHECK_EQ_INT(tc_parse_timestamp("01:02:03.456", &ns), TC_OK);
    CHECK_EQ_INT(ns, ((1 * 3600 + 2 * 60 + 3) * 1000000000LL) + 456000000LL);

    /* Comma is the SRT/VTT decimal separator. */
    CHECK_EQ_INT(tc_parse_timestamp("00:00:05,500", &ns), TC_OK);
    CHECK_EQ_INT(ns, 5500000000LL);

    /* Microsecond precision must not be truncated to milliseconds. */
    CHECK_EQ_INT(tc_parse_timestamp("00:00:00.123456", &ns), TC_OK);
    CHECK_EQ_INT(ns, 123456000LL);

    /* Hours may exceed 24 for long playlists. */
    CHECK_EQ_INT(tc_parse_timestamp("100:00:00.000", &ns), TC_OK);
    CHECK_EQ_INT(ns, 360000000000000LL);

    /* Malformed inputs are rejected rather than guessed at. */
    CHECK_EQ_INT(tc_parse_timestamp("", &ns), TC_E_FORMAT);
    CHECK_EQ_INT(tc_parse_timestamp("abc", &ns), TC_E_FORMAT);
    CHECK_EQ_INT(tc_parse_timestamp("00:00", &ns), TC_E_FORMAT);
    CHECK_EQ_INT(tc_parse_timestamp("00:00:00", &ns), TC_OK); /* no fraction is fine */
    CHECK_EQ_INT(tc_parse_timestamp("00:61:00.000", &ns), TC_E_FORMAT);
    CHECK_EQ_INT(tc_parse_timestamp("00:00:61.000", &ns), TC_E_FORMAT);
    CHECK_EQ_INT(tc_parse_timestamp("00:00:00.x", &ns), TC_E_FORMAT);
}

static void test_timestamp_formatting(void) {
    g_case = "timestamp_formatting";
    char buf[32];

    tc_format_timestamp(0, buf, sizeof(buf));
    CHECK_EQ_STR(buf, "00:00:00.000");

    tc_format_timestamp(1000000000LL, buf, sizeof(buf));
    CHECK_EQ_STR(buf, "00:00:01.000");

    tc_format_timestamp(((1 * 3600 + 2 * 60 + 3) * 1000000000LL) + 456000000LL, buf, sizeof(buf));
    CHECK_EQ_STR(buf, "01:02:03.456");

    tc_format_timestamp(3661000000000LL, buf, sizeof(buf));
    CHECK_EQ_STR(buf, "01:01:01.000");
}

static void test_timestamp_round_trip(void) {
    g_case = "timestamp_round_trip";
    const char *samples[] = {
        "00:00:00.000", "00:00:01.000", "01:02:03.456", "23:59:59.999",
    };
    for (size_t i = 0; i < sizeof(samples) / sizeof(samples[0]); i++) {
        int64_t ns = 0;
        char buf[32];
        CHECK_EQ_INT(tc_parse_timestamp(samples[i], &ns), TC_OK);
        tc_format_timestamp(ns, buf, sizeof(buf));
        CHECK_EQ_STR(buf, samples[i]);
    }
}

/* ------------------------------------------------------------------ */
/* Buffer                                                             */
/* ------------------------------------------------------------------ */

static void test_buffer_growth(void) {
    g_case = "buffer_growth";
    tc_buf b;
    tc_buf_init(&b);
    for (int i = 0; i < 1000; i++) {
        tc_buf_puts(&b, "0123456789");
    }
    CHECK(!b.oom);
    CHECK_EQ_INT(b.len, 10000);
    CHECK_EQ_INT(strlen(b.data), 10000);
    CHECK(b.cap >= 10001);

    tc_buf_printf(&b, "-%d-%s", 42, "tail");
    CHECK(strstr(b.data, "-42-tail") != NULL);
    tc_buf_free(&b);
    CHECK(b.data == NULL);
}

/* ------------------------------------------------------------------ */
/* Mini XML parser                                                    */
/* ------------------------------------------------------------------ */

static void test_xml_basic(void) {
    g_case = "xml_basic";
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
    CHECK_EQ_INT(tc_xml_parse(doc, strlen(doc), &root), TC_OK);
    if (!root) {
        return;
    }

    const tc_xml_node *chapters = tc_xml_child(root, "Chapters");
    CHECK(chapters != NULL);

    const tc_xml_node *edition = tc_xml_child(chapters, "EditionEntry");
    CHECK(edition != NULL);

    const tc_xml_node *atom = tc_xml_child(edition, "ChapterAtom");
    CHECK(atom != NULL);

    const tc_xml_node *start = tc_xml_child(atom, "ChapterTimeStart");
    CHECK(start != NULL);

    tc_buf text;
    tc_buf_init(&text);
    tc_xml_text(start, &text);
    CHECK_EQ_STR(text.data, "00:00:00.000");
    tc_buf_free(&text);

    const tc_xml_node *display = tc_xml_child(atom, "ChapterDisplay");
    const tc_xml_node *str = tc_xml_child(display, "ChapterString");
    tc_buf_init(&text);
    tc_xml_text(str, &text);
    CHECK_EQ_STR(text.data, "Opening");
    tc_buf_free(&text);

    tc_xml_free(root);
}

static void test_xml_attributes_and_entities(void) {
    g_case = "xml_attributes";
    const char *doc =
        "<XPL>\n"
        "  <title name=\"Show &amp; Tell\">\n"
        "    <chapter name=\"A &lt; B\" time=\"00:00:10.000\" />\n"
        "    <chapter name=\"&#65;&#x42;C\" time=\"00:00:20.000\" />\n"
        "  </title>\n"
        "</XPL>\n";

    tc_xml_node *root = NULL;
    CHECK_EQ_INT(tc_xml_parse(doc, strlen(doc), &root), TC_OK);
    if (!root) {
        return;
    }
    const tc_xml_node *xpl = tc_xml_child(root, "XPL");
    CHECK(xpl != NULL);
    if (!xpl) {
        tc_xml_free(root);
        return;
    }
    const tc_xml_node *title = tc_xml_child(xpl, "title");
    CHECK(title != NULL);
    if (!title) {
        tc_xml_free(root);
        return;
    }
    CHECK_EQ_STR(tc_xml_attr(title, "name"), "Show & Tell");

    const tc_xml_node *ch0 = tc_xml_child(title, "chapter");
    CHECK(ch0 != NULL);
    if (!ch0) {
        tc_xml_free(root);
        return;
    }
    CHECK_EQ_STR(tc_xml_attr(ch0, "name"), "A < B");
    CHECK_EQ_STR(tc_xml_attr(ch0, "time"), "00:00:10.000");
    CHECK(tc_xml_attr(ch0, "missing") == NULL);
    CHECK_EQ_STR(tc_xml_attr_or(ch0, "missing", "fallback"), "fallback");

    /* Numeric character references, both decimal and hex. The second chapter
     * is found by scanning the title's children for the next element, because
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
    CHECK(ch1 != NULL);
    if (ch1) {
        CHECK_EQ_STR(tc_xml_attr(ch1, "name"), "ABC");
    }

    tc_xml_free(root);
}

static void test_xml_comments_cdata_and_doctype(void) {
    g_case = "xml_oddities";
    const char *doc =
        "<?xml version=\"1.0\"?>\n"
        "<!DOCTYPE chapters [<!ELEMENT chapters ANY>]>\n"
        "<!-- a comment -->\n"
        "<chapters>\n"
        "  <!-- another -->\n"
        "  <name><![CDATA[Raw <text> & stuff]]></name>\n"
        "</chapters>\n";

    tc_xml_node *root = NULL;
    CHECK_EQ_INT(tc_xml_parse(doc, strlen(doc), &root), TC_OK);
    if (!root) {
        return;
    }
    const tc_xml_node *chapters = tc_xml_child(root, "chapters");
    CHECK(chapters != NULL);
    const tc_xml_node *name = tc_xml_child(chapters, "name");
    CHECK(name != NULL);

    tc_buf text;
    tc_buf_init(&text);
    tc_xml_text(name, &text);
    CHECK_EQ_STR(text.data, "Raw <text> & stuff");
    tc_buf_free(&text);
    tc_xml_free(root);
}

static void test_xml_utf8_bom(void) {
    g_case = "xml_bom";
    const char *doc = "\xEF\xBB\xBF<root><a>x</a></root>";
    tc_xml_node *root = NULL;
    CHECK_EQ_INT(tc_xml_parse(doc, strlen(doc), &root), TC_OK);
    if (!root) {
        return;
    }
    CHECK(tc_xml_child(root, "root") != NULL);
    tc_xml_free(root);
}

static void test_xml_rejects_malformed(void) {
    g_case = "xml_malformed";
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
        CHECK(st != TC_OK);
        CHECK(root == NULL);
        if (root) {
            tc_xml_free(root);
        }
    }
}

static void test_xml_empty_is_rejected(void) {
    g_case = "xml_empty";
    tc_xml_node *root = NULL;
    CHECK(tc_xml_parse("", 0, &root) != TC_OK);
    CHECK(root == NULL);
}

/* ------------------------------------------------------------------ */
/* Format detection                                                   */
/* ------------------------------------------------------------------ */

static void test_format_detection(void) {
    g_case = "format_detection";
    /* A file that cannot be opened falls back to the extension hint, which is
     * what lets TC_FMT_AUTO work for text formats that have no signature. */
    CHECK_EQ_INT(tc_detect_from_file("/definitely/not/here.mpls"), TC_FMT_MPLS);
    CHECK_EQ_INT(tc_detect_from_file("/definitely/not/here.cue"), TC_FMT_CUE);
    CHECK_EQ_INT(tc_detect_from_file("/definitely/not/here.unknown"), TC_FMT_AUTO);
    CHECK_EQ_INT(tc_detect_from_file(NULL), TC_FMT_AUTO);
}

/* ------------------------------------------------------------------ */
/* API error handling                                                 */
/* ------------------------------------------------------------------ */

static void test_api_rejects_bad_arguments(void) {
    g_case = "api_arguments";
    tc_data *d = NULL;

    CHECK_EQ_INT(tc_parse_file(NULL, TC_FMT_AUTO, &d), TC_E_INVALID);
    CHECK_EQ_INT(tc_parse_file("x", TC_FMT_AUTO, NULL), TC_E_INVALID);

    /* A format with no parser yet must say so clearly rather than silently
     * producing an empty result. */
    CHECK_EQ_INT(tc_parse_file("/definitely/not/here.mpls", TC_FMT_MPLS, &d), TC_E_UNSUPPORTED);

    CHECK_EQ_INT(tc_entry_count(NULL), 0);
    CHECK_EQ_INT(tc_chapter_count(NULL, 0), 0);

    tc_entry_info_t ei;
    CHECK_EQ_INT(tc_entry_info(NULL, 0, &ei), TC_E_INVALID);

    tc_chapter_t c;
    CHECK_EQ_INT(tc_chapter_at(NULL, 0, 0, &c), TC_E_INVALID);
}

static void test_last_error_is_set(void) {
    g_case = "last_error";
    tc_data *d = NULL;
    (void)tc_parse_file(NULL, TC_FMT_AUTO, &d);
    CHECK(tc_last_error() != NULL);
    CHECK(strlen(tc_last_error()) > 0);
}

/* ------------------------------------------------------------------ */
/* Writers                                                            */
/* ------------------------------------------------------------------ */

/* Builds a small entry by hand so the writers can be tested before the parsers
 * exist. */
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
    g_case = "writer_ogm";
    tc_data *d = make_sample();
    CHECK(d != NULL);
    if (!d) {
        return;
    }

    size_t need = 0;
    CHECK_EQ_INT(tc_render(d, 0, TC_FMT_OGM, NULL, NULL, NULL, &need), TC_E_RANGE);
    CHECK(need > 1);

    char *buf = malloc(need);
    CHECK(buf != NULL);
    if (buf) {
        size_t len = need;
        CHECK_EQ_INT(tc_render(d, 0, TC_FMT_OGM, NULL, NULL, buf, &len), TC_OK);
        CHECK(strstr(buf, "CHAPTER01=00:00:00.000") != NULL);
        CHECK(strstr(buf, "CHAPTER01NAME=Opening") != NULL);
        CHECK(strstr(buf, "CHAPTER02=00:01:30.000") != NULL);
        CHECK(strstr(buf, "CHAPTER03=00:23:00.000") != NULL);
        free(buf);
    }
    tc_free(d);
}

static void test_writer_ogm_names_default(void) {
    g_case = "writer_ogm_default_name";
    tc_data *d = tc_calloc(1, sizeof(*d));
    tc_entry *e = tc_data_add_entry(d);
    e->fps_den = 1;
    tc_entry_add_chapter(e, NULL, 0, 0);

    size_t need = 0;
    (void)tc_render(d, 0, TC_FMT_OGM, NULL, NULL, NULL, &need);
    char *buf = malloc(need);
    if (buf) {
        size_t len = need;
        CHECK_EQ_INT(tc_render(d, 0, TC_FMT_OGM, NULL, NULL, buf, &len), TC_OK);
        /* A nameless chapter must still produce a usable name. */
        CHECK(strstr(buf, "NAME=Chapter 01") != NULL);
        free(buf);
    }
    tc_free(d);
}

static void test_writer_xml_escapes(void) {
    g_case = "writer_xml";
    tc_data *d = tc_calloc(1, sizeof(*d));
    tc_entry *e = tc_data_add_entry(d);
    e->fps_den = 1;
    tc_entry_add_chapter(e, tc_strdup("A & B < C"), 0, 0);

    size_t need = 0;
    (void)tc_render(d, 0, TC_FMT_XML, "jpn", NULL, NULL, &need);
    char *buf = malloc(need);
    if (buf) {
        size_t len = need;
        CHECK_EQ_INT(tc_render(d, 0, TC_FMT_XML, "jpn", NULL, buf, &len), TC_OK);
        CHECK(strstr(buf, "<ChapterString>A &amp; B &lt; C</ChapterString>") != NULL);
        CHECK(strstr(buf, "<ChapterLanguage>jpn</ChapterLanguage>") != NULL);
        CHECK(strstr(buf, "<Chapters>") != NULL);
        free(buf);
    }
    tc_free(d);
}

static void test_writer_vtt_cue_durations(void) {
    g_case = "writer_vtt";
    tc_data *d = make_sample();
    if (!d) {
        CHECK(0);
        return;
    }
    size_t need = 0;
    (void)tc_render(d, 0, TC_FMT_VTT, NULL, NULL, NULL, &need);
    char *buf = malloc(need);
    if (buf) {
        size_t len = need;
        CHECK_EQ_INT(tc_render(d, 0, TC_FMT_VTT, NULL, NULL, buf, &len), TC_OK);
        CHECK(strncmp(buf, "WEBVTT", 6) == 0);
        /* Cue 1 runs from chapter 1's start to chapter 2's start. */
        CHECK(strstr(buf, "00:00:00.000 --> 00:01:30.000") != NULL);
        /* The last cue ends at the entry duration, not at the next chapter. */
        CHECK(strstr(buf, "00:23:00.000 --> 00:24:00.000") != NULL);
        free(buf);
    }
    tc_free(d);
}

static void test_writer_xpl(void) {
    g_case = "writer_xpl";
    tc_data *d = make_sample();
    if (!d) {
        CHECK(0);
        return;
    }
    size_t need = 0;
    (void)tc_render(d, 0, TC_FMT_XPL, NULL, "00000.m2ts", NULL, &need);
    char *buf = malloc(need);
    if (buf) {
        size_t len = need;
        CHECK_EQ_INT(tc_render(d, 0, TC_FMT_XPL, NULL, "00000.m2ts", buf, &len), TC_OK);
        CHECK(strstr(buf, "<XPL>") != NULL);
        CHECK(strstr(buf, "name=\"00000.m2ts\"") != NULL);
        CHECK(strstr(buf, "<chapter name=\"Opening\" time=\"00:00:00.000\" />") != NULL);
        free(buf);
    }
    tc_free(d);
}

static void test_writer_cue(void) {
    g_case = "writer_cue";
    tc_data *d = make_sample();
    if (!d) {
        CHECK(0);
        return;
    }
    size_t need = 0;
    (void)tc_render(d, 0, TC_FMT_CUE, "jpn", "track.wav", NULL, &need);
    char *buf = malloc(need);
    if (buf) {
        size_t len = need;
        CHECK_EQ_INT(tc_render(d, 0, TC_FMT_CUE, "jpn", "track.wav", buf, &len), TC_OK);
        CHECK(strstr(buf, "FILE \"track.wav\" WAVE") != NULL);
        CHECK(strstr(buf, "TRACK 01 AUDIO") != NULL);
        CHECK(strstr(buf, "INDEX 01 00:00:00.000") != NULL);
        free(buf);
    }
    tc_free(d);
}

static void test_writer_rejects_binary_targets(void) {
    g_case = "writer_unsupported";
    tc_data *d = make_sample();
    if (!d) {
        CHECK(0);
        return;
    }
    size_t need = 0;
    CHECK_EQ_INT(tc_render(d, 0, TC_FMT_MPLS, NULL, NULL, NULL, &need), TC_E_UNSUPPORTED);
    CHECK_EQ_INT(tc_render(d, 0, TC_FMT_MP4, NULL, NULL, NULL, &need), TC_E_UNSUPPORTED);
    CHECK_EQ_INT(tc_render(d, 0, TC_FMT_TAK, NULL, NULL, NULL, &need), TC_E_UNSUPPORTED);
    tc_free(d);
}

static void test_render_small_buffer_reports_size(void) {
    g_case = "render_small_buffer";
    tc_data *d = make_sample();
    if (!d) {
        CHECK(0);
        return;
    }
    char small[4];
    size_t len = sizeof(small);
    CHECK_EQ_INT(tc_render(d, 0, TC_FMT_OGM, NULL, NULL, small, &len), TC_E_RANGE);
    CHECK(len > sizeof(small)); /* the required size is reported back */
    tc_free(d);
}

static void test_render_rejects_bad_entry(void) {
    g_case = "render_bad_entry";
    tc_data *d = make_sample();
    if (!d) {
        CHECK(0);
        return;
    }
    size_t need = 0;
    CHECK_EQ_INT(tc_render(d, 99, TC_FMT_OGM, NULL, NULL, NULL, &need), TC_E_RANGE);
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Save to disk                                                       */
/* ------------------------------------------------------------------ */

static void test_save_writes_a_file(void) {
    g_case = "save_file";
    tc_data *d = make_sample();
    if (!d) {
        CHECK(0);
        return;
    }
    const char *path = "build/test_output.txt";
    CHECK_EQ_INT(tc_save(d, 0, TC_FMT_OGM, path, NULL, NULL), TC_OK);

    FILE *f = fopen(path, "rb");
    CHECK(f != NULL);
    if (f) {
        char head[8] = {0};
        size_t n = fread(head, 1, 3, f);
        /* Chapter files are written with a UTF-8 BOM for Windows consumers. */
        CHECK_EQ_INT(n, 3);
        CHECK((unsigned char)head[0] == 0xEF);
        CHECK((unsigned char)head[1] == 0xBB);
        CHECK((unsigned char)head[2] == 0xBF);
        fclose(f);
    }
    remove(path);
    tc_free(d);
}

static void test_save_rejects_unwritable_path(void) {
    g_case = "save_bad_path";
    tc_data *d = make_sample();
    if (!d) {
        CHECK(0);
        return;
    }
    CHECK_EQ_INT(tc_save(d, 0, TC_FMT_OGM, "/definitely/not/a/dir/out.txt", NULL, NULL), TC_E_IO);
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Entry / chapter accessors                                          */
/* ------------------------------------------------------------------ */

static void test_accessors(void) {
    g_case = "accessors";
    tc_data *d = make_sample();
    if (!d) {
        CHECK(0);
        return;
    }

    CHECK_EQ_INT(tc_entry_count(d), 1);
    CHECK_EQ_INT(tc_chapter_count(d, 0), 3);

    tc_entry_info_t ei;
    CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    CHECK_EQ_STR(ei.title, "Episode 01");
    CHECK_EQ_STR(ei.source, "00000.m2ts");
    CHECK_EQ_INT(ei.fps_num, 24000);
    CHECK_EQ_INT(ei.fps_den, 1001);
    CHECK_EQ_INT(ei.duration_ns, 1440000000000LL);
    CHECK_EQ_INT(ei.chapter_count, 3);

    tc_chapter_t c;
    CHECK_EQ_INT(tc_chapter_at(d, 0, 0, &c), TC_OK);
    CHECK_EQ_STR(c.name, "Opening");
    CHECK_EQ_INT(c.time_ns, 0);

    CHECK_EQ_INT(tc_chapter_at(d, 0, 1, &c), TC_OK);
    CHECK_EQ_STR(c.name, "Part A");
    CHECK_EQ_INT(c.time_ns, 90000000000LL);

    CHECK_EQ_INT(tc_chapter_at(d, 0, 3, &c), TC_E_RANGE);
    CHECK_EQ_INT(tc_entry_info(d, 1, &ei), TC_E_RANGE);

    tc_free(d);
}

static void test_free_accepts_null(void) {
    g_case = "free_null";
    tc_free(NULL); /* must not crash */
    CHECK(1);
}

/* ------------------------------------------------------------------ */
/* main                                                               */
/* ------------------------------------------------------------------ */

int main(void) {
    test_version();
    test_byte_readers();
    test_text_helpers();
    test_timestamp_parsing();
    test_timestamp_formatting();
    test_timestamp_round_trip();
    test_buffer_growth();
    test_xml_basic();
    test_xml_attributes_and_entities();
    test_xml_comments_cdata_and_doctype();
    test_xml_utf8_bom();
    test_xml_rejects_malformed();
    test_xml_empty_is_rejected();
    test_format_detection();
    test_api_rejects_bad_arguments();
    test_last_error_is_set();
    test_writer_ogm();
    test_writer_ogm_names_default();
    test_writer_xml_escapes();
    test_writer_vtt_cue_durations();
    test_writer_xpl();
    test_writer_cue();
    test_writer_rejects_binary_targets();
    test_render_small_buffer_reports_size();
    test_render_rejects_bad_entry();
    test_save_writes_a_file();
    test_save_rejects_unwritable_path();
    test_accessors();
    test_free_accepts_null();

    printf("\n%d checks, %d failures\n", g_checks, g_failures);
    return g_failures == 0 ? 0 : 1;
}
