/*
 * Public API entry points and format dispatch.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#include <ctype.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_internal.h"

/* ------------------------------------------------------------------ */
/* Format detection                                                   */
/* ------------------------------------------------------------------ */

/* Maps a format to its short name, used in diagnostics. Kept private: the
 * public header only needs the extension accessor. */
static const char *format_name(tc_format fmt) {
    switch (fmt) {
    case TC_FMT_MPLS:         return "mpls";
    case TC_FMT_CUE:          return "cue";
    case TC_FMT_FLAC:         return "flac";
    case TC_FMT_IFO:          return "ifo";
    case TC_FMT_MP4:          return "mp4";
    case TC_FMT_OGM:          return "ogm";
    case TC_FMT_VTT:          return "vtt";
    case TC_FMT_XML:          return "xml";
    case TC_FMT_XPL:          return "xpl";
    case TC_FMT_TAK:          return "tak";
    case TC_FMT_MATROSKA_XML: return "matroska_xml";
    case TC_FMT_BDMV:         return "bdmv";
    default:                  return "auto";
    }
}

/* Maps a file extension to a format. Used as a hint; the content is checked
 * first for formats that have a recognisable signature. */
static tc_format format_from_extension(const char *path) {
    const char *dot = strrchr(path, '.');
    if (!dot) {
        return TC_FMT_AUTO;
    }
    if (tc_ieq(dot, ".mpls")) return TC_FMT_MPLS;
    if (tc_ieq(dot, ".cue"))  return TC_FMT_CUE;
    if (tc_ieq(dot, ".flac")) return TC_FMT_FLAC;
    if (tc_ieq(dot, ".ifo"))  return TC_FMT_IFO;
    if (tc_ieq(dot, ".mp4"))  return TC_FMT_MP4;
    if (tc_ieq(dot, ".m4a"))  return TC_FMT_MP4;
    if (tc_ieq(dot, ".ogm"))  return TC_FMT_OGM;
    if (tc_ieq(dot, ".ogm.txt")) return TC_FMT_OGM;
    if (tc_ieq(dot, ".vtt"))  return TC_FMT_VTT;
    if (tc_ieq(dot, ".xpl"))  return TC_FMT_XPL;
    if (tc_ieq(dot, ".tak"))  return TC_FMT_TAK;
    if (tc_ieq(dot, ".xml"))  return TC_FMT_XML;
    if (tc_ieq(dot, ".txt"))  return TC_FMT_OGM;
    return TC_FMT_AUTO;
}

/* Reads the first bytes of a file. Returns the number of bytes read. */
static size_t read_head(const char *path, unsigned char *buf, size_t cap) {
    FILE *f = fopen(path, "rb");
    if (!f) {
        return 0;
    }
    size_t n = fread(buf, 1, cap, f);
    fclose(f);
    return n;
}

/* Public wrapper: the header exposes tc_detect_format, while the implementation
 * detail tc_detect_from_file carries the extra argument checks. */
tc_format tc_detect_format(const char *path) {
    return tc_detect_from_file(path);
}

tc_format tc_detect_from_file(const char *path) {
    if (!path) {
        return TC_FMT_AUTO;
    }

    unsigned char head[64];
    size_t n = read_head(path, head, sizeof(head));

    if (n >= 8 && memcmp(head, "MPLS", 4) == 0) {
        return TC_FMT_MPLS;
    }
    if (n >= 4 && memcmp(head, "fLaC", 4) == 0) {
        return TC_FMT_FLAC;
    }
    if (n >= 12 && memcmp(head + 4, "ftyp", 4) == 0) {
        return TC_FMT_MP4;
    }
    if (n >= 4 && memcmp(head, "tBaK", 4) == 0) {
        return TC_FMT_TAK;
    }
    /* DVD IFO files start with "DVDVIDEO-VMG" or "DVDVIDEO-VTS". */
    if (n >= 12 && memcmp(head, "DVDVIDEO-VM", 11) == 0) {
        return TC_FMT_IFO;
    }
    /* "RIFF" covers OGM-era AVI/OGM containers. */
    if (n >= 4 && memcmp(head, "RIFF", 4) == 0) {
        return TC_FMT_OGM;
    }

    /* Text formats: look for a signature, then fall back to the extension. */
    if (n > 0) {
        /* WEBVTT must be at the very start, optionally after a BOM. */
        const char *s = (const char *)head;
        size_t off = 0;
        if (n >= 3 && head[0] == 0xEF && head[1] == 0xBB && head[2] == 0xBF) {
            off = 3;
        }
        if (n - off >= 6 && strncmp(s + off, "WEBVTT", 6) == 0) {
            return TC_FMT_VTT;
        }
        if (n - off >= 4 && strncmp(s + off, "# time", 6) == 0) {
            /* A timecode file; not a chapter format, but recognised so the
             * caller gets a clear error instead of a wrong parse. */
            return TC_FMT_AUTO;
        }
        /* OGM chapter text begins with a [CHAPTER] section header. */
        const char *bracket = strstr(s, "[CHAPTER]");
        if (bracket && (size_t)(bracket - s) < n) {
            return TC_FMT_OGM;
        }
    }

    tc_format by_ext = format_from_extension(path);
    if (by_ext != TC_FMT_AUTO) {
        return by_ext;
    }
    return TC_FMT_AUTO;
}

/* ------------------------------------------------------------------ */
/* Parser registry                                                    */
/* ------------------------------------------------------------------ */

const tc_parser *tc_parsers(void) {
    /* Populated as the parser packages land (WORKSTREAMS.md §2 B2..B13).
     * Keeping the table here means the dispatcher never has to change. */
    static const tc_parser table[] = {
        {TC_FMT_AUTO, NULL, NULL, NULL},
    };
    return table;
}

const tc_parser *tc_parser_for(tc_format fmt) {
    const tc_parser *p = tc_parsers();
    for (; p->format != TC_FMT_AUTO || p->name != NULL; p++) {
        if (p->format == fmt) {
            return p;
        }
    }
    return NULL;
}

/* ------------------------------------------------------------------ */
/* Public entry points                                                */
/* ------------------------------------------------------------------ */

tc_status tc_parse_file(const char *path, tc_format fmt, tc_data **out) {
    tc_clear_error();
    if (!path || !out) {
        return tc_fail(TC_E_INVALID, "parse_file: null argument");
    }
    *out = NULL;

    if (fmt == TC_FMT_AUTO) {
        fmt = tc_detect_from_file(path);
        if (fmt == TC_FMT_AUTO) {
            return tc_fail(TC_E_FORMAT, "cannot determine the chapter format of \"%s\"", path);
        }
    }

    const tc_parser *p = tc_parser_for(fmt);
    if (!p || !p->parse_file) {
        return tc_fail(TC_E_UNSUPPORTED, "the %s parser is not implemented yet",
                       format_name(fmt));
    }

    tc_data *d = tc_calloc(1, sizeof(*d));
    if (!d) {
        return TC_E_NOMEM;
    }

    tc_status st = p->parse_file(path, d);
    if (st != TC_OK) {
        tc_data_clear(d);
        return st;
    }
    if (d->entry_count == 0) {
        tc_data_clear(d);
        return tc_fail(TC_E_FORMAT, "%s: parser produced no entries", p->name);
    }
    *out = d;
    return TC_OK;
}

tc_status tc_parse_mem(const void *buf, size_t len, tc_format fmt,
                       const char *name_hint, tc_data **out) {    tc_clear_error();
    if (!buf || !out) {
        return tc_fail(TC_E_INVALID, "parse_mem: null argument");
    }
    *out = NULL;

    if (fmt == TC_FMT_AUTO) {
        if (name_hint) {
            fmt = format_from_extension(name_hint);
        }
        if (fmt == TC_FMT_AUTO) {
            return tc_fail(TC_E_FORMAT, "cannot determine the chapter format from memory");
        }
    }

    const tc_parser *p = tc_parser_for(fmt);
    if (!p || !p->parse_mem) {
        return tc_fail(TC_E_UNSUPPORTED, "the %s parser is not implemented yet",
                       format_name(fmt));
    }

    tc_data *d = tc_calloc(1, sizeof(*d));
    if (!d) {
        return TC_E_NOMEM;
    }

    tc_status st = p->parse_mem(buf, len, name_hint, d);
    if (st != TC_OK) {
        tc_data_clear(d);
        return st;
    }
    if (d->entry_count == 0) {
        tc_data_clear(d);
        return tc_fail(TC_E_FORMAT, "%s: parser produced no entries", p->name);
    }
    *out = d;
    return TC_OK;
}

size_t tc_entry_count(const tc_data *d) { return d ? d->entry_count : 0; }

tc_status tc_entry_info(const tc_data *d, size_t index, tc_entry_info_t *out) {
    tc_clear_error();
    if (!d || !out) {
        return tc_fail(TC_E_INVALID, "entry_info: null argument");
    }
    if (index >= d->entry_count) {
        return tc_fail(TC_E_RANGE, "entry index %zu out of range (count %zu)", index, d->entry_count);
    }
    const tc_entry *e = &d->entries[index];
    out->title = e->title ? e->title : "";
    out->source = e->source ? e->source : "";
    out->fps_num = e->fps_num;
    out->fps_den = e->fps_den ? e->fps_den : 1;
    out->duration_ns = e->duration_ns;
    out->chapter_count = e->chapter_count;
    return TC_OK;
}

size_t tc_chapter_count(const tc_data *d, size_t entry) {
    if (!d || entry >= d->entry_count) {
        return 0;
    }
    return d->entries[entry].chapter_count;
}

tc_status tc_chapter_at(const tc_data *d, size_t entry, size_t index, tc_chapter_t *out) {
    tc_clear_error();
    if (!d || !out) {
        return tc_fail(TC_E_INVALID, "chapter_at: null argument");
    }
    if (entry >= d->entry_count) {
        return tc_fail(TC_E_RANGE, "entry index %zu out of range (count %zu)", entry, d->entry_count);
    }
    const tc_entry *e = &d->entries[entry];
    if (index >= e->chapter_count) {
        return tc_fail(TC_E_RANGE, "chapter index %zu out of range (count %zu)", index, e->chapter_count);
    }
    const tc_chapter_impl *c = &e->chapters[index];
    out->name = c->name ? c->name : "";
    out->time_ns = c->time_ns;
    out->frames = c->frames;
    return TC_OK;
}

tc_status tc_render(const tc_data *d, size_t entry, tc_format fmt,
                    const char *language, const char *source_name,
                    char *buf, size_t *len) {
    tc_clear_error();
    if (!d || !len) {
        return tc_fail(TC_E_INVALID, "render: null argument");
    }
    if (entry >= d->entry_count) {
        return tc_fail(TC_E_RANGE, "entry index %zu out of range (count %zu)", entry, d->entry_count);
    }

    tc_writer_fn w = tc_writer_for(fmt);
    if (!w) {
        return tc_fail(TC_E_UNSUPPORTED, "format %s is not writable", tc_format_extension(fmt));
    }

    tc_buf out;
    tc_buf_init(&out);
    tc_status st = w(&d->entries[entry], language, source_name, &out);
    if (st != TC_OK) {
        tc_buf_free(&out);
        return st;
    }
    if (out.oom) {
        tc_buf_free(&out);
        return tc_fail(TC_E_NOMEM, "render: out of memory");
    }

    size_t need = out.len + 1;
    if (!buf || *len < need) {
        *len = need;
        tc_buf_free(&out);
        return tc_fail(TC_E_RANGE, "buffer too small: need %zu bytes", need);
    }
    memcpy(buf, out.data ? out.data : "", out.len);
    buf[out.len] = '\0';
    *len = out.len;
    tc_buf_free(&out);
    return TC_OK;
}

tc_status tc_save(const tc_data *d, size_t entry, tc_format fmt,
                  const char *path, const char *language, const char *source_name) {
    tc_clear_error();
    if (!d || !path) {
        return tc_fail(TC_E_INVALID, "save: null argument");
    }

    size_t need = 0;
    /* First call sizes the buffer; the second fills it. */
    tc_status st = tc_render(d, entry, fmt, language, source_name, NULL, &need);
    if (st != TC_E_RANGE) {
        return st;
    }
    char *buf = tc_malloc(need);
    if (!buf) {
        return TC_E_NOMEM;
    }
    size_t len = need;
    st = tc_render(d, entry, fmt, language, source_name, buf, &len);
    if (st != TC_OK) {
        free(buf);
        return st;
    }

    FILE *f = fopen(path, "wb");
    if (!f) {
        free(buf);
        return tc_fail(TC_E_IO, "cannot create \"%s\"", path);
    }
    /* UTF-8 BOM: the formats this library writes are consumed by Windows tools
     * that assume a BOM for non-ASCII chapter names. */
    static const unsigned char bom[] = {0xEF, 0xBB, 0xBF};
    int ok = fwrite(bom, 1, sizeof(bom), f) == sizeof(bom);
    ok = ok && (len == 0 || fwrite(buf, 1, len, f) == len);
    free(buf);
    if (fclose(f) != 0) {
        ok = 0;
    }
    if (!ok) {
        return tc_fail(TC_E_IO, "failed to write \"%s\"", path);
    }
    return TC_OK;
}
