/*
 * Blu-ray disc structure (BDMV) chapter parser.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Ported from TChapter/Parsing/BDMVParser.cs. The C# class is a thin shell: it
 * locates BDMV/PLAYLIST, reads the disc title out of BDMV/META/DL (and then
 * never uses it), runs eac3to on the disc root, scrapes the playlist list and
 * each playlist's duration out of eac3to's console output, and parses every
 * playlist it found with MPLSParser, combining the per-clip chapters into one
 * entry with ChapterUtil.CombineChapter.
 *
 * Only the directory and playlist resolution is portable, and that is all this
 * file does; the external program is gone. What the original got from eac3to,
 * and what happens instead:
 *
 *  1. which playlists exist. eac3to lists the ones it considers playable, in
 *     its own order. This port enumerates the .mpls files in BDMV/PLAYLIST
 *     directly and sorts them by total duration, longest first, with the file
 *     name as the tie-breaker so the order is stable. That reproduces eac3to's
 *     ordering on the reference disc and, unlike it, does not depend on a
 *     program.
 *  2. the duration. eac3to reports it in whole seconds and the C# code
 *     overwrites the playlist's own sum with it. Here the duration stays the
 *     sum of the play items' IN/OUT deltas, i.e. exactly what MPLSParser
 *     computes (and what CombineChapter sums again over the play items).
 *  3. the disc title. bdmvTitle is assigned and then unused: the method never
 *     sets ChapterInfo.Title, so the disc title does not reach the result in
 *     the original either. It is not read here.
 *
 * The Go engine's replacement for eac3to (PLAN.md §2.1: ffprobe for stream
 * enumeration) is not called from here either, and no other external process
 * is. This parser answers "which playlists does the disc have, and what
 * chapters does each of them carry"; anything that needs the media itself
 * stays on the Go side.
 *
 * Deliberate additions, both reported:
 *  - each entry's source is the playlist file name (e.g. "00002.mpls"), so the
 *    caller can tell the entries apart and re-parse one directly. The original
 *    combined entries carry no SourceName at all. The clip (m2ts) to playlist
 *    mapping is available by parsing that playlist with the MPLS parser.
 *  - a playlist whose file cannot be parsed is skipped instead of failing the
 *    whole disc, because eac3to already filtered the list the original worked
 *    from; when no playlist at all can be parsed, the failure is reported.
 *
 * Entry title is "Full_Chapter", the value CombineChapter assigns.
 *
 * Directory separators: paths handed to the OS are built with the native
 * separator, and the input path may use either.
 */
#include <ctype.h>
#include <dirent.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/types.h>

#include "tc_internal.h"

#if defined(_WIN32)
#  define BDMV_SEP '\\'
#else
#  define BDMV_SEP '/'
#endif

/* Canonical spellings first; the alternates make an unusually cased directory
 * still resolve, which matters on a case-sensitive filesystem. */
static const char *const bdmv_dir_names[] = {"BDMV", "bdmv"};
static const char *const bdmv_playlist_names[] = {"PLAYLIST", "playlist", "Playlist"};
static const char *const bdmv_stream_names[] = {"STREAM", "stream", "Stream"};

/* BDMV's sub directories, from the Blu-ray format. Used only to recognise that
 * a path points inside a disc rather than at one. */
static const char *const bdmv_sub_names[] = {
    "PLAYLIST", "CLIPINF", "STREAM", "AUXDATA", "META", "BDJO",
    "JAR", "BACKUP", "BDMV",
};
#define BDMV_SUB_NAMES_COUNT (sizeof(bdmv_sub_names) / sizeof(bdmv_sub_names[0]))

/* ------------------------------------------------------------------ */
/* Paths                                                              */
/* ------------------------------------------------------------------ */

typedef struct bdmv_path {
    char *s;
    size_t len;
    size_t cap;
} bdmv_path;

static void bdmv_path_init(bdmv_path *p) {
    p->s = NULL;
    p->len = 0;
    p->cap = 0;
}

static void bdmv_path_free(bdmv_path *p) { free(p->s); }

static int bdmv_path_reserve(bdmv_path *p, size_t extra) {
    if (p->len + extra + 1 <= p->cap) {
        return 1;
    }
    size_t want = p->cap ? p->cap : 128;
    while (want < p->len + extra + 1) {
        if (want > (size_t)-1 / 2) {
            want = p->len + extra + 1;
            break;
        }
        want *= 2;
    }
    char *q = tc_realloc(p->s, want);
    if (!q) {
        return 0;
    }
    p->s = q;
    p->cap = want;
    return 1;
}

static int bdmv_path_append(bdmv_path *p, const char *s) {
    size_t n = strlen(s);
    if (!bdmv_path_reserve(p, n)) {
        return 0;
    }
    memcpy(p->s + p->len, s, n);
    p->len += n;
    p->s[p->len] = '\0';
    return 1;
}

static int bdmv_path_reset(bdmv_path *p, const char *s) {
    p->len = 0;
    if (p->s) {
        p->s[0] = '\0';
    }
    return bdmv_path_append(p, s);
}

/* True for either separator, so a path written with the other platform's
 * separator still behaves. */
static int bdmv_is_sep(char c) { return c == '/' || c == '\\'; }

/* Appends a separator unless the path already ends in one (a root such as "/"
 * would otherwise be doubled). Either separator counts, so a caller's
 * mixed-separator path keeps its shape. */
static int bdmv_path_append_sep(bdmv_path *p) {
    if (p->len == 0 || bdmv_is_sep(p->s[p->len - 1])) {
        return 1;
    }
    if (!bdmv_path_reserve(p, 1)) {
        return 0;
    }
    p->s[p->len++] = BDMV_SEP;
    p->s[p->len] = '\0';
    return 1;
}

static int bdmv_path_push(bdmv_path *p, const char *name) {
    return bdmv_path_append_sep(p) && bdmv_path_append(p, name);
}
/* Drops the last component, keeping the separator that precedes it. A trailing
 * separator is not a component, so "a/b/" and "a/b" both pop to "a/". */
static void bdmv_path_pop(bdmv_path *p) {
    size_t end = p->len;
    while (end > 0 && bdmv_is_sep(p->s[end - 1])) {
        end--;
    }
    if (end == 0) {
        /* A root such as "/" or "C:\" has no parent. */
        return;
    }
    size_t start = end;
    while (start > 0 && !bdmv_is_sep(p->s[start - 1])) {
        start--;
    }
    p->len = start;
    p->s[p->len] = '\0';
}

/* Compares the last component of the path against `name`, ignoring case. A
 * trailing separator is ignored, so a directory that was just pushed still
 * matches its own name. */
static int bdmv_path_component_is(const bdmv_path *p, const char *name) {
    size_t end = p->len;
    while (end > 0 && bdmv_is_sep(p->s[end - 1])) {
        end--;
    }
    size_t start = end;
    while (start > 0 && !bdmv_is_sep(p->s[start - 1])) {
        start--;
    }
    size_t len = end - start;
    if (len != strlen(name)) {
        return 0;
    }
    for (size_t i = 0; i < len; i++) {
        if (tolower((unsigned char)p->s[start + i]) !=
            tolower((unsigned char)name[i])) {
            return 0;
        }
    }
    return 1;
}

static int bdmv_is_dir(const char *path) {
    struct stat st;
    if (stat(path, &st) != 0) {
        return 0;
    }
    return S_ISDIR(st.st_mode) ? 1 : 0;
}

/* ------------------------------------------------------------------ */
/* Directory scanning                                                 */
/* ------------------------------------------------------------------ */

typedef struct bdmv_name_list {
    char **names;
    size_t count;
    size_t cap;
} bdmv_name_list;

static void bdmv_name_list_free(bdmv_name_list *l) {
    for (size_t i = 0; i < l->count; i++) {
        free(l->names[i]);
    }
    free(l->names);
    l->names = NULL;
    l->count = 0;
    l->cap = 0;
}

static int bdmv_name_list_add(bdmv_name_list *l, const char *name) {
    if (l->count == l->cap) {
        size_t cap = l->cap ? l->cap * 2 : 8;
        char **p = tc_realloc(l->names, cap * sizeof(*p));
        if (!p) {
            return 0;
        }
        l->names = p;
        l->cap = cap;
    }
    char *copy = tc_strdup(name);
    if (!copy) {
        return 0;
    }
    l->names[l->count++] = copy;
    return 1;
}

static int bdmv_has_suffix_ci(const char *name, const char *suffix) {
    size_t n = strlen(name);
    size_t m = strlen(suffix);
    if (n < m) {
        return 0;
    }
    return tc_ieq(name + (n - m), suffix);
}

/* Collects the regular files in `dir` whose name ends with `suffix`. A
 * directory that cannot be opened yields an empty list; callers probe with
 * bdmv_is_dir when that has to be distinguished from an empty directory.
 *
 * A directory whose name carries the suffix (a stray "foo.mpls" folder) is
 * skipped: only a regular file can be a playlist or a stream. */
static int bdmv_list_files(const char *dir, const char *suffix, bdmv_name_list *out) {
    DIR *d = opendir(dir);
    if (!d) {
        return 1;
    }
    bdmv_path full;
    bdmv_path_init(&full);
    int ok = 1;
    struct dirent *entry;
    while (ok && (entry = readdir(d)) != NULL) {
        if (strcmp(entry->d_name, ".") == 0 || strcmp(entry->d_name, "..") == 0) {
            continue;
        }
        if (!bdmv_has_suffix_ci(entry->d_name, suffix)) {
            continue;
        }
        if (!bdmv_path_reset(&full, dir) || !bdmv_path_push(&full, entry->d_name)) {
            ok = 0;
            break;
        }
        struct stat st;
        if (stat(full.s, &st) != 0 || !S_ISREG(st.st_mode)) {
            continue;
        }
        ok = bdmv_name_list_add(out, entry->d_name);
    }
    bdmv_path_free(&full);
    closedir(d);
    return ok;
}

/* ------------------------------------------------------------------ */
/* Locating the disc                                                  */
/* ------------------------------------------------------------------ */

/* Resolves one directory below `base`, trying each spelling and keeping the
 * case the filesystem really has. Returns 0 when none exists or on failure to
 * build the path; *out is only meaningful when 1 is returned. */
static int bdmv_find_child(const bdmv_path *base, const char *const *names,
                           size_t name_count, bdmv_path *out) {
    for (size_t i = 0; i < name_count; i++) {
        if (!bdmv_path_reset(out, base->s ? base->s : "")) {
            return 0;
        }
        if (!bdmv_path_push(out, names[i])) {
            return 0;
        }
        if (bdmv_is_dir(out->s)) {
            return 1;
        }
    }
    return 0;
}

/* Recursively looks for `BDMV` below `dir`, up to `depth` levels. Only a
 * directory whose name is a known BDMV component is descended into, so an
 * unrelated tree is not searched. Returns 1 and leaves `dir` as the parent of
 * the BDMV directory when one is found, 0 otherwise; `dir` is only meaningful
 * in the first case. */
static int bdmv_walk_up(bdmv_path *dir, int depth) {
    if (depth <= 0) {
        return 0;
    }
    bdmv_path probe;
    bdmv_path_init(&probe);
    int found = 0;

    if (bdmv_find_child(dir, bdmv_dir_names,
                        sizeof(bdmv_dir_names) / sizeof(bdmv_dir_names[0]), &probe)) {
        found = 1;
    } else {
        for (size_t i = 0; i < BDMV_SUB_NAMES_COUNT && !found; i++) {
            if (!bdmv_path_component_is(dir, bdmv_sub_names[i])) {
                continue;
            }
            bdmv_path_pop(dir);
            found = bdmv_walk_up(dir, depth - 1);
        }
    }

    bdmv_path_free(&probe);
    return found;
}

/* Turns the caller's path into the disc root, i.e. the directory that holds
 * BDMV. The C# parser takes that root; a caller may just as well point at the
 * BDMV directory or at a file inside the disc, so those are resolved first.
 * Nothing is guessed beyond the documented BDMV layout: when the path is not
 * inside a disc, the caller reports the same "structure not found" failure the
 * original raises.
 *
 * Returns 1 when *root holds a candidate disc root, 0 when the path cannot be
 * inside a disc and -1 on allocation failure. The candidate is only a
 * candidate: the caller still has to find BDMV below it. */
static int bdmv_find_root(const char *path, bdmv_path *root) {
    char *copy = tc_strdup(path ? path : "");
    if (!copy) {
        return -1;
    }
    size_t n = strlen(copy);
    while (n > 0 && (copy[n - 1] == '/' || copy[n - 1] == '\\')) {
        copy[--n] = '\0';
    }
    if (!bdmv_path_reset(root, copy)) {
        free(copy);
        return -1;
    }

    /* A file, or a path that no longer exists: start from its directory. */
    if (!bdmv_is_dir(root->s)) {
        bdmv_path_pop(root);
    }

    bdmv_path probe;
    bdmv_path_init(&probe);
    int result = 0;

    /* The path may point at the disc root, at the BDMV directory itself or at
     * anything inside it; walk up until BDMV is found. */
    if (bdmv_find_child(root, bdmv_dir_names,
                        sizeof(bdmv_dir_names) / sizeof(bdmv_dir_names[0]), &probe)) {
        result = 1;
    } else if (bdmv_find_child(root, bdmv_playlist_names,
                               sizeof(bdmv_playlist_names) / sizeof(bdmv_playlist_names[0]),
                               &probe)) {
        /* root/PLAYLIST: the caller gave the BDMV directory. */
        bdmv_path_pop(root);
        result = 1;
    } else {
        result = bdmv_walk_up(root, 4);
    }

    bdmv_path_free(&probe);
    free(copy);
    return result;
}

/* ------------------------------------------------------------------ */
/* Playlist summary                                                   */
/* ------------------------------------------------------------------ */

/* One playlist file plus what the ordering needs, collected by running the
 * MPLS parser over it once. The chapters themselves are produced by parsing
 * the file a second time, when it is actually selected; keeping every
 * playlist's chapters alive at once would cost far more than the second read. */
typedef struct bdmv_playlist {
    char *file; /* name inside BDMV/PLAYLIST */
    char *path; /* full path, native separators */
    int64_t duration_ns;
} bdmv_playlist;

static void bdmv_playlists_free(bdmv_playlist *list, size_t count) {
    if (!list) {
        return;
    }
    for (size_t i = 0; i < count; i++) {
        free(list[i].file);
        free(list[i].path);
    }
    free(list);
}

/* Longest first, file name as the tie-breaker so the order does not depend on
 * the directory enumeration. */
static int bdmv_playlist_cmp(const void *a, const void *b) {
    const bdmv_playlist *x = (const bdmv_playlist *)a;
    const bdmv_playlist *y = (const bdmv_playlist *)b;
    if (x->duration_ns != y->duration_ns) {
        return x->duration_ns > y->duration_ns ? -1 : 1;
    }
    return strcmp(x->file, y->file);
}

/* ------------------------------------------------------------------ */
/* Chapters                                                           */
/* ------------------------------------------------------------------ */

/* ChapterUtil.CombineChapter: each clip's chapters are appended at the running
 * offset and renamed from 1, the duration is the sum of the clip durations,
 * the frame rate is the first clip's and the title is the literal the original
 * assigns. */
static tc_status bdmv_combine(const tc_data *clips, tc_entry *out) {
    int64_t offset = 0;
    size_t index = 1;
    for (size_t i = 0; i < clips->entry_count; i++) {
        const tc_entry *clip = &clips->entries[i];
        for (size_t k = 0; k < clip->chapter_count; k++) {
            char name[32];
            snprintf(name, sizeof(name), "Chapter %02u", (unsigned)index);
            char *copy = tc_strdup(name);
            if (!copy) {
                return TC_E_NOMEM;
            }
            tc_status st =
                tc_entry_add_chapter(out, copy, offset + clip->chapters[k].time_ns, -1);
            if (st != TC_OK) {
                return st;
            }
            index++;
        }
        offset += clip->duration_ns;
    }
    out->title = tc_strdup("Full_Chapter");
    out->duration_ns = offset;
    out->fps_num = clips->entries[0].fps_num;
    out->fps_den = clips->entries[0].fps_den;
    return out->title ? TC_OK : TC_E_NOMEM;
}

/* ------------------------------------------------------------------ */
/* Entry point                                                        */
/* ------------------------------------------------------------------ */

tc_status tc_bdmv_parse_file(const char *path, tc_data *d) {
    if (!path || !d) {
        return tc_fail(TC_E_INVALID, "bdmv: null argument");
    }

    bdmv_path root;
    bdmv_path bdmv_dir;
    bdmv_path playlist_dir;
    bdmv_path stream_dir;
    bdmv_path_init(&root);
    bdmv_path_init(&bdmv_dir);
    bdmv_path_init(&playlist_dir);
    bdmv_path_init(&stream_dir);

    bdmv_name_list files;
    memset(&files, 0, sizeof(files));
    bdmv_playlist *playlists = NULL;
    size_t playlist_count = 0;
    char *first_error = NULL;
    tc_status st = TC_OK;
    int found = bdmv_find_root(path, &root);
    if (found < 0) {
        st = TC_E_NOMEM;
        goto done;
    }
    if (found == 0 ||
        !bdmv_find_child(&root, bdmv_dir_names,
                         sizeof(bdmv_dir_names) / sizeof(bdmv_dir_names[0]), &bdmv_dir) ||
        !bdmv_find_child(&bdmv_dir, bdmv_playlist_names,
                         sizeof(bdmv_playlist_names) / sizeof(bdmv_playlist_names[0]),
                         &playlist_dir)) {
        st = tc_fail(TC_E_IO, "bdmv: Blu-ray disc structure not found under \"%s\"", path);
        goto done;
    }

    /* BDMV/STREAM is only used to tell the playlists apart; a disc image
     * without it still has chapters. */
    (void)bdmv_find_child(&bdmv_dir, bdmv_stream_names,
                          sizeof(bdmv_stream_names) / sizeof(bdmv_stream_names[0]),
                          &stream_dir);

    if (!bdmv_list_files(playlist_dir.s, ".mpls", &files)) {
        st = TC_E_NOMEM;
        goto done;
    }
    if (files.count == 0) {
        st = tc_fail(TC_E_IO, "bdmv: no playlists in \"%s\"", playlist_dir.s);
        goto done;
    }

    playlists = tc_calloc(files.count, sizeof(*playlists));
    if (!playlists) {
        st = TC_E_NOMEM;
        goto done;
    }
    playlist_count = files.count;

    /* Summarise every playlist, then order them. */
    for (size_t i = 0; i < playlist_count; i++) {
        bdmv_path p;
        bdmv_path_init(&p);
        if (!bdmv_path_reset(&p, playlist_dir.s) || !bdmv_path_push(&p, files.names[i])) {
            bdmv_path_free(&p);
            st = TC_E_NOMEM;
            goto done;
        }
        bdmv_playlist *pl = &playlists[i];
        pl->file = tc_strdup(files.names[i]);
        pl->path = tc_strdup(p.s);
        bdmv_path_free(&p);
        if (!pl->file || !pl->path) {
            st = TC_E_NOMEM;
            goto done;
        }

        tc_data *clips = tc_calloc(1, sizeof(*clips));
        if (!clips) {
            st = TC_E_NOMEM;
            goto done;
        }
        tc_status pst = tc_mpls_parse_file(pl->path, clips);
        if (pst == TC_OK) {
            for (size_t k = 0; k < clips->entry_count; k++) {
                pl->duration_ns += clips->entries[k].duration_ns;
            }
        } else if (!first_error) {
            first_error = tc_strdup(tc_last_error());
        }
        tc_data_clear(clips);
    }

    qsort(playlists, playlist_count, sizeof(*playlists), bdmv_playlist_cmp);

    for (size_t i = 0; i < playlist_count; i++) {
        tc_data *clips = tc_calloc(1, sizeof(*clips));
        if (!clips) {
            st = TC_E_NOMEM;
            goto done;
        }
        tc_status pst = tc_mpls_parse_file(playlists[i].path, clips);
        if (pst == TC_OK && clips->entry_count == 0) {
            pst = tc_fail(TC_E_FORMAT, "bdmv: playlist \"%s\" has no play items",
                          playlists[i].file);
        }
        if (pst != TC_OK) {
            /* eac3to had already filtered the playlist list the original
             * worked from, so a file it would not have listed is skipped here
             * too; the failure is only fatal when nothing else parsed. */
            if (!first_error) {
                first_error = tc_strdup(tc_last_error());
            }
            tc_data_clear(clips);
            continue;
        }

        tc_entry *e = tc_data_add_entry(d);
        if (!e) {
            tc_data_clear(clips);
            st = TC_E_NOMEM;
            goto done;
        }
        e->source = tc_strdup(playlists[i].file);
        if (!e->source) {
            tc_data_clear(clips);
            st = TC_E_NOMEM;
            goto done;
        }
        st = bdmv_combine(clips, e);
        tc_data_clear(clips);
        if (st != TC_OK) {
            /* Leave the handle as it was rather than half an entry; the
             * caller frees it either way (see the parser contract). */
            d->entry_count--;
            goto done;
        }
    }

    if (d->entry_count == 0) {
        st = tc_fail(TC_E_FORMAT, "bdmv: no usable playlists under \"%s\"%s%s", path,
                     first_error ? ": " : "", first_error ? first_error : "");
        goto done;
    }

done:
    free(first_error);
    bdmv_name_list_free(&files);
    bdmv_playlists_free(playlists, playlist_count);
    bdmv_path_free(&root);
    bdmv_path_free(&bdmv_dir);
    bdmv_path_free(&playlist_dir);
    bdmv_path_free(&stream_dir);
    return st;
}
