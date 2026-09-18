/*
 * XPL (HD DVD playlist) chapter parser.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Ported from TChapter/Parsing/XPLParser.cs. The format is XML in the HD DVD
 * playlist namespace, and its time values are unusual: "HH:MM:SS:TT" where the
 * HH:MM:SS part is scaled by the TitleSet's timeBase and TT is a tick count
 * whose length depends on tickBase / tickBaseDivisor. Both conversions are
 * reproduced exactly, including the truncation behaviour of the original.
 *
 * One entry is produced per <Title> that has a <ChapterList>; titles without
 * chapters (such as <FirstPlayTitle>) are skipped, matching the LINQ filter in
 * the original.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_internal.h"

/* The HD DVD playlist namespace. Elements are matched by local name: the mini
 * XML reader does not resolve namespaces, and no chapter format in scope uses a
 * prefixed playlist element, so matching the literal name is sufficient. */
static const char *const kNamespace = "http://www.dvdforum.org/2005/HDDVDVideo/Playlist";

/* ------------------------------------------------------------------ */
/* Time conversion                                                    */
/* ------------------------------------------------------------------ */

/* Parses a "timeBase"-style attribute such as "60fps" into a number. Returns
 * false when the attribute is absent or unparseable. */
static int parse_base(const char *value, double *out) {
    if (!value || !*value) {
        return 0;
    }
    /* Strip a trailing "fps" the way the original does. */
    char buf[64];
    size_t n = strlen(value);
    if (n >= sizeof(buf)) {
        return 0;
    }
    memcpy(buf, value, n + 1);
    char *suffix = strstr(buf, "fps");
    if (suffix) {
        *suffix = '\0';
    }
    char *end = NULL;
    double v = strtod(buf, &end);
    if (end == buf || v <= 0) {
        return 0;
    }
    *out = v;
    return 1;
}

/* Converts "HH:MM:SS:TT" into nanoseconds.
 *
 * The original builds a TimeSpan from the "HH:MM:SS" prefix, rescales it by
 * timeBase/60, then adds the tick field scaled by 1/(tickBase/divisor) seconds.
 * Truncation happens at the same two points as in the original, so the results
 * are identical rather than merely close. */
static tc_status convert_time(const char *value, double time_base, double tick_base,
                             int tick_base_divisor, int64_t *out_ns) {
    if (!value || !*value) {
        return tc_fail(TC_E_FORMAT, "xpl: missing time value");
    }
    if (tick_base <= 0 || tick_base_divisor <= 0) {
        return tc_fail(TC_E_FORMAT, "xpl: invalid tick base");
    }

    const char *last_colon = strrchr(value, ':');
    if (!last_colon) {
        return tc_fail(TC_E_FORMAT, "xpl: time \"%s\" has no tick field", value);
    }

    /* --- the HH:MM:SS prefix --- */
    int hours = 0, minutes = 0, seconds = 0;
    if (sscanf(value, "%d:%d:%d", &hours, &minutes, &seconds) != 3) {
        return tc_fail(TC_E_FORMAT, "xpl: time \"%s\" is malformed", value);
    }
    if (minutes < 0 || minutes >= 60 || seconds < 0 || seconds >= 60) {
        return tc_fail(TC_E_FORMAT, "xpl: time \"%s\" is out of range", value);
    }
    double total_seconds = (double)(hours * 3600 + minutes * 60 + seconds);
    /* (long) in C# truncates toward zero; a non-negative value makes that a
     * plain floor. */
    int64_t scaled = (int64_t)(total_seconds / 60.0 * time_base);
    int64_t base_ns = scaled * 1000000000LL;

    /* --- the tick field --- */
    char *end = NULL;
    double ticks = strtod(last_colon + 1, &end);
    if (end == last_colon + 1) {
        return tc_fail(TC_E_FORMAT, "xpl: time \"%s\" has a non-numeric tick field", value);
    }
    double ns_per_tick = 1000000000.0 / (tick_base / (double)tick_base_divisor);
    int64_t tick_ns = (int64_t)(ticks * ns_per_tick);

    *out_ns = base_ns + tick_ns;
    return TC_OK;
}

/* ------------------------------------------------------------------ */
/* Parsing                                                            */
/* ------------------------------------------------------------------ */

/* Returns the nth child element with the given name, or NULL. */
static const tc_xml_node *nth_child(const tc_xml_node *parent, const char *name, size_t n) {
    size_t seen = 0;
    for (size_t i = 0; i < parent->child_count; i++) {
        const tc_xml_node *c = &parent->children[i];
        if (c->kind != TC_XML_ELEMENT || !c->name || strcmp(c->name, name) != 0) {
            continue;
        }
        if (seen == n) {
            return c;
        }
        seen++;
    }
    return NULL;
}

/* Counts child elements with the given name. */
static size_t count_children(const tc_xml_node *parent, const char *name) {
    size_t n = 0;
    for (size_t i = 0; i < parent->child_count; i++) {
        const tc_xml_node *c = &parent->children[i];
        if (c->kind == TC_XML_ELEMENT && c->name && strcmp(c->name, name) == 0) {
            n++;
        }
    }
    return n;
}

/* Derives the title name: the file's base name, overridden by the title's "id"
 * and then by "displayName", exactly as the original does. */
static char *title_name(const tc_xml_node *title, const char *location) {
    char *name = NULL;

    if (location) {
        const char *base = location;
        for (const char *p = location; *p; p++) {
            if (*p == '/' || *p == '\\') {
                base = p + 1;
            }
        }
        char *copy = tc_strdup(base);
        if (!copy) {
            return NULL;
        }
        /* Strip the extension. */
        char *dot = strrchr(copy, '.');
        if (dot && dot != copy) {
            *dot = '\0';
        }
        name = copy;
    }
    if (!name) {
        name = tc_strdup("");
        if (!name) {
            return NULL;
        }
    }

    const char *id = tc_xml_attr(title, "id");
    if (id) {
        char *replacement = tc_strdup(id);
        if (!replacement) {
            free(name);
            return NULL;
        }
        free(name);
        name = replacement;
    }
    const char *display = tc_xml_attr(title, "displayName");
    if (display) {
        char *replacement = tc_strdup(display);
        if (!replacement) {
            free(name);
            return NULL;
        }
        free(name);
        name = replacement;
    }
    return name;
}

/* Chapter name: "id", overridden by "displayName"; empty when neither is
 * present. */
static char *chapter_name(const tc_xml_node *chapter) {
    const char *display = tc_xml_attr(chapter, "displayName");
    if (display) {
        return tc_strdup(display);
    }
    const char *id = tc_xml_attr(chapter, "id");
    if (id) {
        return tc_strdup(id);
    }
    return tc_strdup("");
}

static tc_status parse_document(const char *text, size_t len, const char *location, tc_data *d) {
    tc_xml_node *root = NULL;
    tc_status st = tc_xml_parse(text, len, &root);
    if (st != TC_OK) {
        return st;
    }

    /* The root is a synthetic #document node; the playlist is its only
     * element child. The name must match exactly: accepting a lookalike root
     * would let a different XML dialect through and produce empty chapters. */
    const tc_xml_node *playlist = NULL;
    for (size_t i = 0; i < root->child_count && !playlist; i++) {
        const tc_xml_node *c = &root->children[i];
        if (c->kind == TC_XML_ELEMENT && c->name && strcmp(c->name, "Playlist") == 0) {
            playlist = c;
        }
    }
    if (!playlist) {
        /* Report the namespace in the message: a wrong document is by far the
         * most likely cause of this failure. */
        tc_xml_free(root);
        return tc_fail(TC_E_FORMAT, "xpl: no <Playlist> element (expected the HD DVD namespace %s)",
                       kNamespace);
    }

    size_t title_sets = count_children(playlist, "TitleSet");
    for (size_t ts_index = 0; ts_index < title_sets; ts_index++) {
        const tc_xml_node *ts = nth_child(playlist, "TitleSet", ts_index);
        if (!ts) {
            continue;
        }

        /* timeBase is required by the format; 60 is the original's fallback.
         * tickBase is optional and defaults to 24. */
        double time_base = 60.0;
        double tick_base = 24.0;
        parse_base(tc_xml_attr(ts, "timeBase"), &time_base);
        parse_base(tc_xml_attr(ts, "tickBase"), &tick_base);

        size_t titles = count_children(ts, "Title");
        for (size_t t_index = 0; t_index < titles; t_index++) {
            const tc_xml_node *title = nth_child(ts, "Title", t_index);
            if (!title) {
                continue;
            }
            /* Only titles that actually carry chapters are entries. */
            const tc_xml_node *chapter_list = tc_xml_child(title, "ChapterList");
            if (!chapter_list) {
                continue;
            }

            int tick_base_divisor = 1;
            const char *divisor = tc_xml_attr(title, "tickBaseDivisor");
            if (divisor) {
                int parsed = atoi(divisor);
                if (parsed > 0) {
                    tick_base_divisor = parsed;
                }
            }

            tc_entry *e = tc_data_add_entry(d);
            if (!e) {
                tc_xml_free(root);
                return TC_E_NOMEM;
            }
            e->fps_num = 24;
            e->fps_den = 1;

            e->title = title_name(title, location);
            if (!e->title) {
                tc_xml_free(root);
                return TC_E_NOMEM;
            }

            const tc_xml_node *clip = tc_xml_child(title, "PrimaryAudioVideoClip");
            const char *src = clip ? tc_xml_attr(clip, "src") : NULL;
            e->source = tc_strdup(src ? src : "");
            if (!e->source) {
                tc_xml_free(root);
                return TC_E_NOMEM;
            }

            const char *duration = tc_xml_attr(title, "titleDuration");
            if (duration) {
                int64_t ns = 0;
                if (convert_time(duration, time_base, tick_base, tick_base_divisor, &ns) == TC_OK) {
                    e->duration_ns = ns;
                }
                /* A malformed duration is not fatal: the chapters are still
                 * usable, and the original would have thrown here. Recorded as
                 * a deliberate leniency; see SPEC notes in the test file. */
            }

            size_t chapters = count_children(chapter_list, "Chapter");
            for (size_t c_index = 0; c_index < chapters; c_index++) {
                const tc_xml_node *chapter = nth_child(chapter_list, "Chapter", c_index);
                if (!chapter) {
                    continue;
                }
                const char *begin = tc_xml_attr(chapter, "titleTimeBegin");
                if (!begin) {
                    /* titleTimeBegin is required; without it the chapter has no
                     * position and cannot be represented. */
                    tc_xml_free(root);
                    return tc_fail(TC_E_FORMAT,
                                   "xpl: <Chapter> without a titleTimeBegin attribute");
                }
                int64_t ns = 0;
                st = convert_time(begin, time_base, tick_base, tick_base_divisor, &ns);
                if (st != TC_OK) {
                    tc_xml_free(root);
                    return st;
                }
                char *name = chapter_name(chapter);
                if (!name) {
                    tc_xml_free(root);
                    return TC_E_NOMEM;
                }
                if (tc_entry_add_chapter(e, name, ns, -1) != TC_OK) {
                    tc_xml_free(root);
                    return TC_E_NOMEM;
                }
            }
        }
    }

    tc_xml_free(root);
    if (d->entry_count == 0) {
        return tc_fail(TC_E_FORMAT, "xpl: no title with a <ChapterList> was found");
    }
    return TC_OK;
}

tc_status tc_xpl_parse_mem(const void *buf, size_t len, const char *hint, tc_data *d) {
    if (!buf) {
        return tc_fail(TC_E_INVALID, "xpl: null buffer");
    }
    return parse_document((const char *)buf, len, hint, d);
}

tc_status tc_xpl_parse_file(const char *path, tc_data *d) {
    char *text = NULL;
    size_t len = 0;
    tc_status st = tc_read_file(path, &text, &len);
    if (st != TC_OK) {
        return st;
    }
    /* The file name supplies the default title name, so the path is passed
     * through as the location hint. */
    st = parse_document(text, len, path, d);
    free(text);
    return st;
}
