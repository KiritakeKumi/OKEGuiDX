/*
 * Chapter XML parser (B11).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Ported from TChapter.Parsing.XMLParser. The document is Matroska chapter XML
 * and is deserialised with the same shape B9 uses
 * (TChapter.Chapters.Serializable.Chapters), so the traversal matches
 * parsers/tc_matroska.c:
 *
 *   - <Chapters> is the root and every <EditionEntry> becomes one entry, even
 *     when it carries no <ChapterAtom>.
 *   - A <ChapterAtom> emits a chapter for <ChapterTimeStart>, then recurses into
 *     its child <ChapterAtom>s, then emits a second chapter for
 *     <ChapterTimeEnd>. An atom with both ends produces two chapters sharing one
 *     name.
 *   - The name is the first <ChapterDisplay>'s first <ChapterString>, matching
 *     how XmlSerializer binds a non-repeated property: later displays and later
 *     strings inside one display are ignored. An absent <ChapterString> means an
 *     empty name.
 *   - Flags (<ChapterFlagHidden>, <EditionFlagHidden>, <ChapterFlagEnabled>) are
 *     deserialised by the reference but never consulted.
 *
 * XMLParser is also the back end of MATROSKAParser, so this parser and B9 accept
 * the same documents; the two differ only in how strict they are about the
 * pieces the reference happens to dereference without a null check.
 */
#include <ctype.h>
#include <stddef.h>
#include <stdint.h>
#include <string.h>

#include "tc_internal.h"

/* ------------------------------------------------------------------ */
/* Chapter names                                                      */
/* ------------------------------------------------------------------ */

/* The first <ChapterDisplay>'s first <ChapterString>, or "" when either is
 * absent. Never returns NULL unless allocation fails. */
static char *xmlchapters_chapter_name(const tc_xml_node *atom) {
    const tc_xml_node *display = tc_xml_child(atom, "ChapterDisplay");
    const tc_xml_node *string = display ? tc_xml_child(display, "ChapterString") : NULL;
    if (!string) {
        return tc_strdup("");
    }

    tc_buf text;
    tc_buf_init(&text);
    tc_xml_text(string, &text);
    if (text.oom) {
        tc_buf_free(&text);
        return NULL;
    }
    char *name = tc_strdup(text.data ? text.data : "");
    tc_buf_free(&text);
    return name;
}

/* ------------------------------------------------------------------ */
/* Time codes                                                         */
/* ------------------------------------------------------------------ */

static int xmlchapters_is_space(char c) {
    return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f';
}

static const char *xmlchapters_skip_space(const char *p, const char *end) {
    while (p < end && xmlchapters_is_space(*p)) {
        p++;
    }
    return p;
}

typedef struct xmlchapters_time {
    int64_t hours;
    int64_t minutes;
    int64_t seconds;
    int millis;
} xmlchapters_time;

/* Accumulates decimal digits, stopping the scan at `cap`. */
static int64_t xmlchapters_digits(const char **cursor, const char *end, int64_t cap) {
    const char *p = *cursor;
    int64_t v = 0;
    while (p < end && isdigit((unsigned char)*p)) {
        if (v <= cap) {
            v = v * 10 + (*p - '0');
        }
        p++;
    }
    *cursor = p;
    return v;
}

/* Finds the leftmost match of Config.TIME_FORMAT,
 * (\d+)\s*:\s*(\d+)\s*:\s*(\d+)\s*[.,]\s*(\d{3,9}), exactly as the reference
 * does. The digit runs are greedy and a shorter run can never be followed by the
 * separator the pattern requires, so only the start of a run can match. */
static int xmlchapters_find_time(const char *s, const char *end, xmlchapters_time *out) {
    /* int.Parse rejects anything beyond Int32, and TimeSpan cannot hold values
     * near this cap anyway; larger components are rejected below. */
    const int64_t cap = 1000000000000000LL;

    for (const char *p = s; p < end; p++) {
        if (!isdigit((unsigned char)*p)) {
            continue;
        }
        if (p > s && isdigit((unsigned char)p[-1])) {
            continue;
        }

        const char *q = p;
        int64_t hours = xmlchapters_digits(&q, end, cap);
        q = xmlchapters_skip_space(q, end);
        if (q >= end || *q != ':') {
            continue;
        }

        q = xmlchapters_skip_space(q + 1, end);
        if (q >= end || !isdigit((unsigned char)*q)) {
            continue;
        }
        int64_t minutes = xmlchapters_digits(&q, end, cap);
        q = xmlchapters_skip_space(q, end);
        if (q >= end || *q != ':') {
            continue;
        }

        q = xmlchapters_skip_space(q + 1, end);
        if (q >= end || !isdigit((unsigned char)*q)) {
            continue;
        }
        int64_t seconds = xmlchapters_digits(&q, end, cap);
        q = xmlchapters_skip_space(q, end);
        if (q >= end || (*q != '.' && *q != ',')) {
            continue;
        }

        /* The pattern takes 3 to 9 digits, and ToTimeSpan keeps the first
         * three, so ".123456789" is 123 ms. */
        q = xmlchapters_skip_space(q + 1, end);
        int millis = 0;
        int frac_digits = 0;
        while (q < end && isdigit((unsigned char)*q) && frac_digits < 9) {
            if (frac_digits < 3) {
                millis = millis * 10 + (*q - '0');
            }
            q++;
            frac_digits++;
        }
        if (frac_digits < 3) {
            continue;
        }

        out->hours = hours;
        out->minutes = minutes;
        out->seconds = seconds;
        out->millis = millis;
        return 1;
    }
    return 0;
}

/* Converts the text of a time element into nanoseconds. A value without a
 * matching time code yields zero, like ToTimeSpan. */
static tc_status xmlchapters_time_value(const tc_xml_node *node, int64_t *out_ns) {
    tc_buf text;
    tc_buf_init(&text);
    tc_xml_text(node, &text);
    if (text.oom) {
        tc_buf_free(&text);
        return TC_E_NOMEM;
    }

    const char *start = text.data ? text.data : "";
    xmlchapters_time t;
    int found = xmlchapters_find_time(start, start + text.len, &t);
    tc_buf_free(&text);

    if (!found) {
        *out_ns = 0;
        return TC_OK;
    }

    /* TimeSpan normalises oversized minute and second fields, so "00:99:00" is
     * 99 minutes. */
    int64_t total_seconds = t.hours * 3600 + t.minutes * 60 + t.seconds;
    /* Nanoseconds cap a timestamp at ~2.56 million hours; the reference's
     * TimeSpan reaches 100 times further but throws past that, so an absurd
     * value is reported as a format error rather than wrapped. */
    if (total_seconds > 9223372036LL) {
        return tc_fail(TC_E_FORMAT, "xml: timestamp is out of range");
    }

    int64_t hours = total_seconds / 3600;
    int64_t rest = total_seconds % 3600;

    /* tc_parse_timestamp does the actual conversion; the normalised form is
     * "H:MM:SS.mmm" with exactly three fraction digits. */
    tc_buf canon;
    tc_buf_init(&canon);
    tc_buf_put_i64(&canon, hours, 0);
    tc_buf_putc(&canon, ':');
    tc_buf_put_i64(&canon, rest / 60, 2);
    tc_buf_putc(&canon, ':');
    tc_buf_put_i64(&canon, rest % 60, 2);
    tc_buf_putc(&canon, '.');
    tc_buf_put_i64(&canon, t.millis, 3);
    if (canon.oom) {
        tc_buf_free(&canon);
        return TC_E_NOMEM;
    }

    tc_status st = tc_parse_timestamp(canon.data, out_ns);
    if (st != TC_OK) {
        st = tc_fail(TC_E_FORMAT, "xml: invalid timestamp \"%s\"", canon.data);
    }
    tc_buf_free(&canon);
    return st;
}

/* ------------------------------------------------------------------ */
/* Chapter atoms                                                      */
/* ------------------------------------------------------------------ */

/* Emits the chapters of one atom in the reference's order: the start time, then
 * the sub-atoms, then the end time. */
static tc_status xmlchapters_add_atom(const tc_xml_node *atom, tc_entry *e) {
    const tc_xml_node *start = tc_xml_child(atom, "ChapterTimeStart");
    if (start) {
        int64_t ns = 0;
        tc_status st = xmlchapters_time_value(start, &ns);
        if (st != TC_OK) {
            return st;
        }
        char *name = xmlchapters_chapter_name(atom);
        if (!name) {
            return TC_E_NOMEM;
        }
        st = tc_entry_add_chapter(e, name, ns, -1);
        if (st != TC_OK) {
            return st;
        }
    }

    for (size_t i = 0; i < atom->child_count; i++) {
        const tc_xml_node *c = &atom->children[i];
        if (c->kind != TC_XML_ELEMENT || !c->name || strcmp(c->name, "ChapterAtom") != 0) {
            continue;
        }
        tc_status st = xmlchapters_add_atom(c, e);
        if (st != TC_OK) {
            return st;
        }
    }

    const tc_xml_node *end = tc_xml_child(atom, "ChapterTimeEnd");
    if (end) {
        int64_t ns = 0;
        tc_status st = xmlchapters_time_value(end, &ns);
        if (st != TC_OK) {
            return st;
        }
        char *name = xmlchapters_chapter_name(atom);
        if (!name) {
            return TC_E_NOMEM;
        }
        st = tc_entry_add_chapter(e, name, ns, -1);
        if (st != TC_OK) {
            return st;
        }
    }
    return TC_OK;
}

/* ------------------------------------------------------------------ */
/* Parser                                                             */
/* ------------------------------------------------------------------ */

tc_status tc_xmlchapters_parse_mem(const void *buf, size_t len, const char *hint, tc_data *d) {
    (void)hint;
    if (!buf || !d) {
        return tc_fail(TC_E_INVALID, "xml: null argument");
    }

    tc_xml_node *root = NULL;
    tc_status st = tc_xml_parse((const char *)buf, len, &root);
    if (st != TC_OK) {
        return st;
    }

    /* The serialiser is case-sensitive and requires this exact root. */
    const tc_xml_node *chapters = tc_xml_child(root, "Chapters");
    if (!chapters) {
        tc_xml_free(root);
        return tc_fail(TC_E_FORMAT, "xml: no <Chapters> root element");
    }

    for (size_t i = 0; i < chapters->child_count; i++) {
        const tc_xml_node *edition = &chapters->children[i];
        if (edition->kind != TC_XML_ELEMENT || !edition->name ||
            strcmp(edition->name, "EditionEntry") != 0) {
            continue;
        }

        tc_entry *e = tc_data_add_entry(d);
        if (!e) {
            tc_xml_free(root);
            return TC_E_NOMEM;
        }
        for (size_t k = 0; k < edition->child_count; k++) {
            const tc_xml_node *atom = &edition->children[k];
            if (atom->kind != TC_XML_ELEMENT || !atom->name ||
                strcmp(atom->name, "ChapterAtom") != 0) {
                continue;
            }
            st = xmlchapters_add_atom(atom, e);
            if (st != TC_OK) {
                tc_xml_free(root);
                return st;
            }
        }
    }

    tc_xml_free(root);
    if (d->entry_count == 0) {
        /* The reference walks a null array here and throws. */
        return tc_fail(TC_E_FORMAT, "xml: no <EditionEntry> element");
    }
    return TC_OK;
}
