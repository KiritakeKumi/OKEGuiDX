/*
 * FLAC chapter parser (B5).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Ported from TChapter.Parsing.FLACParser + TChapter.Object.FLAC.GetMetadataFromFlac.
 *
 * The reference behaviour is narrower than the container suggests: it walks the
 * metadata blocks only to collect the Vorbis comment fields, then looks up the
 * single key "cuesheet" whose value is an entire CUE sheet as text, and hands
 * that text to the CUE parser. It does NOT read CHAPTERnnn tags, and the
 * CUESHEET metadata block (type 5) is skipped like padding.
 *
 * Behaviour kept from the reference:
 *   - the "fLaC" magic is required,
 *   - block types beyond PICTURE (7..127) are a format error, not skipped,
 *   - Vorbis comment integers are little-endian, everything else big-endian,
 *   - the Vorbis comment fields are read straight from the stream, so only the
 *     file end bounds those reads, not the declared block length,
 *   - a duplicate key overwrites the earlier value (dictionary indexer), which
 *     matters because the last "cuesheet" tag wins,
 *   - a comment without '=' is a format error (the reference's IndexOf/Substring
 *     pair would throw),
 *   - a missing "cuesheet" key is a format error ("No cuesheet found").
 *
 * Deviations, all on malformed input that a real encoder never produces: the
 * reference parses STREAMINFO and PICTURE field by field and can throw on
 * truncated ones, while those blocks are skipped by their declared length here;
 * neither carries information the chapter data uses.
 */
#include <stdlib.h>
#include <string.h>

#include "tc_internal.h"

/* METADATA_BLOCK types (https://xiph.org/flac/format.html). */
enum {
    FLAC_BLOCK_STREAMINFO     = 0,
    FLAC_BLOCK_PADDING        = 1,
    FLAC_BLOCK_APPLICATION    = 2,
    FLAC_BLOCK_SEEKTABLE      = 3,
    FLAC_BLOCK_VORBIS_COMMENT = 4,
    FLAC_BLOCK_CUESHEET       = 5,
    FLAC_BLOCK_PICTURE        = 6
};

/* Cursor over the in-memory file image. Every read is bounds-checked, which is
 * where the reference's ReadBytes throws EndOfStreamException. */
typedef struct flac_cursor {
    const uint8_t *p;
    const uint8_t *end;
} flac_cursor;

/* Returns TC_OK and advances past 4 little-endian bytes, or TC_E_FORMAT. */
static tc_status flac_read_le32(flac_cursor *c, uint32_t *out) {
    if ((size_t)(c->end - c->p) < 4) {
        return tc_fail(TC_E_FORMAT, "flac: truncated vorbis comment");
    }
    *out = tc_le32(c->p);
    c->p += 4;
    return TC_OK;
}

/* Consumes n bytes, or fails when the image ends first. */
static int flac_take(flac_cursor *c, uint32_t n, const uint8_t **out) {
    if ((size_t)(c->end - c->p) < (size_t)n) {
        return 0;
    }
    *out = c->p;
    c->p += n;
    return 1;
}

/* Parses one VORBIS_COMMENT block and captures the "cuesheet" value.
 *
 * *cuesheet receives a malloc'd copy of the value when the key is present; a
 * later occurrence replaces an earlier one, matching the reference's
 * dictionary indexer. The key comparison is exact: the reference dictionary is
 * case-sensitive. */
static tc_status flac_parse_vorbis_comment(flac_cursor *c, char **cuesheet,
                                           size_t *cuesheet_len) {
    uint32_t vendor_len = 0;
    tc_status st = flac_read_le32(c, &vendor_len);
    if (st != TC_OK) {
        return tc_fail(TC_E_FORMAT, "flac: truncated vorbis comment (vendor)");
    }
    const uint8_t *vendor = NULL;
    if (!flac_take(c, vendor_len, &vendor)) {
        return tc_fail(TC_E_FORMAT, "flac: truncated vorbis comment (vendor string)");
    }

    uint32_t count = 0;
    st = flac_read_le32(c, &count);
    if (st != TC_OK) {
        return tc_fail(TC_E_FORMAT, "flac: truncated vorbis comment (count)");
    }

    for (uint32_t i = 0; i < count; i++) {
        uint32_t clen = 0;
        st = flac_read_le32(c, &clen);
        if (st != TC_OK) {
            return tc_fail(TC_E_FORMAT, "flac: truncated vorbis comment (comment %u of %u)",
                           (unsigned)i, (unsigned)count);
        }
        const uint8_t *comment = NULL;
        if (!flac_take(c, clen, &comment)) {
            return tc_fail(TC_E_FORMAT, "flac: truncated vorbis comment (comment %u of %u)",
                           (unsigned)i, (unsigned)count);
        }

        const uint8_t *eq = memchr(comment, '=', clen);
        if (!eq) {
            /* The reference's IndexOf('=') followed by Substring(0, -1) throws
             * for a field without a separator. */
            return tc_fail(TC_E_FORMAT, "flac: vorbis comment field without '='");
        }
        size_t key_len = (size_t)(eq - comment);
        if (key_len == 8 && memcmp(comment, "cuesheet", 8) == 0) {
            size_t value_len = (size_t)clen - key_len - 1;
            char *copy = tc_strndup((const char *)eq + 1, value_len);
            if (!copy) {
                return TC_E_NOMEM;
            }
            free(*cuesheet);
            *cuesheet = copy;
            *cuesheet_len = value_len;
        }
    }
    return TC_OK;
}

tc_status tc_flac_parse_file(const char *path, tc_data *d) {
    if (!path || !d) {
        return tc_fail(TC_E_INVALID, "flac: null argument");
    }

    char *raw = NULL;
    size_t len = 0;
    tc_status st = tc_read_file(path, &raw, &len);
    if (st != TC_OK) {
        return st;
    }
    const uint8_t *base = (const uint8_t *)raw;

    if (len < 4 || memcmp(base, "fLaC", 4) != 0) {
        free(raw);
        return tc_fail(TC_E_FORMAT, "flac: not a FLAC file (missing fLaC magic)");
    }

    char *cuesheet = NULL;
    size_t cuesheet_len = 0;
    flac_cursor c = {base + 4, base + len};

    /* METADATA_BLOCK_HEADER: 1-bit last flag, 7-bit type, 24-bit length. */
    while (c.p < c.end) {
        const uint8_t *header = NULL;
        if (!flac_take(&c, 4, &header)) {
            st = tc_fail(TC_E_FORMAT, "flac: truncated metadata block header");
            goto out;
        }
        const uint32_t header32 = tc_be32(header);
        const int last = (header32 >> 31) != 0;
        const uint32_t type = (header32 >> 24) & 0x7fu;
        const uint32_t blen = header32 & 0xffffffu;

        if (type > FLAC_BLOCK_PICTURE) {
            /* The reference throws ArgumentOutOfRangeException here rather
             * than skipping, so an unknown type fails the parse. */
            st = tc_fail(TC_E_FORMAT, "flac: invalid metadata block type %u",
                         (unsigned)type);
            goto out;
        }

        if (type == FLAC_BLOCK_VORBIS_COMMENT) {
            /* The reference reads these fields from the stream, unconstrained
             * by the block length; only the file end bounds them here too. */
            st = flac_parse_vorbis_comment(&c, &cuesheet, &cuesheet_len);
            if (st != TC_OK) {
                goto out;
            }
        } else {
            /* STREAMINFO, PADDING, APPLICATION, SEEKTABLE, CUESHEET and
             * PICTURE carry nothing this parser needs. The reference seeks
             * past the block, and seeking beyond the end merely ends its loop,
             * so an over-long length is clamped instead of rejected. */
            size_t skip = blen;
            if (skip > (size_t)(c.end - c.p)) {
                skip = (size_t)(c.end - c.p);
            }
            c.p += skip;
        }
        if (last) {
            break;
        }
    }

    if (!cuesheet) {
        st = tc_fail(TC_E_FORMAT, "flac: no cuesheet found in FLAC file");
        goto out;
    }

    /* The reference hands the CUE text to the CUE parser without a file-name
     * hint (it wraps the value in a MemoryStream). */
    st = tc_cue_parse_mem(cuesheet, cuesheet_len, NULL, d);

out:
    free(cuesheet);
    free(raw);
    return st;
}
