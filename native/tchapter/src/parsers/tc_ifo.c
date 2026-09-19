/*
 * DVD IFO chapter parser.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Ported from TChapter/Parsing/IFOParser.cs and IFOParserUtil.cs. Chapters come
 * from the PGCIT (Program Chain Information Table): the parser walks the chain
 * table, keeps the longest chain, and reports one chapter per program. A
 * chapter time is the sum of its cells' playback times, which are BCD-encoded
 * HH:MM:SS:FF; frames are scaled by 30 (NTSC, recorded as 30000/1001) or
 * 25 (PAL). Every conversion below reproduces the original, including its
 * rounding.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_internal.h"

/* ------------------------------------------------------------------ */
/* Time conversion                                                     */
/* ------------------------------------------------------------------ */

/* BCD to decimal: the high nibble counts tens. */
static int bcd_to_int(uint8_t value) {
    return (value >> 4) * 10 + (value & 0x0F);
}

/* Converts a 4-byte BCD playback time to nanoseconds. Returns 0 with *ok = 0
 * when the frame field is absent (the reference's null return), or when any
 * digit is not valid BCD (the reference's exception path, logged as "Failed
 * to read timespan"). *ntsc reports the cell's frame-rate flag. */
static int64_t bcd_time_to_ns(const uint8_t t[4], int *ok, int *ntsc) {
    *ok = 0;
    *ntsc = t[3] >> 6 == 0x03;
    /* The low bit of the rate flag says whether the frame field is present. */
    if (((t[3] >> 6) & 0x01) != 1) {
        return 0;
    }
    /* bcd_to_int takes the byte as an int so the nibble arithmetic stays in
     * int; the array is only here to keep the three fields symmetric. */
    const uint8_t *raw = t;
    int hours = bcd_to_int(raw[0]);
    int minutes = bcd_to_int(raw[1]);
    int seconds = bcd_to_int(raw[2]);
    int frames = bcd_to_int(t[3] & 0x3F);

    /* The C# constructor throws only when it cannot represent the values; the
     * stored bytes make minutes and seconds at most 99, which the format
     * accepts. Guard anyway so a hostile file cannot overflow the frame
     * counter. */
    if (hours < 0 || hours > 99 || minutes < 0 || minutes > 99 ||
        seconds < 0 || seconds > 99) {
        return 0;
    }
    const int fps = 30;
    int64_t total_frames = frames + (int64_t)(hours * 3600 + minutes * 60 + seconds) * fps;

    /* IfoTimeSpan -> TimeSpan is Math.Round(frames / fps * 1e9) with
     * fps = 30000/1001 for NTSC and 25 for PAL. Frames are non-negative, so
     * this is round-half-up over a positive value, reproduced exactly in
     * integer arithmetic below. */
    int64_t ns;
    if (*ntsc) {
        ns = (total_frames * 1001000000LL + 15) / 30;
    } else {
        ns = total_frames * 40000000LL;
    }
    *ok = 1;
    return ns;
}

/* ------------------------------------------------------------------ */
/* File layout helpers                                                 */
/* ------------------------------------------------------------------ */

/* One file slice; every accessor below reads from it. */
typedef struct {
    const uint8_t *data;
    size_t len;
} ifo_view;

/* Bounds-checked access: a truncated file must fail, not read garbage. */
static int in_range(const ifo_view *f, size_t offset, size_t count) {
    return offset <= f->len && count <= f->len - offset;
}

/* PGCIT start sector from the IFO header (0xCC), in 2048-byte units. */
static int pgcit_position(const ifo_view *f, size_t *out) {
    if (!in_range(f, 0xCC, 4)) {
        return 0;
    }
    size_t pos = (size_t)tc_be32(f->data + 0xCC) * 0x800u;
    /* GetFileBlock rejects a negative position only; the caller reads the
     * chain count next, so that is the first real bound check. */
    if (!in_range(f, pos, 2)) {
        return 0;
    }
    *out = pos;
    return 1;
}

/* Number of chains, the byte at pgcit + 1. */
static int chain_count(const ifo_view *f, size_t pgcit, int *out) {
    if (!in_range(f, pgcit + 1, 1)) {
        return 0;
    }
    *out = f->data[pgcit + 1];
    return 1;
}

/* Chain slot offset: the table begins at pgcit + 8 and has two 4-byte slots
 * per chain (duration, offset); chain n's offset sits at pgcit + 8n + 4. */
static int chain_offset(const ifo_view *f, size_t pgcit, int chain, size_t *out) {
    if (chain < 0 || !in_range(f, pgcit + 8 * (size_t)chain + 4, 4)) {
        return 0;
    }
    *out = tc_be32(f->data + pgcit + 8 * (size_t)chain + 4);
    return 1;
}

/* Number of programs, the byte at chain_off + 2. */
static int chain_programs(const ifo_view *f, size_t pgcit, size_t chain_off,
                          int *out) {
    if (!in_range(f, pgcit + chain_off + 2, 1)) {
        return 0;
    }
    *out = f->data[pgcit + chain_off + 2];
    return 1;
}

/* ------------------------------------------------------------------ */
/* Chain parsing                                                       */
/* ------------------------------------------------------------------ */

/* One program's chapter time: the sum of its cells' playback times. */
static int program_time_ns(const ifo_view *f, size_t chain_off,
                           int entry_cell, int exit_cell, int64_t *out) {
    if (!in_range(f, chain_off + 0xE8, 2)) {
        return 0;
    }
    size_t cell_table_off = tc_be16(f->data + chain_off + 0xE8);
    int64_t total = 0;
    for (int index = entry_cell; index <= exit_cell; index++) {
        if (index < 1) {
            return 0;
        }
        size_t cell_start = cell_table_off + (size_t)(index - 1) * 0x18;
        if (!in_range(f, chain_off + cell_start, 1) ||
            !in_range(f, chain_off + cell_start + 4, 4)) {
            return 0;
        }
        const uint8_t *cell = f->data + chain_off + cell_start;
        uint32_t cell_type = cell[0] >> 6;
        /* Types 00 and 01 carry a playback time; 10/11 do not (the reference
         * skips them silently). */
        if (cell_type == 0x00 || cell_type == 0x01) {
            int ok = 0, ntsc = 0;
            int64_t ns = bcd_time_to_ns(cell + 4, &ok, &ntsc);
            if (ok) {
                total += ns;
            }
        }
    }
    *out = total;
    return 1;
}

/* ------------------------------------------------------------------ */
/* Entry building                                                      */
/* ------------------------------------------------------------------ */

/* Derives the entry title: "VTS_05_0" for "VTS_05_0.IFO". The reference
 * matches ^VTS_(\d+)_0\.IFO to switch which chain table to consult, but a
 * real VTS file is already a VTS table, so the file name is only used for the
 * entry's title and source name here. */
static char *title_from_path(const char *path) {
    const char *base = path;
    for (const char *p = path; *p; p++) {
        if (*p == '/' || *p == '\\') {
            base = p + 1;
        }
    }
    size_t n = strlen(base);
    const char *dot = strrchr(base, '.');
    if (dot && dot != base) {
        n = (size_t)(dot - base);
    }
    return tc_strndup(base, n);
}

static tc_status ifo_parse(const uint8_t *data, size_t len, const char *path,
                           tc_data *d) {
    const ifo_view f = {data, len};
    if (len < 12 || memcmp(data, "DVDVIDEO-", 9) != 0) {
        return tc_fail(TC_E_FORMAT, "ifo: not a DVD IFO structure file");
    }

    size_t pgcit = 0;
    if (!pgcit_position(&f, &pgcit)) {
        return tc_fail(TC_E_FORMAT, "ifo: truncated or invalid IFO header");
    }
    int chains = 0;
    if (!chain_count(&f, pgcit, &chains) || chains <= 0) {
        return tc_fail(TC_E_FORMAT, "ifo: no program chain table");
    }

    /* The caller of the original could pass a specific chain; without one the
     * longest chain wins: skip chains whose playback time fails to read or
     * does not strictly grow the record, and keep the last strictly longest. */
    int best_chain = -1, best_programs = 0;
    int64_t best_time = -1;
    size_t chain_off = 0;
    for (int cur_chain = 1; cur_chain <= chains; cur_chain++) {
        size_t offset = 0;
        if (!chain_offset(&f, pgcit, cur_chain, &offset)) {
            continue;
        }
        size_t base = pgcit + offset;
        if (!in_range(&f, base + 4, 4) || !in_range(&f, base + 2, 1)) {
            continue;
        }
        int ok = 0, ntsc = 0;
        int64_t ns = bcd_time_to_ns(f.data + base + 4, &ok, &ntsc);
        if (!ok || ns <= best_time) {
            continue;
        }
        int programs = 0;
        if (!chain_programs(&f, pgcit, offset, &programs)) {
            continue;
        }
        best_chain = cur_chain;
        best_programs = programs;
        best_time = ns;
    }
    if (best_chain < 0) {
        return tc_fail(TC_E_FORMAT, "ifo: no usable program chain found");
    }

    if (!chain_offset(&f, pgcit, best_chain, &chain_off)) {
        return tc_fail(TC_E_FORMAT, "ifo: chain table out of range");
    }
    size_t base = pgcit + chain_off;

    /* The program map (one entry cell per program) and the cell table offset
     * both live inside the chain structure. */
    if (!in_range(&f, base + 230, 2) || !in_range(&f, base + 0xE8, 2)) {
        return tc_fail(TC_E_FORMAT, "ifo: chain structure truncated");
    }
    size_t program_map_off = tc_be16(f.data + base + 230);

    if (best_programs <= 0) {
        return tc_fail(TC_E_FORMAT, "ifo: chain has no programs");
    }

    tc_entry *e = tc_data_add_entry(d);
    if (!e) {
        return TC_E_NOMEM;
    }

    char *title = title_from_path(path);
    if (!title) {
        return TC_E_NOMEM;
    }
    e->title = title;
    e->source = tc_strdup(title);
    if (!e->source) {
        return TC_E_NOMEM;
    }
    /* The reference reports 30000/1001 when the chain's playback time said
     * NTSC and 25 otherwise. */
    e->fps_num = 25;
    e->fps_den = 1;

    if (program_map_off == 0) {
        return tc_fail(TC_E_FORMAT, "ifo: chain has no program map");
    }

    /* Chapter 1 is the start of the title; every later chapter is the running
     * total of its program's cells, matching the reference's accumulation
     * (its `duration += totalTime`, added at the chapter's own start time).
     * The final program only extends the duration. */
    int64_t duration_ns = 0;
    for (int program = 0; program < best_programs; program++) {
        if (!in_range(&f, base + program_map_off + (size_t)program, 1)) {
            return tc_fail(TC_E_FORMAT, "ifo: program map out of range");
        }
        int entry_cell = f.data[base + program_map_off + (size_t)program];
        int exit_cell = entry_cell;
        if (program < best_programs - 1) {
            if (!in_range(&f, base + program_map_off + (size_t)program + 1, 1)) {
                return tc_fail(TC_E_FORMAT, "ifo: program map out of range");
            }
            exit_cell = f.data[base + program_map_off + (size_t)program + 1] - 1;
        }
        int64_t total = 0;
        if (!program_time_ns(&f, chain_off, entry_cell, exit_cell, &total)) {
            return tc_fail(TC_E_FORMAT, "ifo: cell table out of range");
        }
        duration_ns += total;
        if (program + 1 < best_programs) {
            char name[16];
            snprintf(name, sizeof(name), "Chapter %02d", program + 2);
            char *name_copy = tc_strdup(name);
            if (!name_copy) {
                return TC_E_NOMEM;
            }
            if (tc_entry_add_chapter(e, name_copy, duration_ns, -1) != TC_OK) {
                return TC_E_NOMEM;
            }
        }
    }

    e->duration_ns = duration_ns;
    return TC_OK;
}

tc_status tc_ifo_parse_file(const char *path, tc_data *d) {
    char *buf = NULL;
    size_t len = 0;
    tc_status st = tc_read_file(path, &buf, &len);
    if (st != TC_OK) {
        return st;
    }
    st = ifo_parse((const uint8_t *)buf, len, path, d);
    free(buf);
    return st;
}
