/*
 * tchapter CLI - a thin JSON front end over libtchapter.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * The Go engine talks to this program rather than linking the library, because
 * cgo would cost pure GOOS/GOARCH cross-compilation for all five targets
 * (PLAN.md §3). The JSON contract is:
 *
 *   tchapter info <file>              -> {"version":1,"format":"...","entries":[...]}
 *   tchapter save <file> <out> [opts] -> writes a chapter file
 *   tchapter --version                -> {"version":"1.0.0","abi":1}
 *   tchapter --help
 *
 * Output is always a single JSON object on stdout. Errors are reported as
 * {"error":{"status":"E_FORMAT","message":"..."}} with a non-zero exit code, so
 * the caller never has to parse free-form text.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tchapter.h"

#define EXIT_USAGE 2
#define EXIT_FAILURE 1

static void json_string(FILE *f, const char *s) {
    fputc('"', f);
    for (; s && *s; s++) {
        unsigned char c = (unsigned char)*s;
        switch (c) {
        case '"':  fputs("\\\"", f); break;
        case '\\': fputs("\\\\", f); break;
        case '\b': fputs("\\b", f);  break;
        case '\f': fputs("\\f", f);  break;
        case '\n': fputs("\\n", f);  break;
        case '\r': fputs("\\r", f);  break;
        case '\t': fputs("\\t", f);  break;
        default:
            if (c < 0x20) {
                fprintf(f, "\\u%04x", c);
            } else {
                fputc((int)c, f);
            }
            break;
        }
    }
    fputc('"', f);
}

/* Prints a 64-bit integer without relying on "%lld", which older MSVCRT
 * runtimes do not support. The value is emitted as a bare JSON number; chapter
 * timestamps in nanoseconds exceed the exact range of a double, but they are
 * always integers, and JSON numbers without a fraction are read back exactly by
 * every JSON parser the Go side uses. */
static void print_i64(FILE *f, int64_t v) {
    char tmp[24];
    int n = 0;
    uint64_t u;
    if (v < 0) {
        fputc('-', f);
        u = (uint64_t)0 - (uint64_t)v;
    } else {
        u = (uint64_t)v;
    }
    if (u == 0) {
        tmp[n++] = '0';
    } else {
        while (u > 0) {
            tmp[n++] = (char)('0' + (u % 10));
            u /= 10;
        }
    }
    for (int i = n - 1; i >= 0; i--) {
        fputc(tmp[i], f);
    }
}

static void print_time(FILE *f, int64_t ns) {
    print_i64(f, ns);
}

static int print_error(tc_status st) {
    fputs("{\"error\":{\"status\":", stdout);
    json_string(stdout, tc_status_name(st));
    fputs(",\"message\":", stdout);
    json_string(stdout, tc_last_error());
    fputs("}}\n", stdout);
    return EXIT_FAILURE;
}

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

static tc_format parse_format(const char *name) {
    if (!name || !*name) return TC_FMT_AUTO;
    if (!strcmp(name, "auto"))         return TC_FMT_AUTO;
    if (!strcmp(name, "mpls"))         return TC_FMT_MPLS;
    if (!strcmp(name, "cue"))          return TC_FMT_CUE;
    if (!strcmp(name, "flac"))         return TC_FMT_FLAC;
    if (!strcmp(name, "ifo"))          return TC_FMT_IFO;
    if (!strcmp(name, "mp4"))          return TC_FMT_MP4;
    if (!strcmp(name, "ogm"))          return TC_FMT_OGM;
    if (!strcmp(name, "vtt"))          return TC_FMT_VTT;
    if (!strcmp(name, "xml"))          return TC_FMT_XML;
    if (!strcmp(name, "xpl"))          return TC_FMT_XPL;
    if (!strcmp(name, "tak"))          return TC_FMT_TAK;
    if (!strcmp(name, "matroska"))     return TC_FMT_MATROSKA_XML;
    if (!strcmp(name, "bdmv"))         return TC_FMT_BDMV;
    return (tc_format)-1;
}

static int cmd_info(int argc, char **argv) {
    if (argc < 3) {
        fprintf(stderr, "usage: tchapter info <file> [format]\n");
        return EXIT_USAGE;
    }
    tc_format fmt = parse_format(argc > 3 ? argv[3] : NULL);
    if ((int)fmt == -1) {
        fprintf(stderr, "unknown format: %s\n", argv[3]);
        return EXIT_USAGE;
    }

    tc_data *d = NULL;
    tc_status st = tc_parse_file(argv[2], fmt, &d);
    if (st != TC_OK) {
        return print_error(st);
    }

    fputs("{\"version\":1,\"format\":", stdout);
    json_string(stdout, format_name(fmt == TC_FMT_AUTO ? tc_detect_format(argv[2]) : fmt));
    fputs(",\"entries\":[", stdout);

    size_t entries = tc_entry_count(d);
    for (size_t i = 0; i < entries; i++) {
        tc_entry_info_t ei;
        if (tc_entry_info(d, i, &ei) != TC_OK) {
            continue;
        }
        if (i) fputc(',', stdout);
        fputs("{\"title\":", stdout);
        json_string(stdout, ei.title);
        fputs(",\"source\":", stdout);
        json_string(stdout, ei.source);
        fprintf(stdout, ",\"fps_num\":%lld,\"fps_den\":%lld,\"duration_ns\":",
                (long long)ei.fps_num, (long long)ei.fps_den);
        print_time(stdout, ei.duration_ns);
        fputs(",\"chapters\":[", stdout);

        size_t chapters = tc_chapter_count(d, i);
        for (size_t k = 0; k < chapters; k++) {
            tc_chapter_t c;
            if (tc_chapter_at(d, i, k, &c) != TC_OK) {
                continue;
            }
            if (k) fputc(',', stdout);
            fputs("{\"name\":", stdout);
            json_string(stdout, c.name);
            fputs(",\"time_ns\":", stdout);
            print_time(stdout, c.time_ns);
            fputs(",\"frames\":", stdout);
            print_i64(stdout, c.frames);
            fputc('}', stdout);
        }
        fputs("]}", stdout);
    }
    fputs("]}\n", stdout);
    tc_free(d);
    return 0;
}

static int cmd_save(int argc, char **argv) {
    if (argc < 5) {
        fprintf(stderr, "usage: tchapter save <file> <output> <format> [language] [source]\n");
        return EXIT_USAGE;
    }
    tc_format in_fmt = TC_FMT_AUTO;
    tc_format out_fmt = parse_format(argv[4]);
    if ((int)out_fmt == -1) {
        fprintf(stderr, "unknown output format: %s\n", argv[4]);
        return EXIT_USAGE;
    }

    tc_data *d = NULL;
    tc_status st = tc_parse_file(argv[2], in_fmt, &d);
    if (st != TC_OK) {
        return print_error(st);
    }
    st = tc_save(d, 0, out_fmt, argv[3], argc > 5 ? argv[5] : NULL,
                 argc > 6 ? argv[6] : NULL);
    if (st != TC_OK) {
        tc_free(d);
        return print_error(st);
    }
    tc_free(d);
    fputs("{\"ok\":true}\n", stdout);
    return 0;
}

int main(int argc, char **argv) {
    if (argc < 2) {
        fprintf(stderr,
                "tchapter %s - chapter parsing\n"
                "usage:\n"
                "  tchapter info <file> [format]\n"
                "  tchapter save <file> <output> <format> [language] [source]\n"
                "  tchapter --version\n",
                tc_version());
        return EXIT_USAGE;
    }
    if (!strcmp(argv[1], "--version") || !strcmp(argv[1], "-v")) {
        printf("{\"version\":\"%s\",\"abi\":1}\n", tc_version());
        return 0;
    }
    if (!strcmp(argv[1], "--help") || !strcmp(argv[1], "-h")) {
        printf("tchapter %s\nformats: mpls cue flac ifo mp4 ogm vtt xml xpl tak matroska bdmv\n",
               tc_version());
        return 0;
    }
    if (!strcmp(argv[1], "info")) {
        return cmd_info(argc, argv);
    }
    if (!strcmp(argv[1], "save")) {
        return cmd_save(argc, argv);
    }
    fprintf(stderr, "unknown command: %s\n", argv[1]);
    return EXIT_USAGE;
}
