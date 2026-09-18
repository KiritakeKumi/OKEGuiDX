/*
 * Core utilities for libtchapter: allocation, errors, buffers, byte readers and
 * text helpers.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#include <ctype.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_internal.h"

/* ------------------------------------------------------------------ */
/* Error reporting                                                    */
/* ------------------------------------------------------------------ */

/* The error message is thread-local: parsing happens on worker threads and a
 * global buffer would be corrupted by concurrent use. */
#if defined(_MSC_VER)
#  define TC_THREAD_LOCAL __declspec(thread)
#else
#  define TC_THREAD_LOCAL _Thread_local
#endif

#define TC_ERR_MAX 1024
static TC_THREAD_LOCAL char g_error[TC_ERR_MAX];

void tc_clear_error(void) { g_error[0] = '\0'; }

void tc_set_error(const char *fmt, ...) {
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(g_error, sizeof(g_error), fmt, ap);
    va_end(ap);
    g_error[sizeof(g_error) - 1] = '\0';
}

tc_status tc_fail(tc_status status, const char *fmt, ...) {
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(g_error, sizeof(g_error), fmt, ap);
    va_end(ap);
    g_error[sizeof(g_error) - 1] = '\0';
    return status;
}

const char *tc_last_error(void) { return g_error; }

/* ------------------------------------------------------------------ */
/* Version and names                                                  */
/* ------------------------------------------------------------------ */

const char *tc_version(void) {
    return "1.0.0";
}

const char *tc_status_name(tc_status status) {
    switch (status) {
    case TC_OK:            return "OK";
    case TC_E_IO:          return "E_IO";
    case TC_E_FORMAT:      return "E_FORMAT";
    case TC_E_RANGE:       return "E_RANGE";
    case TC_E_NOMEM:       return "E_NOMEM";
    case TC_E_INVALID:     return "E_INVALID";
    case TC_E_UNSUPPORTED: return "E_UNSUPPORTED";
    default:               return "E_UNKNOWN";
    }
}

const char *tc_format_extension(tc_format fmt) {
    switch (fmt) {
    case TC_FMT_MPLS:         return ".mpls";
    case TC_FMT_CUE:          return ".cue";
    case TC_FMT_FLAC:         return ".flac";
    case TC_FMT_IFO:          return ".ifo";
    case TC_FMT_MP4:          return ".mp4";
    case TC_FMT_OGM:          return ".txt";
    case TC_FMT_VTT:          return ".vtt";
    case TC_FMT_XML:          return ".xml";
    case TC_FMT_XPL:          return ".xpl";
    case TC_FMT_TAK:          return ".tak";
    case TC_FMT_MATROSKA_XML: return ".xml";
    case TC_FMT_BDMV:         return ".mpls";
    default:                  return "";
    }
}

/* ------------------------------------------------------------------ */
/* Allocation                                                         */
/* ------------------------------------------------------------------ */

void *tc_malloc(size_t n) {
    void *p = malloc(n ? n : 1);
    if (!p) {
        tc_set_error("out of memory (%zu bytes)", n);
    }
    return p;
}

void *tc_calloc(size_t count, size_t size) {
    /* Overflow guard: count * size must fit in size_t. */
    if (size != 0 && count > (size_t)-1 / size) {
        tc_set_error("allocation size overflow (%zu x %zu)", count, size);
        return NULL;
    }
    void *p = calloc(count ? count : 1, size ? size : 1);
    if (!p) {
        tc_set_error("out of memory (%zu x %zu bytes)", count, size);
    }
    return p;
}

void *tc_realloc(void *p, size_t n) {
    void *q = realloc(p, n ? n : 1);
    if (!q) {
        tc_set_error("out of memory (%zu bytes)", n);
    }
    return q;
}

char *tc_strdup(const char *s) {
    if (!s) {
        return NULL;
    }
    size_t n = strlen(s);
    char *p = tc_malloc(n + 1);
    if (p) {
        memcpy(p, s, n + 1);
    }
    return p;
}

char *tc_strndup(const char *s, size_t n) {
    if (!s) {
        return NULL;
    }
    char *p = tc_malloc(n + 1);
    if (p) {
        memcpy(p, s, n);
        p[n] = '\0';
    }
    return p;
}

/* ------------------------------------------------------------------ */
/* Growable buffer                                                    */
/* ------------------------------------------------------------------ */

void tc_buf_init(tc_buf *b) {
    b->data = NULL;
    b->len = 0;
    b->cap = 0;
    b->oom = 0;
}

void tc_buf_free(tc_buf *b) {
    free(b->data);
    tc_buf_init(b);
}

void tc_buf_reset(tc_buf *b) { b->len = 0; }

/* Ensures at least `extra` more bytes fit. Returns 0 on failure. */
static int tc_buf_reserve(tc_buf *b, size_t extra) {
    if (b->oom) {
        return 0;
    }
    if (b->len + extra + 1 <= b->cap) {
        return 1;
    }
    size_t want = b->cap ? b->cap : 256;
    while (want < b->len + extra + 1) {
        /* Grow geometrically but never overflow size_t. */
        if (want > (size_t)-1 / 2) {
            want = b->len + extra + 1;
            break;
        }
        want *= 2;
    }
    char *p = tc_realloc(b->data, want);
    if (!p) {
        b->oom = 1;
        return 0;
    }
    b->data = p;
    b->cap = want;
    return 1;
}

void tc_buf_putc(tc_buf *b, char c) {
    if (!tc_buf_reserve(b, 1)) {
        return;
    }
    b->data[b->len++] = c;
    b->data[b->len] = '\0';
}

void tc_buf_write(tc_buf *b, const char *s, size_t n) {
    if (n == 0 || !tc_buf_reserve(b, n)) {
        return;
    }
    memcpy(b->data + b->len, s, n);
    b->len += n;
    b->data[b->len] = '\0';
}

void tc_buf_puts(tc_buf *b, const char *s) {
    if (s) {
        tc_buf_write(b, s, strlen(s));
    }
}

void tc_buf_printf(tc_buf *b, const char *fmt, ...) {
    char stack[512];
    va_list ap;
    va_start(ap, fmt);
    int n = vsnprintf(stack, sizeof(stack), fmt, ap);
    va_end(ap);
    if (n < 0) {
        b->oom = 1;
        return;
    }
    if ((size_t)n < sizeof(stack)) {
        tc_buf_write(b, stack, (size_t)n);
        return;
    }
    /* The formatted text did not fit on the stack; allocate exactly. */
    char *heap = tc_malloc((size_t)n + 1);
    if (!heap) {
        b->oom = 1;
        return;
    }
    va_start(ap, fmt);
    vsnprintf(heap, (size_t)n + 1, fmt, ap);
    va_end(ap);
    tc_buf_write(b, heap, (size_t)n);
    free(heap);
}

/* The C library's own integer conversions are not portable: MSVCRT (which
 * llvm-mingw and MSVC link against) lacks "%zu" and, on older runtimes, "%lld".
 * Since this library must build on win-x64, win-arm64, linux-x64/arm64/riscv64
 * with whatever libc each target has, integers are formatted here instead of
 * being handed to printf.
 *
 * `min_width` pads with leading zeros; 0 means no padding. */

void tc_buf_put_u64(tc_buf *b, uint64_t v, int min_width) {
    char tmp[24];
    int i = 0;
    if (v == 0) {
        tmp[i++] = '0';
    } else {
        while (v > 0) {
            tmp[i++] = (char)('0' + (v % 10));
            v /= 10;
        }
    }
    while (i < min_width && i < (int)sizeof(tmp)) {
        tmp[i++] = '0';
    }
    /* The digits were collected least-significant first. */
    for (int k = i - 1; k >= 0; k--) {
        tc_buf_putc(b, tmp[k]);
    }
}

void tc_buf_put_i64(tc_buf *b, int64_t v, int min_width) {
    if (v < 0) {
        tc_buf_putc(b, '-');
        /* Negating INT64_MIN overflows, so the magnitude is computed in
         * unsigned arithmetic. */
        uint64_t mag = (uint64_t)0 - (uint64_t)v;
        tc_buf_put_u64(b, mag, min_width > 0 ? min_width - 1 : 0);
        return;
    }
    tc_buf_put_u64(b, (uint64_t)v, min_width);
}

/* Appends `n` as a decimal number with at least `min_width` digits. */
void tc_buf_put_size(tc_buf *b, size_t n, int min_width) {
    tc_buf_put_u64(b, (uint64_t)n, min_width);
}

/* ------------------------------------------------------------------ */
/* Entries and chapters                                               */
/* ------------------------------------------------------------------ */

tc_entry *tc_data_add_entry(tc_data *d) {
    if (d->entry_count == d->entry_cap) {
        size_t cap = d->entry_cap ? d->entry_cap * 2 : 4;
        tc_entry *p = tc_realloc(d->entries, cap * sizeof(*p));
        if (!p) {
            return NULL;
        }
        d->entries = p;
        d->entry_cap = cap;
    }
    tc_entry *e = &d->entries[d->entry_count++];
    memset(e, 0, sizeof(*e));
    e->fps_den = 1;
    return e;
}

tc_status tc_entry_add_chapter(tc_entry *e, char *name, int64_t time_ns, int64_t frames) {
    if (e->chapter_count == e->chapter_cap) {
        size_t cap = e->chapter_cap ? e->chapter_cap * 2 : 16;
        tc_chapter_impl *p = tc_realloc(e->chapters, cap * sizeof(*p));
        if (!p) {
            free(name);
            return TC_E_NOMEM;
        }
        e->chapters = p;
        e->chapter_cap = cap;
    }
    tc_chapter_impl *c = &e->chapters[e->chapter_count++];
    c->name = name;
    c->time_ns = time_ns;
    c->frames = frames;
    return TC_OK;
}

static void tc_entry_clear(tc_entry *e) {
    free(e->title);
    free(e->source);
    for (size_t i = 0; i < e->chapter_count; i++) {
        free(e->chapters[i].name);
    }
    free(e->chapters);
    memset(e, 0, sizeof(*e));
}

void tc_data_clear(tc_data *d) {
    if (!d) {
        return;
    }
    for (size_t i = 0; i < d->entry_count; i++) {
        tc_entry_clear(&d->entries[i]);
    }
    free(d->entries);
    free(d);
}

void tc_free(tc_data *d) { tc_data_clear(d); }

/* ------------------------------------------------------------------ */
/* Byte readers                                                       */
/* ------------------------------------------------------------------ */

uint8_t tc_be8(const uint8_t *p) { return p[0]; }

uint16_t tc_be16(const uint8_t *p) {
    return (uint16_t)((uint16_t)p[0] << 8 | (uint16_t)p[1]);
}

uint32_t tc_be24(const uint8_t *p) {
    return ((uint32_t)p[0] << 16) | ((uint32_t)p[1] << 8) | (uint32_t)p[2];
}

uint32_t tc_be32(const uint8_t *p) {
    return ((uint32_t)p[0] << 24) | ((uint32_t)p[1] << 16) |
           ((uint32_t)p[2] << 8) | (uint32_t)p[3];
}

uint64_t tc_be64(const uint8_t *p) {
    return ((uint64_t)tc_be32(p) << 32) | (uint64_t)tc_be32(p + 4);
}

uint16_t tc_le16(const uint8_t *p) {
    return (uint16_t)((uint16_t)p[1] << 8 | (uint16_t)p[0]);
}

uint32_t tc_le32(const uint8_t *p) {
    return ((uint32_t)p[3] << 24) | ((uint32_t)p[2] << 16) |
           ((uint32_t)p[1] << 8) | (uint32_t)p[0];
}

/* ------------------------------------------------------------------ */
/* Text helpers                                                       */
/* ------------------------------------------------------------------ */

char *tc_trim(char *s) {
    if (!s) {
        return NULL;
    }
    while (*s && isspace((unsigned char)*s)) {
        s++;
    }
    char *end = s + strlen(s);
    while (end > s && isspace((unsigned char)end[-1])) {
        end--;
    }
    *end = '\0';
    return s;
}

void tc_chomp(char *s) {
    if (!s) {
        return;
    }
    size_t n = strlen(s);
    while (n > 0 && (s[n - 1] == '\n' || s[n - 1] == '\r')) {
        s[--n] = '\0';
    }
}

int tc_ieq(const char *a, const char *b) {
    if (!a || !b) {
        return a == b;
    }
    while (*a && *b) {
        if (tolower((unsigned char)*a) != tolower((unsigned char)*b)) {
            return 0;
        }
        a++;
        b++;
    }
    return *a == *b;
}

int tc_istarts_with(const char *s, const char *prefix) {
    if (!s || !prefix) {
        return 0;
    }
    while (*prefix) {
        if (tolower((unsigned char)*s) != tolower((unsigned char)*prefix)) {
            return 0;
        }
        s++;
        prefix++;
    }
    return 1;
}

/* Parses a timestamp of the form HH:MM:SS.mmm or HH:MM:SS,mmm.
 *
 * The fraction may have 3 to 9 digits: Blu-ray playlists use a 45 kHz clock
 * (not a decimal fraction at all, handled elsewhere) while text formats use
 * milliseconds or microseconds. More than 9 digits is rejected rather than
 * silently truncated. */
tc_status tc_parse_timestamp(const char *s, int64_t *out_ns) {
    if (!s || !out_ns) {
        return tc_fail(TC_E_INVALID, "timestamp: null argument");
    }

    const char *p = s;
    while (*p == ' ' || *p == '\t') {
        p++;
    }

    int64_t hours = 0, minutes = 0, seconds = 0;
    int64_t frac_ns = 0;

    /* Hours: read digits until the first colon. */
    if (!isdigit((unsigned char)*p)) {
        return tc_fail(TC_E_FORMAT, "timestamp: expected digits at \"%s\"", s);
    }
    while (isdigit((unsigned char)*p)) {
        hours = hours * 10 + (*p - '0');
        p++;
    }
    if (*p != ':') {
        return tc_fail(TC_E_FORMAT, "timestamp: missing ':' in \"%s\"", s);
    }
    p++;

    if (!isdigit((unsigned char)*p)) {
        return tc_fail(TC_E_FORMAT, "timestamp: expected minutes in \"%s\"", s);
    }
    while (isdigit((unsigned char)*p)) {
        minutes = minutes * 10 + (*p - '0');
        p++;
    }
    if (*p != ':') {
        return tc_fail(TC_E_FORMAT, "timestamp: missing second ':' in \"%s\"", s);
    }
    p++;

    if (!isdigit((unsigned char)*p)) {
        return tc_fail(TC_E_FORMAT, "timestamp: expected seconds in \"%s\"", s);
    }
    while (isdigit((unsigned char)*p)) {
        seconds = seconds * 10 + (*p - '0');
        p++;
    }

    if (*p == '.' || *p == ',') {
        p++;
        int digits = 0;
        while (isdigit((unsigned char)*p)) {
            if (digits < 9) {
                frac_ns = frac_ns * 10 + (*p - '0');
                digits++;
            }
            p++;
        }
        if (digits == 0) {
            return tc_fail(TC_E_FORMAT, "timestamp: missing fraction in \"%s\"", s);
        }
        /* Scale the fraction up to nanoseconds. */
        for (int i = digits; i < 9; i++) {
            frac_ns *= 10;
        }
    }

    /* Trailing whitespace is tolerated; anything else is not. */
    while (*p == ' ' || *p == '\t') {
        p++;
    }
    if (*p != '\0') {
        return tc_fail(TC_E_FORMAT, "timestamp: unexpected trailing text \"%s\"", p);
    }

    if (minutes >= 60 || seconds >= 60) {
        return tc_fail(TC_E_FORMAT, "timestamp: minutes or seconds out of range in \"%s\"", s);
    }

    *out_ns = ((hours * 3600 + minutes * 60 + seconds) * 1000000000LL) + frac_ns;
    return TC_OK;
}

void tc_format_timestamp(int64_t ns, char *buf, size_t buflen) {
    if (!buf || buflen == 0) {
        return;
    }
    int neg = ns < 0;
    if (neg) {
        ns = -ns;
    }
    int64_t total_seconds = ns / 1000000000LL;
    int64_t millis = (ns % 1000000000LL) / 1000000LL;
    int64_t hours = total_seconds / 3600;
    int64_t minutes = (total_seconds % 3600) / 60;
    int64_t seconds = total_seconds % 60;
    /* Assembled by hand: "%02lld" is not portable to MSVCRT. */
    size_t pos = 0;
    if (neg && pos + 1 < buflen) {
        buf[pos++] = (char)0x2D; /* - */
    }
    tc_buf tsbuf;
    tc_buf_init(&tsbuf);
    tc_buf_put_i64(&tsbuf, hours, 2);
    tc_buf_putc(&tsbuf, (char)0x3A);
    tc_buf_put_i64(&tsbuf, minutes, 2);
    tc_buf_putc(&tsbuf, (char)0x3A);
    tc_buf_put_i64(&tsbuf, seconds, 2);
    tc_buf_putc(&tsbuf, (char)0x2E);
    tc_buf_put_i64(&tsbuf, millis, 3);
    if (tsbuf.data) {
        size_t n = tsbuf.len;
        if (pos + n >= buflen) {
            n = buflen > pos + 1 ? buflen - pos - 1 : 0;
        }
        memcpy(buf + pos, tsbuf.data, n);
        pos += n;
    }
    tc_buf_free(&tsbuf);
    buf[pos] = (char)0;
}

/* ------------------------------------------------------------------ */
/* File reading                                                       */
/* ------------------------------------------------------------------ */

tc_status tc_read_file(const char *path, char **out, size_t *len) {
    if (!path || !out) {
        return tc_fail(TC_E_INVALID, "read_file: null argument");
    }
    *out = NULL;
    if (len) {
        *len = 0;
    }

    FILE *f = fopen(path, "rb");
    if (!f) {
        return tc_fail(TC_E_IO, "cannot open \"%s\"", path);
    }

    if (fseek(f, 0, SEEK_END) != 0) {
        fclose(f);
        return tc_fail(TC_E_IO, "cannot seek \"%s\"", path);
    }
    long size = ftell(f);
    if (size < 0) {
        fclose(f);
        return tc_fail(TC_E_IO, "cannot determine the size of \"%s\"", path);
    }
    if (fseek(f, 0, SEEK_SET) != 0) {
        fclose(f);
        return tc_fail(TC_E_IO, "cannot rewind \"%s\"", path);
    }

    char *buf = tc_malloc((size_t)size + 1);
    if (!buf) {
        fclose(f);
        return TC_E_NOMEM;
    }

    size_t got = fread(buf, 1, (size_t)size, f);
    if (got != (size_t)size) {
        free(buf);
        fclose(f);
        return tc_fail(TC_E_IO, "short read on \"%s\" (%zu of %ld bytes)", path, got, size);
    }
    buf[size] = '\0';
    fclose(f);

    *out = buf;
    if (len) {
        *len = (size_t)size;
    }
    return TC_OK;
}
