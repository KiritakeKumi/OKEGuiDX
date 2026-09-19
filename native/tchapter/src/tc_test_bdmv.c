/*
 * BDMV parser tests.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Two kinds of fixture are used:
 *
 *  - the real disc tree that ships with the original test suite
 *    (TChapter.Test/Assets/BDMV/DISC1), referenced in place. Its STREAM
 *    directory holds empty .m2ts files, so the playlists can be resolved but
 *    the media cannot; that is exactly the boundary of this parser.
 *    The expected values were replayed from the C# sources (BDMVParser,
 *    MPLSParser.GetChapters, ChapterUtil.CombineChapter) and are pinned here.
 *  - synthetic discs under build/, built from the MPLS fixtures the MPLS
 *    suite already uses. They cover the ordering, the skipping of an
 *    unreadable playlist, an empty PLAYLIST directory and a lower-case
 *    directory layout.
 *
 * The original BDMVParserTest.cs cannot be used as a reference for assertions:
 * it only runs when eac3to.exe exists at a hardcoded path and asserts nothing.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/types.h>

#include "tc_test.h"

#if defined(_WIN32)
#  include <direct.h>
#  define TEST_MKDIR(p) _mkdir(p)
#  define TEST_RMDIR(p) _rmdir(p)
#else
#  include <unistd.h>
#  define TEST_MKDIR(p) mkdir((p), 0777)
#  define TEST_RMDIR(p) rmdir(p)
#endif

/* The real disc shipped with the original test suite. */
#define BDMV_ASSET_DIR \
    "C:/Users/KiritakeKumi/Documents/GitHub/OKEGuiDX/TChapter/TChapter.Test/Assets/BDMV/DISC1"

/* Scratch tree; it lives under build/ so `make clean` removes it and the test
 * runner's working directory (native/tchapter) always has build/ next to it. */
#define BDMV_TMP_ROOT "build/tc_test_bdmv_tmp"

/* ------------------------------------------------------------------ */
/* Scratch tree helpers                                               */
/* ------------------------------------------------------------------ */

static int path_is_dir(const char *path) {
    struct stat st;
    if (stat(path, &st) != 0) {
        return 0;
    }
    return S_ISDIR(st.st_mode) ? 1 : 0;
}

static int write_file(const char *path, const void *data, size_t len) {
    FILE *f = fopen(path, "wb");
    if (!f) {
        return 0;
    }
    int ok = len == 0 || fwrite(data, 1, len, f) == len;
    if (fclose(f) != 0) {
        ok = 0;
    }
    return ok;
}

/* Creates `path` and every missing parent below the scratch root. */
static int make_dirs(const char *path) {
    char buf[512];
    snprintf(buf, sizeof(buf), "%s", path);
    for (size_t i = 1; i < sizeof(buf) && buf[i] != '\0'; i++) {
        if (buf[i] != '/') {
            continue;
        }
        buf[i] = '\0';
        if (!path_is_dir(buf) && TEST_MKDIR(buf) != 0 && !path_is_dir(buf)) {
            return 0;
        }
        buf[i] = '/';
    }
    if (!path_is_dir(buf) && TEST_MKDIR(buf) != 0 && !path_is_dir(buf)) {
        return 0;
    }
    return 1;
}

/* Copies a testdata file into the scratch tree. Returns 0 when the fixture is
 * missing or the copy failed, which the callers report as a skip. */
static int copy_fixture(const char *fixture, const char *dest) {
    char *buf = NULL;
    size_t len = 0;
    if (tc_test_read_data(fixture, &buf, &len) != TC_OK) {
        return 0;
    }
    int ok = write_file(dest, buf, len);
    free(buf);
    return ok;
}

/* ------------------------------------------------------------------ */
/* Real disc                                                          */
/* ------------------------------------------------------------------ */

/* name, duration in ns, frame rate as a rational, chapter count. The order is
 * longest first with the file name as the tie-breaker; 00002 and 00003 have
 * the same duration. */
typedef struct disc_entry {
    const char *source;
    int64_t duration_ns;
    int num;
    int den;
    size_t chapters;
} disc_entry;

static const disc_entry g_disc[] = {
    {"00002.mpls", 36127244000000LL, 24000, 1001, 166},
    {"00003.mpls", 36127244000000LL, 24000, 1001, 166},
    {"00023.mpls", 9758281000000LL, 30000, 1001, 43},
    {"00021.mpls", 4263050000000LL, 24000, 1001, 21},
    {"00022.mpls", 3620826000000LL, 24000, 1001, 9},
    {"00001.mpls", 1619994000000LL, 24000, 1001, 9},
    {"00004.mpls", 137387000000LL, 24000, 1001, 3},
    {"00005.mpls", 59793000000LL, 30000, 1001, 5},
    {"00000.mpls", 32199000000LL, 30000, 1001, 3},
};
#define DISC_ENTRY_COUNT (sizeof(g_disc) / sizeof(g_disc[0]))

static void check_disc(tc_data *d) {
    TC_CHECK_EQ_INT(tc_entry_count(d), DISC_ENTRY_COUNT);
    for (size_t i = 0; i < DISC_ENTRY_COUNT; i++) {
        tc_entry_info_t ei;
        TC_CHECK_EQ_INT(tc_entry_info(d, i, &ei), TC_OK);
        TC_CHECK_EQ_STR(ei.source, g_disc[i].source);
        TC_CHECK_EQ_INT(ei.duration_ns, g_disc[i].duration_ns);
        TC_CHECK_EQ_INT(ei.fps_num, g_disc[i].num);
        TC_CHECK_EQ_INT(ei.fps_den, g_disc[i].den);
        TC_CHECK_EQ_INT(ei.chapter_count, g_disc[i].chapters);
        /* ChapterUtil.CombineChapter gives the entry this literal title. */
        TC_CHECK_EQ_STR(ei.title, "Full_Chapter");

        /* Names are renumbered over the combined list; the last one proves the
         * two-digit minimum is applied to a three-digit index as well. */
        char last[32];
        snprintf(last, sizeof(last), "Chapter %02u", (unsigned)g_disc[i].chapters);
        tc_chapter_t c;
        TC_CHECK_EQ_INT(tc_chapter_at(d, i, 0, &c), TC_OK);
        TC_CHECK_EQ_STR(c.name, "Chapter 01");
        TC_CHECK_EQ_INT(c.time_ns, 0);
        TC_CHECK_EQ_INT(c.frames, -1);
        TC_CHECK_EQ_INT(tc_chapter_at(d, i, ei.chapter_count - 1, &c), TC_OK);
        TC_CHECK_EQ_STR(c.name, last);
    }
}

static void test_real_disc(void) {
    if (!path_is_dir(BDMV_ASSET_DIR)) {
        fprintf(stderr, "  (skipped: %s not found)\n", BDMV_ASSET_DIR);
        return;
    }

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_file(BDMV_ASSET_DIR, TC_FMT_BDMV, &d), TC_OK);
    if (!d) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }
    check_disc(d);
    tc_free(d);
}

static void test_real_disc_chapters(void) {
    if (!path_is_dir(BDMV_ASSET_DIR)) {
        fprintf(stderr, "  (skipped: %s not found)\n", BDMV_ASSET_DIR);
        return;
    }

    /* These three playlists have no multi-clip play items, so the combined
     * chapter list is the playlist's own list with the play item durations
     * accumulated in front of each one. */
    static const struct {
        const char *source;
        size_t chapters;
        int64_t times[9];
    } want[] = {
        {"00022.mpls", 9,
         {0, 932515000000LL, 2144809000000LL, 2151816000000LL, 2180845000000LL,
          2270810000000LL, 2929802000000LL, 3523854000000LL, 3613818000000LL}},
        {"00001.mpls", 9,
         {0, 59643000000LL, 430013000000LL, 719636000000LL, 809559000000LL,
          899148000000LL, 1219594000000LL, 1529195000000LL, 1618993000000LL}},
        {"00005.mpls", 5,
         {0, 14581000000LL, 29596000000LL, 44244000000LL, 58792000000LL}},
    };

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_file(BDMV_ASSET_DIR, TC_FMT_BDMV, &d), TC_OK);
    if (!d) {
        return;
    }
    for (size_t w = 0; w < sizeof(want) / sizeof(want[0]); w++) {
        size_t index = DISC_ENTRY_COUNT;
        for (size_t i = 0; i < DISC_ENTRY_COUNT; i++) {
            if (strcmp(g_disc[i].source, want[w].source) == 0) {
                index = i;
                break;
            }
        }
        TC_CHECK(index != DISC_ENTRY_COUNT);
        if (index == DISC_ENTRY_COUNT) {
            continue;
        }
        TC_CHECK_EQ_INT(tc_chapter_count(d, index), want[w].chapters);
        for (size_t k = 0; k < want[w].chapters; k++) {
            tc_chapter_t c;
            TC_CHECK_EQ_INT(tc_chapter_at(d, index, k, &c), TC_OK);
            TC_CHECK_EQ_INT(c.time_ns, want[w].times[k]);
            char name[32];
            snprintf(name, sizeof(name), "Chapter %02u", (unsigned)(k + 1));
            TC_CHECK_EQ_STR(c.name, name);
        }
    }
    tc_free(d);
}

static void test_real_disc_last_chapters(void) {
    if (!path_is_dir(BDMV_ASSET_DIR)) {
        fprintf(stderr, "  (skipped: %s not found)\n", BDMV_ASSET_DIR);
        return;
    }

    /* The long playlists carry their chapter marks on the first play item;
     * the last chapter of the combined entry is the last mark of that item. */
    static const struct {
        const char *source;
        size_t chapters;
        int64_t last_ns;
    } want[] = {
        {"00002.mpls", 166, 35909610000000LL},
        {"00023.mpls", 43, 9757280000000LL},
        {"00021.mpls", 21, 4262049000000LL},
    };

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_file(BDMV_ASSET_DIR, TC_FMT_BDMV, &d), TC_OK);
    if (!d) {
        return;
    }
    for (size_t w = 0; w < sizeof(want) / sizeof(want[0]); w++) {
        size_t index = DISC_ENTRY_COUNT;
        for (size_t i = 0; i < DISC_ENTRY_COUNT; i++) {
            if (strcmp(g_disc[i].source, want[w].source) == 0) {
                index = i;
                break;
            }
        }
        if (index == DISC_ENTRY_COUNT) {
            TC_CHECK(0);
            continue;
        }
        TC_CHECK_EQ_INT(tc_chapter_count(d, index), want[w].chapters);
        tc_chapter_t c;
        TC_CHECK_EQ_INT(tc_chapter_at(d, index, want[w].chapters - 1, &c), TC_OK);
        TC_CHECK_EQ_INT(c.time_ns, want[w].last_ns);
    }
    tc_free(d);
}

/* The caller may point at the disc root, the BDMV directory, a directory
 * inside it, or a file; all of them name the same disc. Both separators are
 * accepted, which is what makes a path written on one platform work on the
 * other. */
static void test_path_forms(void) {
    if (!path_is_dir(BDMV_ASSET_DIR)) {
        fprintf(stderr, "  (skipped: %s not found)\n", BDMV_ASSET_DIR);
        return;
    }

    static const char *const forms[] = {
        BDMV_ASSET_DIR,
        BDMV_ASSET_DIR "/",
        BDMV_ASSET_DIR "/BDMV",
        BDMV_ASSET_DIR "/BDMV/",
        BDMV_ASSET_DIR "/BDMV/PLAYLIST",
        BDMV_ASSET_DIR "/BDMV/STREAM",
        BDMV_ASSET_DIR "/BDMV/PLAYLIST/00001.mpls",
        BDMV_ASSET_DIR "/BDMV/STREAM/00003.m2ts",
        BDMV_ASSET_DIR "/BDMV/META", /* META does not exist; still inside BDMV */
    };
    for (size_t i = 0; i < sizeof(forms) / sizeof(forms[0]); i++) {
        tc_data *d = NULL;
        tc_status st = tc_parse_file(forms[i], TC_FMT_BDMV, &d);
        TC_CHECK_EQ_INT(st, TC_OK);
        if (st != TC_OK) {
            fprintf(stderr, "  %s -> %s\n", forms[i], tc_last_error());
            continue;
        }
        TC_CHECK_EQ_INT(tc_entry_count(d), DISC_ENTRY_COUNT);
        tc_entry_info_t ei;
        TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
        TC_CHECK_EQ_STR(ei.source, "00002.mpls");
        tc_free(d);
    }

    /* The forward-slash spelling of a native path, as a caller on the other
     * platform would write it. */
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(
        tc_parse_file(BDMV_ASSET_DIR "/BDMV/STREAM/00003.m2ts", TC_FMT_BDMV, &d),
        TC_OK);
    if (d) {
        TC_CHECK_EQ_INT(tc_entry_count(d), DISC_ENTRY_COUNT);
        tc_free(d);
    }
}

/* ------------------------------------------------------------------ */
/* Scratch discs                                                      */
/* ------------------------------------------------------------------ */

/* Builds the scratch tree. Returns 0 when the MPLS fixtures are unavailable,
 * which the caller reports as a skip. */
static int build_scratch_tree(void) {
    struct {
        const char *dir;
    } dirs[] = {
        {BDMV_TMP_ROOT},
        {BDMV_TMP_ROOT "/disc1/BDMV/PLAYLIST"},
        {BDMV_TMP_ROOT "/disc1/BDMV/STREAM"},
        {BDMV_TMP_ROOT "/disc2/BDMV/PLAYLIST"},
        {BDMV_TMP_ROOT "/disc3/BDMV"},
        {BDMV_TMP_ROOT "/disc4/BDMV/PLAYLIST"},
        {BDMV_TMP_ROOT "/disc5/bdmv/playlist"},
        {BDMV_TMP_ROOT "/disc6/BDMV/PLAYLIST"},
        {BDMV_TMP_ROOT "/disc7/BDMV/PLAYLIST"},
    };
    for (size_t i = 0; i < sizeof(dirs) / sizeof(dirs[0]); i++) {
        if (!make_dirs(dirs[i].dir)) {
            return 0;
        }
    }

    /* disc1: a long and a short playlist, plus one that cannot be read. */
    if (!copy_fixture("mpls-hd.mpls", BDMV_TMP_ROOT "/disc1/BDMV/PLAYLIST/00001.mpls") ||
        !copy_fixture("mpls-empty.mpls", BDMV_TMP_ROOT "/disc1/BDMV/PLAYLIST/00000.mpls") ||
        !write_file(BDMV_TMP_ROOT "/disc1/BDMV/PLAYLIST/zz-bad.mpls", "not an mpls", 11) ||
        !write_file(BDMV_TMP_ROOT "/disc1/BDMV/STREAM/00002.m2ts", "", 0)) {
        return 0;
    }

    /* disc2: a single playlist. */
    if (!copy_fixture("mpls-uhd.mpls", BDMV_TMP_ROOT "/disc2/BDMV/PLAYLIST/00000.mpls")) {
        return 0;
    }

    /* disc5: the same disc with every directory name in lower case. */
    if (!copy_fixture("mpls-uhd.mpls", BDMV_TMP_ROOT "/disc5/bdmv/playlist/00000.mpls")) {
        return 0;
    }

    /* disc6: a directory whose name merely contains ".mpls" and a file whose
     * name is not a playlist at all; both must be ignored. */
    if (!copy_fixture("mpls-uhd.mpls", BDMV_TMP_ROOT "/disc6/BDMV/PLAYLIST/00000.mpls") ||
        !write_file(BDMV_TMP_ROOT "/disc6/BDMV/PLAYLIST/notes.mpls.txt", "x", 1) ||
        !make_dirs(BDMV_TMP_ROOT "/disc6/BDMV/PLAYLIST/dir.mpls")) {
        return 0;
    }

    /* disc7: a playlist directory where nothing can be parsed at all. */
    if (!write_file(BDMV_TMP_ROOT "/disc7/BDMV/PLAYLIST/00000.mpls", "not an mpls", 11)) {
        return 0;
    }
    return 1;
}

static void cleanup_scratch_tree(void) {
    static const char *const files[] = {
        BDMV_TMP_ROOT "/disc1/BDMV/PLAYLIST/00001.mpls",
        BDMV_TMP_ROOT "/disc1/BDMV/PLAYLIST/00000.mpls",
        BDMV_TMP_ROOT "/disc1/BDMV/PLAYLIST/zz-bad.mpls",
        BDMV_TMP_ROOT "/disc1/BDMV/STREAM/00002.m2ts",
        BDMV_TMP_ROOT "/disc2/BDMV/PLAYLIST/00000.mpls",
        BDMV_TMP_ROOT "/disc5/bdmv/playlist/00000.mpls",
        BDMV_TMP_ROOT "/disc6/BDMV/PLAYLIST/00000.mpls",
        BDMV_TMP_ROOT "/disc6/BDMV/PLAYLIST/notes.mpls.txt",
        BDMV_TMP_ROOT "/disc7/BDMV/PLAYLIST/00000.mpls",
    };
    static const char *const dirs[] = {
        BDMV_TMP_ROOT "/disc1/BDMV/PLAYLIST",
        BDMV_TMP_ROOT "/disc1/BDMV/STREAM",
        BDMV_TMP_ROOT "/disc1/BDMV",
        BDMV_TMP_ROOT "/disc1",
        BDMV_TMP_ROOT "/disc2/BDMV/PLAYLIST",
        BDMV_TMP_ROOT "/disc2/BDMV",
        BDMV_TMP_ROOT "/disc2",
        BDMV_TMP_ROOT "/disc3/BDMV",
        BDMV_TMP_ROOT "/disc3",
        BDMV_TMP_ROOT "/disc4/BDMV/PLAYLIST",
        BDMV_TMP_ROOT "/disc4/BDMV",
        BDMV_TMP_ROOT "/disc4",
        BDMV_TMP_ROOT "/disc5/bdmv/playlist",
        BDMV_TMP_ROOT "/disc5/bdmv",
        BDMV_TMP_ROOT "/disc5",
        BDMV_TMP_ROOT "/disc6/BDMV/PLAYLIST/dir.mpls",
        BDMV_TMP_ROOT "/disc6/BDMV/PLAYLIST",
        BDMV_TMP_ROOT "/disc6/BDMV",
        BDMV_TMP_ROOT "/disc6",
        BDMV_TMP_ROOT "/disc7/BDMV/PLAYLIST",
        BDMV_TMP_ROOT "/disc7/BDMV",
        BDMV_TMP_ROOT "/disc7",
        BDMV_TMP_ROOT,
    };
    for (size_t i = 0; i < sizeof(files) / sizeof(files[0]); i++) {
        remove(files[i]);
    }
    for (size_t i = 0; i < sizeof(dirs) / sizeof(dirs[0]); i++) {
        TEST_RMDIR(dirs[i]);
    }
}

/* mpls-hd.mpls holds two play items of 6 chapters each; CombineChapter
 * appends the second item's chapters after the first item's duration. The
 * per-item values are the ones the MPLS suite pins for the same fixture. */
static void test_combine_across_play_items(void) {
    if (!path_is_dir(BDMV_TMP_ROOT "/disc1")) {
        fprintf(stderr, "  (skipped: scratch tree not built)\n");
        return;
    }

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_file(BDMV_TMP_ROOT "/disc1", TC_FMT_BDMV, &d), TC_OK);
    if (!d) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }

    /* The unreadable playlist is skipped, not fatal; the other two are ordered
     * longest first. */
    TC_CHECK_EQ_INT(tc_entry_count(d), 2);
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.source, "00001.mpls");
    TC_CHECK_EQ_INT(ei.duration_ns, 2844341000000LL); /* 1422087 + 1422254 ms */
    TC_CHECK_EQ_INT(ei.chapter_count, 12);
    TC_CHECK_EQ_INT(ei.fps_num, 24000);
    TC_CHECK_EQ_INT(ei.fps_den, 1001);

    static const int64_t chapters[] = {
        0,
        74992000000LL,
        165040000000LL,
        747038000000LL,
        1316023000000LL,
        1406030000000LL,
        /* second play item, shifted by the first item's duration */
        1423088000000LL,
        1484066000000LL,
        1574114000000LL,
        2113111000000LL,
        2738068000000LL,
        2828075000000LL,
    };
    for (size_t k = 0; k < sizeof(chapters) / sizeof(chapters[0]); k++) {
        tc_chapter_t c;
        TC_CHECK_EQ_INT(tc_chapter_at(d, 0, k, &c), TC_OK);
        TC_CHECK_EQ_INT(c.time_ns, chapters[k]);
        char name[32];
        snprintf(name, sizeof(name), "Chapter %02u", (unsigned)(k + 1));
        TC_CHECK_EQ_STR(c.name, name);
    }

    /* The 12 play items of the shorter playlist, each with its own duration;
     * every item after the first has no chapter marks, so the placeholder
     * chapter lands at the running offset and is renumbered on the way out. */
    static const int64_t item_durations[] = {
        1001000000LL,  23398000000LL, 25234000000LL, 19686000000LL,
        24983000000LL, 26860000000LL, 25901000000LL, 19686000000LL,
        27986000000LL, 22064000000LL, 27402000000LL, 28862000000LL,
    };
    TC_CHECK_EQ_INT(tc_entry_info(d, 1, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.source, "00000.mpls");
    TC_CHECK_EQ_INT(ei.chapter_count, 12);
    TC_CHECK_EQ_INT(ei.fps_num, 24000);
    TC_CHECK_EQ_INT(ei.fps_den, 1001);
    int64_t expected_duration = 0;
    for (size_t k = 0; k < 12; k++) {
        expected_duration += item_durations[k];
    }
    TC_CHECK_EQ_INT(ei.duration_ns, expected_duration);
    int64_t offset = 0;
    for (size_t k = 0; k < 12; k++) {
        tc_chapter_t c;
        TC_CHECK_EQ_INT(tc_chapter_at(d, 1, k, &c), TC_OK);
        TC_CHECK_EQ_INT(c.time_ns, offset);
        offset += item_durations[k];
    }

    tc_free(d);
}

static void test_single_playlist_disc(void) {
    if (!path_is_dir(BDMV_TMP_ROOT "/disc2")) {
        fprintf(stderr, "  (skipped: scratch tree not built)\n");
        return;
    }

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_file(BDMV_TMP_ROOT "/disc2/BDMV", TC_FMT_BDMV, &d), TC_OK);
    if (!d) {
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.source, "00000.mpls");
    TC_CHECK_EQ_INT(ei.duration_ns, 6294288000000LL);
    TC_CHECK_EQ_INT(ei.chapter_count, 16);
    TC_CHECK_EQ_INT(ei.fps_num, 24000);
    TC_CHECK_EQ_INT(ei.fps_den, 1001);
    TC_CHECK_EQ_STR(ei.title, "Full_Chapter");
    tc_free(d);
}

static void test_lowercase_directories(void) {
    if (!path_is_dir(BDMV_TMP_ROOT "/disc5")) {
        fprintf(stderr, "  (skipped: scratch tree not built)\n");
        return;
    }

    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_file(BDMV_TMP_ROOT "/disc5", TC_FMT_BDMV, &d), TC_OK);
    if (!d) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_INT(ei.chapter_count, 16);
    tc_free(d);
}

static void test_odd_file_names(void) {
    if (!path_is_dir(BDMV_TMP_ROOT "/disc6")) {
        fprintf(stderr, "  (skipped: scratch tree not built)\n");
        return;
    }

    /* Only regular files ending in ".mpls" are playlists. The directory named
     * "dir.mpls" has the suffix too and must not be opened as one, and
     * "notes.mpls.txt" must not be picked up either. */
    tc_data *d = NULL;
    TC_CHECK_EQ_INT(tc_parse_file(BDMV_TMP_ROOT "/disc6", TC_FMT_BDMV, &d), TC_OK);
    if (!d) {
        fprintf(stderr, "  parse error: %s\n", tc_last_error());
        return;
    }
    TC_CHECK_EQ_INT(tc_entry_count(d), 1);
    tc_entry_info_t ei;
    TC_CHECK_EQ_INT(tc_entry_info(d, 0, &ei), TC_OK);
    TC_CHECK_EQ_STR(ei.source, "00000.mpls");
    TC_CHECK_EQ_INT(ei.chapter_count, 16);
    tc_free(d);
}

/* ------------------------------------------------------------------ */
/* Error paths                                                        */
/* ------------------------------------------------------------------ */

static void test_missing_structure(void) {
    tc_data *d = NULL;

    /* A path that does not exist at all. */
    TC_CHECK_EQ_INT(tc_parse_file(BDMV_TMP_ROOT "/nope", TC_FMT_BDMV, &d), TC_E_IO);
    TC_CHECK(d == NULL);
    TC_CHECK_CONTAINS(tc_last_error(), "structure not found");

    /* A directory that exists but has no BDMV below it. */
    if (path_is_dir(BDMV_TMP_ROOT)) {
        TC_CHECK_EQ_INT(tc_parse_file(BDMV_TMP_ROOT, TC_FMT_BDMV, &d), TC_E_IO);
        TC_CHECK(d == NULL);
        TC_CHECK_CONTAINS(tc_last_error(), "structure not found");
    }

    /* BDMV without PLAYLIST. */
    if (path_is_dir(BDMV_TMP_ROOT "/disc3")) {
        TC_CHECK_EQ_INT(tc_parse_file(BDMV_TMP_ROOT "/disc3", TC_FMT_BDMV, &d), TC_E_IO);
        TC_CHECK(d == NULL);
        TC_CHECK_CONTAINS(tc_last_error(), "structure not found");
    }

    /* A PLAYLIST directory with no playlists in it. */
    if (path_is_dir(BDMV_TMP_ROOT "/disc4")) {
        TC_CHECK_EQ_INT(tc_parse_file(BDMV_TMP_ROOT "/disc4", TC_FMT_BDMV, &d), TC_E_IO);
        TC_CHECK(d == NULL);
        TC_CHECK_CONTAINS(tc_last_error(), "no playlists");
    }

    /* Playlists exist but none of them can be parsed. The failure is only
     * raised once nothing is left, and it carries the first reason. */
    if (path_is_dir(BDMV_TMP_ROOT "/disc7")) {
        TC_CHECK_EQ_INT(tc_parse_file(BDMV_TMP_ROOT "/disc7", TC_FMT_BDMV, &d),
                        TC_E_FORMAT);
        TC_CHECK(d == NULL);
        TC_CHECK_CONTAINS(tc_last_error(), "no usable playlists");
    }

    /* Null arguments are rejected before anything is touched. */
    TC_CHECK_EQ_INT(tc_parse_file(NULL, TC_FMT_BDMV, &d), TC_E_INVALID);
    TC_CHECK(d == NULL);
}

/* ------------------------------------------------------------------ */
/* Suite                                                              */
/* ------------------------------------------------------------------ */

TC_SUITE(bdmv) {
    int scratch = build_scratch_tree();
    if (!scratch) {
        fprintf(stderr, "  (scratch tree unavailable: MPLS fixtures missing?)\n");
    }

    TC_CASE("real_disc");           test_real_disc();
    TC_CASE("real_disc_chapters");  test_real_disc_chapters();
    TC_CASE("real_disc_last");      test_real_disc_last_chapters();
    TC_CASE("path_forms");          test_path_forms();
    TC_CASE("combine_play_items");  test_combine_across_play_items();
    TC_CASE("single_playlist");     test_single_playlist_disc();
    TC_CASE("lowercase_dirs");      test_lowercase_directories();
    TC_CASE("odd_file_names");      test_odd_file_names();
    TC_CASE("missing_structure");   test_missing_structure();

    cleanup_scratch_tree();
}
