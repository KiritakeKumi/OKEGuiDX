/*
 * TAK chapter parser (B6).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Ported from TChapter.Parsing.TAKParser.
 *
 * The reference does NOT parse TAK's APE tag structure. It reads the last
 * 20 KiB of the file, scans them for the literal text "cuesheet" with a small
 * case-insensitive state machine, and treats what follows as an embedded CUE
 * sheet:
 *
 *   - the "tBaK" magic is required,
 *   - only the last min(20480, file size) bytes are scanned, so a key before
 *     that window is invisible,
 *   - the match is not anchored to a tag boundary and is case-insensitive:
 *     the APE item key "Cuesheet" and the word "cuesheet" anywhere in the
 *     scanned bytes both match,
 *   - the sheet starts two bytes past the last character of the matched word:
 *     `stream.Position` is already one past it and the reference adds one more
 *     (`beginPos = stream.Position + 1`). In a real TAK file that skipped byte
 *     is the NUL between the APE item key and its value,
 *   - the sheet ends at the first run of six consecutive NUL bytes; the copied
 *     range includes the first NUL of the run. When the two bytes at endPos-3
 *     and endPos-2 are CR LF, the reference decrements endPos once, which
 *     drops that first NUL and ends the copy on the byte before the run (in
 *     the test fixture, the 0x19 padding byte that follows the final CR LF),
 *   - when no "cuesheet" is found, the scan for the terminator still starts at
 *     the beginning of the window and whatever lies between the window start
 *     and the first six-NUL run is handed to the CUE parser. This port keeps
 *     that fallback because it is what the reference does; for a file shorter
 *     than the window the guard `beginPos == 0` returns an empty stream, and
 *     for a longer one the CUE parser is fed binary data.
 *
 * The extracted bytes are handed to the CUE parser as a memory stream. The
 * reference wraps them in a MemoryStream that StreamReader reads with byte
 * order mark detection, so the CUE parser owns all decoding; this port does
 * the same.
 *
 * Deviations, all on malformed input that a real encoder never produces:
 *
 *   - when the terminator is not found the reference leaves endPos at 0 and
 *     the `endPos <= 1` guard produces an empty stream, which the CUE parser
 *     reports as "Empty cue file". This port delegates the empty stream in the
 *     same way, so the error comes from the CUE parser.
 *   - the reference seeks to endPos - 3 for the CR LF test without checking
 *     that endPos is at least 3; a terminator at offset 2 makes it seek to -1
 *     and throw an IOException. This port skips the test in that case.
 *   - the reference reads the magic with Stream.Read, which may return fewer
 *     than four bytes; a short file then reports "Except an tak but get an
 *     ..." with whatever the array held. This port rejects anything shorter
 *     than four bytes outright.
 */
#include <stdlib.h>
#include <string.h>

#include "tc_internal.h"

/* The reference scans at most this many bytes from the end of the file. */
#define TAK_SCAN_WINDOW 20480

/* The terminator is six consecutive NUL bytes for TAK (the dead FLAC branch in
 * the reference mentions three). */
#define TAK_CONTROL_COUNT 6

/* Matches the eight characters "cuesheet" case-insensitively using the
 * reference's own state machine: the states are the number of matched
 * characters and any mismatch resets to 0. On success *match_end receives the
 * index one past the final 't'. */
static int tak_find_cuesheet(const uint8_t *s, size_t len, size_t *match_end) {
    int state = 0;
    for (size_t i = 0; i < len; i++) {
        char c = (char)s[i];
        if (c >= 'A' && c <= 'Z') {
            c = (char)(c - 'A' + 'a');
        }
        switch (c) {
        case 'c':
            state = 1;
            break;
        case 'u':
            state = state == 1 ? 2 : 0;
            break;
        case 'e':
            switch (state) {
            case 2: state = 3; break;
            case 5: state = 6; break;
            case 6: state = 7; break;
            default: state = 0; break;
            }
            break;
        case 's':
            state = state == 3 ? 4 : 0;
            break;
        case 'h':
            state = state == 4 ? 5 : 0;
            break;
        case 't':
            state = state == 7 ? 8 : 0;
            break;
        default:
            state = 0;
            break;
        }
        if (state == 8) {
            *match_end = i + 1;
            return 1;
        }
    }
    return 0;
}

/* Finds the first run of TAK_CONTROL_COUNT NUL bytes at or after `start` and
 * returns its first index, or `len` when there is none. */
static size_t tak_find_terminator(const uint8_t *s, size_t len, size_t start) {
    size_t run = 0;
    for (size_t i = start; i < len; i++) {
        if (s[i] == 0) {
            run++;
            if (run == TAK_CONTROL_COUNT) {
                return i + 1 - TAK_CONTROL_COUNT;
            }
        } else {
            run = 0;
        }
    }
    return len;
}

tc_status tc_tak_parse_file(const char *path, tc_data *d) {
    if (!path || !d) {
        return tc_fail(TC_E_INVALID, "tak: null argument");
    }

    char *raw = NULL;
    size_t len = 0;
    tc_status st = tc_read_file(path, &raw, &len);
    if (st != TC_OK) {
        return st;
    }
    const uint8_t *base = (const uint8_t *)raw;

    if (len < 4 || memcmp(base, "tBaK", 4) != 0) {
        free(raw);
        return tc_fail(TC_E_FORMAT, "tak: not a TAK file (missing tBaK magic)");
    }

    /* stream.Seek(Math.Max(-20480, -stream.Length), SeekOrigin.End): the window
     * is the last 20480 bytes, or the whole file when it is shorter. */
    size_t scan_start = 0;
    if (len > TAK_SCAN_WINDOW) {
        scan_start = len - TAK_SCAN_WINDOW;
    }

    /* Without a match, beginPos keeps the position the reference's scan
     * started at; there is no error path for a missing key. */
    size_t begin = scan_start;
    size_t match_end = 0;
    if (tak_find_cuesheet(base + scan_start, len - scan_start, &match_end)) {
        /* beginPos = stream.Position + 1: one past the match, plus the byte
         * the reference always skips. */
        begin = scan_start + match_end + 1;
    }

    /* endPos stays 0 in the reference when no run is found; the `endPos <= 1`
     * guard then yields Stream.Null. Seeking past the end has the same
     * effect. */
    size_t end = 0;
    if (begin < len) {
        size_t run = tak_find_terminator(base, len, begin);
        if (run < len) {
            end = run;
        }
    }

    if (begin == 0 || end <= 1) {
        /* The reference returns Stream.Null here and the CUE parser reports
         * the empty sheet; delegating keeps the error identical. */
        st = tc_cue_parse_mem(base, 0, NULL, d);
        free(raw);
        return st;
    }

    /* The reference seeks to endPos - 3 without a lower bound; endPos < 3
     * would seek before the stream start and throw. A matched key puts end at
     * 13 or higher, but the no-match fallback can leave end at 1 or 2, so the
     * lower bound is checked here. */
    if (end >= 3 && base[end - 3] == 0x0D && base[end - 2] == 0x0A) {
        end--;
    }

    st = tc_cue_parse_mem(base + begin, end - begin + 1, NULL, d);
    free(raw);
    return st;
}
