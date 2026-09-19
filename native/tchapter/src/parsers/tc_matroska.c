/*
 * Matroska chapter XML parser (B9).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * The reference MATROSKAParser does two things: it runs `mkvextract chapters`
 * and hands the process output to XMLParser. The process boundary belongs to
 * the Go engine (internal/chapter), so this file implements only the parsing
 * half: the input is the XML that mkvextract writes to stdout.
 *
 * Semantics taken from TChapter.Parsing.XMLParser.ToChapterInfo / ToChapter:
 *   - <Chapters> is the root; every <EditionEntry> becomes one entry, even
 *     when it has no <ChapterAtom>.
 *   - A <ChapterAtom> emits a chapter for <ChapterTimeStart>, then recurses
 *     into its <ChapterAtom> children, then emits a second chapter for
 *     <ChapterTimeEnd>. mkvextract output normally carries both ends, so one
 *     atom produces two chapters with the same name.
 *   - The name is the first <ChapterDisplay>'s first <ChapterString>. An atom
 *     without a display has an empty name; the reference dereferences the
 *     display and throws there, but mkvextract emits such atoms for chapters
 *     written without a name, so an empty name is the only usable behaviour.
 *   - <ChapterFlagHidden>, <EditionFlagHidden> and <ChapterFlagEnabled> are
 *     deserialised but never consulted: hidden chapters are still returned.
 *   - An absent <ChapterTimeStart> / <ChapterTimeEnd> means "no chapter",
 *     while a present but empty one means zero.
 */
#include <ctype.h>
#include <stddef.h>
#include <stdint.h>
#include <string.h>

#include "tc_internal.h"

/* ------------------------------------------------------------------ */
/* Chapter names                                                      */
/* ------------------------------------------------------------------ */

/* The first <ChapterDisplay>'s first <ChapterString>, matching how the XML
 * serialiser binds a non-repeated property. Never returns NULL. */
static char *matroska_chapter_name(const tc_xml_node *atom) {
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

static int matroska_is_space(char c) {
    return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f';
}

static const char *matroska_skip_space(const char *p, const char *end) {
    while (p < end && matroska_is_space(*p)) {
        p++;
    }
    return p;
}

/* The components of a matched time code, before normalisation. */
typedef struct matroska_time {
    int64_t hours;
    int64_t minutes;
    int64_t seconds;
    /* Integer value of the first up-to-nine fraction digits, and how many
     * digits there were (3..9). */
    int64_t fraction;
    int fraction_digits;
} matroska_time;

/* Accumulates decimal digits, clamping once the value passes `cap`. The
 * reference parses each component with int.Parse, which throws for anything
 * above 2147483647; clamping to a smaller bound turns those values into a
 * range error later instead, without risking int64 overflow. */
static int64_t matroska_digits(const char **cursor, const char *end, int64_t cap) {
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

/* Finds the leftmost match of the reference's pattern,
 * (\d+)\s*:\s*(\d+)\s*:\s*(\d+)\s*[.,]\s*(\d{3,9}), exactly as
 * Config.TIME_FORMAT does. Returns 1 on a match and 0 otherwise; the reference
 * then hands Match(...).Value to ToTimeSpan, which yields zero for no match.
 *
 * The digit runs are greedy and a shorter run can never be followed by the
 * separator the pattern requires, so only the start of a run can match. */
static int matroska_find_time(const char *s, const char *end, matroska_time *out) {
    const int64_t cap = 999999999;

    for (const char *p = s; p < end; p++) {
        if (!isdigit((unsigned char)*p)) {
            continue;
        }
        if (p > s && isdigit((unsigned char)p[-1])) {
            continue;
        }

        /* hour */
        const char *q = p;
        int64_t hours = matroska_digits(&q, end, cap);
        q = matroska_skip_space(q, end);
        if (q >= end || *q != ':') {
            continue;
        }

        /* minute */
        q = matroska_skip_space(q + 1, end);
        if (q >= end || !isdigit((unsigned char)*q)) {
            continue;
        }
        int64_t minutes = matroska_digits(&q, end, cap);
        q = matroska_skip_space(q, end);
        if (q >= end || *q != ':') {
            continue;
        }

        /* second */
        q = matroska_skip_space(q + 1, end);
        if (q >= end || !isdigit((unsigned char)*q)) {
            continue;
        }
        int64_t seconds = matroska_digits(&q, end, cap);
        q = matroska_skip_space(q, end);
        if (q >= end || (*q != '.' && *q != ',')) {
            continue;
        }

        /* Fraction: the pattern takes 3 to 9 digits. */
        q = matroska_skip_space(q + 1, end);
        int64_t fraction = 0;
        int digits = 0;
        while (q < end && isdigit((unsigned char)*q) && digits < 9) {
            fraction = fraction * 10 + (*q - '0');
            q++;
            digits++;
        }
        if (digits < 3) {
            continue;
        }

        out->hours = hours;
        out->minutes = minutes;
        out->seconds = seconds;
        out->fraction = fraction;
        out->fraction_digits = digits;
        return 1;
    }
    return 0;
}

/* Converts the text of a time element into nanoseconds. */
static tc_status matroska_time_value(const tc_xml_node *node, int64_t *out_ns) {
    tc_buf text;
    tc_buf_init(&text);
    tc_xml_text(node, &text);
    if (text.oom) {
        tc_buf_free(&text);
        return TC_E_NOMEM;
    }

    const char *start = text.data ? text.data : "";
    matroska_time t;
    int found = matroska_find_time(start, start + text.len, &t);
    tc_buf_free(&text);

    if (!found) {
        /* No match: the reference falls back to TimeSpan.Zero. */
        *out_ns = 0;
        return TC_OK;
    }

    /* TimeSpan normalises oversized minute and second fields, so "00:99:00"
     * is 99 minutes. The components were clamped, so this cannot overflow. */
    int64_t total_seconds = t.hours * 3600 + t.minutes * 60 + t.seconds;
    /* The library stores nanoseconds in an int64, which caps a timestamp at
     * ~2.56 million hours; the reference's TimeSpan holds 100 times more, but
     * no chapter list approaches either bound. Beyond it the reference throws
     * a TimeSpan overflow and this returns a format error. */
    if (total_seconds > 9223372035LL) {
        return tc_fail(TC_E_FORMAT, "matroska: timestamp is out of range");
    }

    /* ToTimeSpan divides the fraction by 10^(digits-3) and scales it to 100 ns
     * ticks. Both steps are IEEE-754 doubles in the reference, and that is not
     * equivalent to integer scaling: ".75981" is 7598099 ticks, not 7598100.
     * The operation order is reproduced here exactly. */
    static const double pow10[] = {1.0, 10.0, 100.0, 1000.0, 10000.0, 100000.0, 1000000.0};
    double millis = (double)t.fraction / pow10[t.fraction_digits - 3];
    int64_t ticks = (int64_t)(millis * 10000.0);

    /* tc_parse_timestamp converts the whole seconds; the fraction is added as
     * ticks because its precision is not a whole number of nanoseconds. */
    int64_t hours = total_seconds / 3600;
    int64_t rest = total_seconds % 3600;
    tc_buf canon;
    tc_buf_init(&canon);
    tc_buf_put_i64(&canon, hours, 2);
    tc_buf_putc(&canon, ':');
    tc_buf_put_i64(&canon, rest / 60, 2);
    tc_buf_putc(&canon, ':');
    tc_buf_put_i64(&canon, rest % 60, 2);
    if (canon.oom) {
        tc_buf_free(&canon);
        return TC_E_NOMEM;
    }

    int64_t ns = 0;
    tc_status st = tc_parse_timestamp(canon.data, &ns);
    tc_buf_free(&canon);
    if (st != TC_OK) {
        return tc_fail(TC_E_FORMAT, "matroska: invalid timestamp");
    }

    /* total_seconds was bounded so that this cannot overflow. */
    *out_ns = ns + ticks * 100;
    return TC_OK;
}

/* ------------------------------------------------------------------ */
/* Chapter atoms                                                      */
/* ------------------------------------------------------------------ */

/* Emits the chapters of one atom, in the reference's order: the start time,
 * then the sub-atoms, then the end time. */
static tc_status matroska_add_atom(const tc_xml_node *atom, tc_entry *e) {
    const tc_xml_node *start = tc_xml_child(atom, "ChapterTimeStart");
    if (start) {
        int64_t ns = 0;
        tc_status st = matroska_time_value(start, &ns);
        if (st != TC_OK) {
            return st;
        }
        char *name = matroska_chapter_name(atom);
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
        tc_status st = matroska_add_atom(c, e);
        if (st != TC_OK) {
            return st;
        }
    }

    const tc_xml_node *end = tc_xml_child(atom, "ChapterTimeEnd");
    if (end) {
        int64_t ns = 0;
        tc_status st = matroska_time_value(end, &ns);
        if (st != TC_OK) {
            return st;
        }
        char *name = matroska_chapter_name(atom);
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

tc_status tc_matroska_parse_mem(const void *buf, size_t len, const char *hint, tc_data *d) {
    (void)hint;
    if (!buf || !d) {
        return tc_fail(TC_E_INVALID, "matroska: null argument");
    }

    tc_xml_node *root = NULL;
    tc_status st = tc_xml_parse((const char *)buf, len, &root);
    if (st != TC_OK) {
        return st;
    }

    /* The serialiser requires this exact root element in no namespace;
     * anything else is a different document that happens to be XML. A prefixed
     * root is already rejected above because its name carries the prefix, but a
     * default namespace has to be checked explicitly: the element name is still
     * "Chapters" while XmlSerializer would refuse to bind it. */
    const tc_xml_node *chapters = tc_xml_child(root, "Chapters");
    if (!chapters) {
        tc_xml_free(root);
        return tc_fail(TC_E_FORMAT, "matroska: no <Chapters> root element");
    }
    const char *xmlns = tc_xml_attr(chapters, "xmlns");
    if (xmlns && xmlns[0] != '\0') {
        tc_xml_free(root);
        return tc_fail(TC_E_FORMAT,
                       "matroska: <Chapters> must not be in a namespace (found \"%s\")", xmlns);
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
            st = matroska_add_atom(atom, e);
            if (st != TC_OK) {
                tc_xml_free(root);
                return st;
            }
        }
    }

    tc_xml_free(root);
    if (d->entry_count == 0) {
        /* The reference walks a null array here and throws. */
        return tc_fail(TC_E_FORMAT, "matroska: no <EditionEntry> element");
    }
    return TC_OK;
}
