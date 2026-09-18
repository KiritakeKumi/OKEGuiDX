/*
 * Internal shared declarations for libtchapter.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TC_INTERNAL_H
#define TC_INTERNAL_H

#include <stddef.h>
#include <stdint.h>

#include "tchapter.h"

/* ------------------------------------------------------------------ */
/* Allocation                                                         */
/* ------------------------------------------------------------------ */

/* Allocation wrappers that set the thread-local error message on failure, so
 * every call site does not have to. */
void *tc_malloc(size_t n);
void *tc_calloc(size_t count, size_t size);
void *tc_realloc(void *p, size_t n);
char *tc_strdup(const char *s);
char *tc_strndup(const char *s, size_t n);

/* ------------------------------------------------------------------ */
/* Error reporting                                                    */
/* ------------------------------------------------------------------ */

/* Sets the thread-local error message. */
void tc_set_error(const char *fmt, ...);
/* Sets the message and returns the status, for `return tc_fail(...)` idioms. */
tc_status tc_fail(tc_status status, const char *fmt, ...);
/* Clears the message; called at the start of every public entry point. */
void tc_clear_error(void);

/* ------------------------------------------------------------------ */
/* Buffers                                                            */
/* ------------------------------------------------------------------ */

/* A growable byte buffer used by every writer. */
typedef struct tc_buf {
    char *data;
    size_t len;
    size_t cap;
    int oom; /* sticky allocation failure flag */
} tc_buf;

void tc_buf_init(tc_buf *b);
void tc_buf_free(tc_buf *b);
void tc_buf_reset(tc_buf *b);
void tc_buf_putc(tc_buf *b, char c);
void tc_buf_write(tc_buf *b, const char *s, size_t n);
void tc_buf_puts(tc_buf *b, const char *s);
/* printf-style append. Only portable conversions should be used; for 64-bit and
 * size_t values prefer tc_buf_put_u64 / tc_buf_put_i64 / tc_buf_put_size, which
 * do not depend on the target libc. */
void tc_buf_printf(tc_buf *b, const char *fmt, ...);
/* Appends an unsigned decimal integer, zero-padded to min_width digits. */
void tc_buf_put_u64(tc_buf *b, uint64_t v, int min_width);
/* Appends a signed decimal integer. */
void tc_buf_put_i64(tc_buf *b, int64_t v, int min_width);
/* Appends a size_t as a decimal integer. */
void tc_buf_put_size(tc_buf *b, size_t n, int min_width);

/* ------------------------------------------------------------------ */
/* Chapters                                                           */
/* ------------------------------------------------------------------ */

typedef struct tc_chapter_impl {
    char *name;
    int64_t time_ns;
    int64_t frames;
} tc_chapter_impl;

typedef struct tc_entry {
    char *title;
    char *source;
    int64_t fps_num;
    int64_t fps_den;
    int64_t duration_ns;
    tc_chapter_impl *chapters;
    size_t chapter_count;
    size_t chapter_cap;
} tc_entry;

struct tc_data {
    tc_entry *entries;
    size_t entry_count;
    size_t entry_cap;
};

/* Appends a chapter, taking ownership of `name`. */
tc_status tc_entry_add_chapter(tc_entry *e, char *name, int64_t time_ns, int64_t frames);
/* Appends an entry and returns a pointer to it, or NULL on failure. */
tc_entry *tc_data_add_entry(tc_data *d);
/* Releases everything owned by a handle. */
void tc_data_clear(tc_data *d);

/* ------------------------------------------------------------------ */
/* Format detection                                                   */
/* ------------------------------------------------------------------ */

/* Detects the format from a file's leading bytes plus its name. */
tc_format tc_detect_from_file(const char *path);

/* ------------------------------------------------------------------ */
/* Parsers                                                            */
/* ------------------------------------------------------------------ */

/* Every parser fills a freshly allocated entry appended to `d`.
 *
 * Contract: on success return TC_OK with at least one entry added. On failure
 * return a status and set the error message; the caller frees `d` either way.
 */
typedef struct tc_parser {
    tc_format format;
    const char *name;
    tc_status (*parse_file)(const char *path, tc_data *d);
    tc_status (*parse_mem)(const void *buf, size_t len, const char *name_hint, tc_data *d);
} tc_parser;

/* The parser table, terminated by an entry with format TC_FMT_AUTO. */
const tc_parser *tc_parsers(void);

/* Looks up a parser for a concrete format; NULL when there is none. */
const tc_parser *tc_parser_for(tc_format fmt);

/* ------------------------------------------------------------------ */
/* Shared helpers for parsers                                         */
/* ------------------------------------------------------------------ */

/* Reads an entire file into a NUL-terminated buffer. The caller frees it with
 * free(). Returns TC_OK and sets *out and *len, or an error status. */
tc_status tc_read_file(const char *path, char **out, size_t *len);

/* --- big-endian readers, used by the binary parsers --- */
uint8_t tc_be8(const uint8_t *p);
uint16_t tc_be16(const uint8_t *p);
uint32_t tc_be24(const uint8_t *p);
uint32_t tc_be32(const uint8_t *p);
uint64_t tc_be64(const uint8_t *p);
/* --- little-endian readers --- */
uint16_t tc_le16(const uint8_t *p);
uint32_t tc_le32(const uint8_t *p);

/* --- text helpers --- */

/* Trims leading and trailing ASCII whitespace in place and returns the start. */
char *tc_trim(char *s);
/* Trims trailing CR/LF in place. */
void tc_chomp(char *s);
/* Case-insensitive comparison of a NUL-terminated string against a literal. */
int tc_ieq(const char *a, const char *b);
/* Case-insensitive prefix test. */
int tc_istarts_with(const char *s, const char *prefix);

/* Parses "HH:MM:SS.mmm" or "HH:MM:SS,mmm" (with 3 to 9 fractional digits) into
 * nanoseconds. Returns TC_OK or TC_E_FORMAT. */
tc_status tc_parse_timestamp(const char *s, int64_t *out_ns);

/* Formats nanoseconds as "HH:MM:SS.mmm". `buf` must hold at least 13 bytes. */
void tc_format_timestamp(int64_t ns, char *buf, size_t buflen);

/* ------------------------------------------------------------------ */
/* Writers                                                            */
/* ------------------------------------------------------------------ */

typedef tc_status (*tc_writer_fn)(const tc_entry *e, const char *language,
                                  const char *source_name, tc_buf *out);

/* Returns the writer for a text format, or NULL when the format is not
 * writable. */
tc_writer_fn tc_writer_for(tc_format fmt);

/* ------------------------------------------------------------------ */
/* Mini XML parser (B1)                                               */
/* ------------------------------------------------------------------ */

/* The Matroska, XML and XPL parsers all need to read a small subset of XML.
 * Pulling in libxml2 would add a dependency to every target for the sake of a
 * few hundred lines, so a purpose-built reader lives here instead.
 *
 * Supported: elements, attributes, text, self-closing tags, comments, the XML
 * declaration, CDATA and the five predefined entities. Not supported (and not
 * needed by any chapter format): DTDs, namespaces beyond a literal prefix,
 * processing instructions other than the declaration.
 */

typedef enum tc_xml_kind {
    TC_XML_ELEMENT = 0,
    TC_XML_TEXT
} tc_xml_kind;

typedef struct tc_xml_attr {
    char *name;
    char *value;
} tc_xml_attr_t;

typedef struct tc_xml_node {
    tc_xml_kind kind;
    /* Element name, or NULL for text nodes. */
    char *name;
    /* Decoded text for text nodes, or NULL for elements. */
    char *text;
    tc_xml_attr_t *attrs;
    size_t attr_count;
    struct tc_xml_node *children;
    size_t child_count;
    size_t child_cap;
} tc_xml_node;

/* Parses an XML document. The caller frees the result with tc_xml_free. */
tc_status tc_xml_parse(const char *text, size_t len, tc_xml_node **out);
void tc_xml_free(tc_xml_node *root);

/* Finds the first child element with the given name; NULL when absent. */
const tc_xml_node *tc_xml_child(const tc_xml_node *parent, const char *name);
/* Finds the first direct child element whose name matches, ignoring case. */
const tc_xml_node *tc_xml_child_ci(const tc_xml_node *parent, const char *name);
/* Returns an attribute value, or NULL. */
const char *tc_xml_attr(const tc_xml_node *node, const char *name);
/* Returns an attribute value, or `fallback` when absent. */
const char *tc_xml_attr_or(const tc_xml_node *node, const char *name, const char *fallback);
/* Concatenates the text of all direct text children into `out`. */
void tc_xml_text(const tc_xml_node *node, tc_buf *out);

#endif /* TC_INTERNAL_H */
