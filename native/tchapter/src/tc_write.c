/*
 * Text writers for the chapter formats OKEGui consumes.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#include <stdio.h>
#include <string.h>

#include "tc_internal.h"

/* ------------------------------------------------------------------ */
/* Helpers                                                            */
/* ------------------------------------------------------------------ */

static const char *name_or_default(const char *name, size_t index) {
    static char buf[32];
    if (name && name[0]) {
        return name;
    }
    /* Built by hand rather than with snprintf: "%zu" is not portable to the
     * MSVCRT that the Windows targets link against. */
    memcpy(buf, "Chapter ", 8);
    size_t pos = 8;
    if (index + 1 < 10) {
        buf[pos++] = '0';
    }
    char digits[24];
    int n = 0;
    size_t v = index + 1;
    do {
        digits[n++] = (char)('0' + (v % 10));
        v /= 10;
    } while (v > 0);
    while (n > 0) {
        buf[pos++] = digits[--n];
    }
    buf[pos] = '\0';
    return buf;
}

/* Counts the chapters so a caller can decide on numbering width. OGM files
 * conventionally number from 01 even for short chapter lists, so the minimum is
 * two digits. */
static size_t chapter_digits(size_t count) {
    size_t digits = 2;
    while (count >= 100) {
        count /= 10;
        digits++;
    }
    return digits;
}

/* ------------------------------------------------------------------ */
/* OGM (the format OKEGui uses most)                                  */
/* ------------------------------------------------------------------ */

/* OGM chapter format:
 *
 *   CHAPTER01=00:00:00.000
 *   CHAPTER01NAME=Chapter 01
 *
 * The NAME line is omitted when the chapter has no name. */
static tc_status write_ogm(const tc_entry *e, const char *language,
                           const char *source_name, tc_buf *out) {
    (void)language;
    (void)source_name;

    size_t width = chapter_digits(e->chapter_count);
    for (size_t i = 0; i < e->chapter_count; i++) {
        const tc_chapter_impl *c = &e->chapters[i];
        char ts[32];
        tc_format_timestamp(c->time_ns, ts, sizeof(ts));
        tc_buf_puts(out, "CHAPTER");
        tc_buf_put_size(out, i + 1, (int)width);
        tc_buf_putc(out, '=');
        tc_buf_puts(out, ts);
        tc_buf_puts(out, "\r\n");
        /* The name line is always written: a chapter list without names is
         * useless to the operator, and the legacy writer defaulted too. */
        tc_buf_puts(out, "CHAPTER");
        tc_buf_put_size(out, i + 1, (int)width);
        tc_buf_puts(out, "NAME=");
        tc_buf_puts(out, name_or_default(c->name, i));
        tc_buf_puts(out, "\r\n");
    }
    return out->oom ? TC_E_NOMEM : TC_OK;
}

/* ------------------------------------------------------------------ */
/* Matroska / plain XML                                               */
/* ------------------------------------------------------------------ */

static void xml_escape_into(tc_buf *out, const char *s) {
    for (; s && *s; s++) {
        switch (*s) {
        case '&':  tc_buf_puts(out, "&amp;");  break;
        case '<':  tc_buf_puts(out, "&lt;");   break;
        case '>':  tc_buf_puts(out, "&gt;");   break;
        case '"':  tc_buf_puts(out, "&quot;"); break;
        case '\'': tc_buf_puts(out, "&apos;"); break;
        default:   tc_buf_putc(out, *s);       break;
        }
    }
}

/* Matroska chapter XML. `source_name` becomes the SegmentUID-less
 * ChapterStringUID anchor's source reference; it is optional. */
static tc_status write_xml(const tc_entry *e, const char *language,
                           const char *source_name, tc_buf *out) {
    (void)language;

    tc_buf_puts(out, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\r\n");
    tc_buf_puts(out, "<Chapters>\r\n");
    tc_buf_puts(out, "  <EditionEntry>\r\n");
    if (e->title && e->title[0]) {
        tc_buf_puts(out, "    <EditionUID>0</EditionUID>\r\n");
    }
    for (size_t i = 0; i < e->chapter_count; i++) {
        const tc_chapter_impl *c = &e->chapters[i];
        char ts[32];
        tc_format_timestamp(c->time_ns, ts, sizeof(ts));

        tc_buf_puts(out, "    <ChapterAtom>\r\n");
        tc_buf_puts(out, "      <ChapterTimeStart>");
        tc_buf_puts(out, ts);
        tc_buf_puts(out, "</ChapterTimeStart>\r\n");
        tc_buf_puts(out, "      <ChapterFlagHidden>0</ChapterFlagHidden>\r\n");
        tc_buf_puts(out, "      <ChapterFlagEnabled>1</ChapterFlagEnabled>\r\n");
        tc_buf_puts(out, "      <ChapterDisplay>\r\n");
        tc_buf_puts(out, "        <ChapterString>");
        xml_escape_into(out, name_or_default(c->name, i));
        tc_buf_puts(out, "</ChapterString>\r\n");
        if (language && language[0]) {
            tc_buf_puts(out, "        <ChapterLanguage>");
            tc_buf_puts(out, language);
            tc_buf_puts(out, "</ChapterLanguage>\r\n");
        }
        tc_buf_puts(out, "      </ChapterDisplay>\r\n");
        tc_buf_puts(out, "    </ChapterAtom>\r\n");
    }
    tc_buf_puts(out, "  </EditionEntry>\r\n");
    tc_buf_puts(out, "</Chapters>\r\n");
    (void)source_name;
    return out->oom ? TC_E_NOMEM : TC_OK;
}

/* ------------------------------------------------------------------ */
/* XPL (Zoom Player playlist)                                         */
/* ------------------------------------------------------------------ */

/* XPL chapter format, used by some authoring tools:
 *
 *   <?xml version="1.0"?>
 *   <XPL>
 *     <title name="...">
 *       <chapter name="..." time="HH:MM:SS.mmm" />
 *     </title>
 *   </XPL>
 */
static tc_status write_xpl(const tc_entry *e, const char *language,
                           const char *source_name, tc_buf *out) {
    (void)language;

    tc_buf_puts(out, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\r\n");
    tc_buf_puts(out, "<XPL>\r\n");
    tc_buf_puts(out, "  <title name=\"");
    xml_escape_into(out, source_name && source_name[0] ? source_name
                                                       : (e->title ? e->title : ""));
    tc_buf_puts(out, "\">\r\n");
    for (size_t i = 0; i < e->chapter_count; i++) {
        const tc_chapter_impl *c = &e->chapters[i];
        char ts[32];
        tc_format_timestamp(c->time_ns, ts, sizeof(ts));
        tc_buf_puts(out, "    <chapter name=\"");
        xml_escape_into(out, name_or_default(c->name, i));
        tc_buf_puts(out, "\" time=\"");
        tc_buf_puts(out, ts);
        tc_buf_puts(out, "\" />\r\n");
    }
    tc_buf_puts(out, "  </title>\r\n");
    tc_buf_puts(out, "</XPL>\r\n");
    return out->oom ? TC_E_NOMEM : TC_OK;
}

/* ------------------------------------------------------------------ */
/* WebVTT                                                             */
/* ------------------------------------------------------------------ */

/* WebVTT cues. The final chapter's end time is the entry duration, or the last
 * chapter's start when the duration is unknown (a cue with no duration is
 * invalid). */
static tc_status write_vtt(const tc_entry *e, const char *language,
                           const char *source_name, tc_buf *out) {
    (void)source_name;

    tc_buf_puts(out, "WEBVTT\r\n");
    if (language && language[0]) {
        tc_buf_puts(out, "Language: ");
        tc_buf_puts(out, language);
        tc_buf_puts(out, "\r\n");
    }
    tc_buf_puts(out, "\r\n");

    for (size_t i = 0; i < e->chapter_count; i++) {
        const tc_chapter_impl *c = &e->chapters[i];
        int64_t end;
        if (i + 1 < e->chapter_count) {
            end = e->chapters[i + 1].time_ns;
        } else if (e->duration_ns > c->time_ns) {
            end = e->duration_ns;
        } else {
            end = c->time_ns;
        }

        char start_ts[32], end_ts[32];
        tc_format_timestamp(c->time_ns, start_ts, sizeof(start_ts));
        tc_format_timestamp(end, end_ts, sizeof(end_ts));

        tc_buf_put_size(out, i + 1, 0);
        tc_buf_puts(out, "\r\n");
        tc_buf_puts(out, start_ts);
        tc_buf_puts(out, " --> ");
        tc_buf_puts(out, end_ts);
        tc_buf_puts(out, "\r\n");
        tc_buf_puts(out, name_or_default(c->name, i));
        tc_buf_puts(out, "\r\n\r\n");
    }
    return out->oom ? TC_E_NOMEM : TC_OK;
}

/* ------------------------------------------------------------------ */
/* CUE                                                                */
/* ------------------------------------------------------------------ */

/* CUE sheets are only meaningful for audio tracks, but a chapter list maps
 * cleanly onto one track per chapter, which is what the legacy writer did. */
static tc_status write_cue(const tc_entry *e, const char *language,
                           const char *source_name, tc_buf *out) {
    tc_buf_puts(out, "REM GENERATED BY OKEGUIDX\r\n");
    if (e->title && e->title[0]) {
        tc_buf_puts(out, "TITLE \"");
        tc_buf_puts(out, e->title);
        tc_buf_puts(out, "\"\r\n");
    }
    tc_buf_puts(out, "FILE \"");
    tc_buf_puts(out, source_name && source_name[0] ? source_name : "");
    tc_buf_puts(out, "\" WAVE\r\n");

    for (size_t i = 0; i < e->chapter_count; i++) {
        const tc_chapter_impl *c = &e->chapters[i];
        char ts[32];
        tc_format_timestamp(c->time_ns, ts, sizeof(ts));
        tc_buf_puts(out, "  TRACK ");
        tc_buf_put_size(out, i + 1, 2);
        tc_buf_puts(out, " AUDIO\r\n");
        if (language && language[0]) {
            tc_buf_puts(out, "    REM LANGUAGE ");
            tc_buf_puts(out, language);
            tc_buf_puts(out, "\r\n");
        }
        tc_buf_puts(out, "    TITLE \"");
        tc_buf_puts(out, name_or_default(c->name, i));
        tc_buf_puts(out, "\"\r\n");
        tc_buf_puts(out, "    INDEX 01 ");
        tc_buf_puts(out, ts);
        tc_buf_puts(out, "\r\n");
    }
    return out->oom ? TC_E_NOMEM : TC_OK;
}

/* ------------------------------------------------------------------ */
/* Dispatch                                                           */
/* ------------------------------------------------------------------ */

tc_writer_fn tc_writer_for(tc_format fmt) {
    switch (fmt) {
    case TC_FMT_OGM:          return write_ogm;
    case TC_FMT_XML:          return write_xml;
    case TC_FMT_MATROSKA_XML: return write_xml;
    case TC_FMT_XPL:          return write_xpl;
    case TC_FMT_VTT:          return write_vtt;
    case TC_FMT_CUE:          return write_cue;
    default:                  return NULL;
    }
}
