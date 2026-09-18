/*
 * A minimal XML reader for the chapter formats that need one.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Scope: enough XML to read Matroska chapter XML, the XML chapter format and
 * XPL playlists. Deliberately not a general parser -- no DTD, no namespace
 * resolution, no entity definitions beyond the five predefined ones. Adding
 * libxml2 for this would put a dependency on every target including riscv64,
 * which is exactly what this library exists to avoid.
 */
#include <ctype.h>
#include <stdlib.h>
#include <string.h>

#include "tc_internal.h"

typedef struct xml_parser {
    const char *p;
    const char *end;
} xml_parser;

/* ------------------------------------------------------------------ */
/* Node construction                                                  */
/* ------------------------------------------------------------------ */

static tc_xml_node *xml_new_node(tc_xml_kind kind) {
    tc_xml_node *n = tc_calloc(1, sizeof(*n));
    if (n) {
        n->kind = kind;
    }
    return n;
}

static int xml_add_child(tc_xml_node *parent, tc_xml_node *child) {
    if (parent->child_count == parent->child_cap) {
        size_t cap = parent->child_cap ? parent->child_cap * 2 : 8;
        tc_xml_node *p = tc_realloc(parent->children, cap * sizeof(*p));
        if (!p) {
            return 0;
        }
        parent->children = p;
        parent->child_cap = cap;
    }
    parent->children[parent->child_count++] = *child;
    /* Ownership of the contents moved into the array; the temporary node
     * itself is freed by the caller. */
    free(child);
    return 1;
}

static int xml_add_attr(tc_xml_node *node, char *name, char *value) {
    tc_xml_attr_t *p = tc_realloc(node->attrs, (node->attr_count + 1) * sizeof(*p));
    if (!p) {
        free(name);
        free(value);
        return 0;
    }
    node->attrs = p;
    node->attrs[node->attr_count].name = name;
    node->attrs[node->attr_count].value = value;
    node->attr_count++;
    return 1;
}

/* Frees everything a node owns but not the node itself. Child nodes live inside
 * their parent's array, so they must never be passed to free() individually. */
static void xml_free_contents(tc_xml_node *n) {
    for (size_t i = 0; i < n->attr_count; i++) {
        free(n->attrs[i].name);
        free(n->attrs[i].value);
    }
    free(n->attrs);
    for (size_t i = 0; i < n->child_count; i++) {
        xml_free_contents(&n->children[i]);
    }
    free(n->children);
    free(n->name);
    free(n->text);
    n->attrs = NULL;
    n->children = NULL;
    n->name = NULL;
    n->text = NULL;
    n->attr_count = n->child_count = 0;
}

void tc_xml_free(tc_xml_node *root) {
    if (!root) {
        return;
    }
    xml_free_contents(root);
    free(root);
}

/* ------------------------------------------------------------------ */
/* Character handling                                                 */
/* ------------------------------------------------------------------ */

static int xml_skip_space(xml_parser *x) {
    int skipped = 0;
    while (x->p < x->end && isspace((unsigned char)*x->p)) {
        x->p++;
        skipped = 1;
    }
    return skipped;
}

static void xml_skip_comment(xml_parser *x) {
    /* Called with x->p just past "<!--". */
    while (x->p + 2 < x->end) {
        if (x->p[0] == '-' && x->p[1] == '-' && x->p[2] == '>') {
            x->p += 3;
            return;
        }
        x->p++;
    }
    x->p = x->end;
}

static void xml_skip_pi(xml_parser *x) {
    /* Called with x->p just past "<?". */
    while (x->p + 1 < x->end) {
        if (x->p[0] == '?' && x->p[1] == '>') {
            x->p += 2;
            return;
        }
        x->p++;
    }
    x->p = x->end;
}

/* Skips <!DOCTYPE ...> including a bracketed internal subset. */
static void xml_skip_doctype(xml_parser *x) {
    int depth = 0;
    while (x->p < x->end) {
        if (*x->p == '[') {
            depth++;
        } else if (*x->p == ']') {
            depth--;
        } else if (*x->p == '>' && depth <= 0) {
            x->p++;
            return;
        }
        x->p++;
    }
}

/* ------------------------------------------------------------------ */
/* Text decoding                                                      */
/* ------------------------------------------------------------------ */

/* Appends one character, expanding the predefined entities and numeric
 * character references. Unknown entities are copied verbatim: chapter names
 * sometimes contain a bare '&' and dropping the text would be worse. */
static void xml_decode_into(tc_buf *b, const char *s, size_t n) {
    for (size_t i = 0; i < n; i++) {
        if (s[i] != '&') {
            tc_buf_putc(b, s[i]);
            continue;
        }
        size_t semi = i + 1;
        while (semi < n && semi - i <= 10 && s[semi] != ';') {
            semi++;
        }
        if (semi >= n || s[semi] != ';') {
            tc_buf_putc(b, '&');
            continue;
        }
        size_t entlen = semi - i - 1;
        const char *ent = s + i + 1;
        if (entlen == 3 && strncmp(ent, "amp", 3) == 0) {
            tc_buf_putc(b, '&');
        } else if (entlen == 2 && strncmp(ent, "lt", 2) == 0) {
            tc_buf_putc(b, '<');
        } else if (entlen == 2 && strncmp(ent, "gt", 2) == 0) {
            tc_buf_putc(b, '>');
        } else if (entlen == 4 && strncmp(ent, "quot", 4) == 0) {
            tc_buf_putc(b, '"');
        } else if (entlen == 4 && strncmp(ent, "apos", 4) == 0) {
            tc_buf_putc(b, '\'');
        } else if (entlen >= 2 && ent[0] == '#') {
            long code = 0;
            int ok = 1;
            if (ent[1] == 'x' || ent[1] == 'X') {
                for (size_t k = 2; k < entlen; k++) {
                    char c = ent[k];
                    if (c >= '0' && c <= '9') {
                        code = code * 16 + (c - '0');
                    } else if (c >= 'a' && c <= 'f') {
                        code = code * 16 + (c - 'a' + 10);
                    } else if (c >= 'A' && c <= 'F') {
                        code = code * 16 + (c - 'A' + 10);
                    } else {
                        ok = 0;
                        break;
                    }
                }
            } else {
                for (size_t k = 1; k < entlen; k++) {
                    if (ent[k] < '0' || ent[k] > '9') {
                        ok = 0;
                        break;
                    }
                    code = code * 10 + (ent[k] - '0');
                }
            }
            if (ok && code > 0 && code < 0x80) {
                tc_buf_putc(b, (char)code);
            } else if (ok && code >= 0x80 && code <= 0x10FFFF) {
                /* UTF-8 encode. */
                if (code < 0x800) {
                    tc_buf_putc(b, (char)(0xC0 | (code >> 6)));
                    tc_buf_putc(b, (char)(0x80 | (code & 0x3F)));
                } else if (code < 0x10000) {
                    tc_buf_putc(b, (char)(0xE0 | (code >> 12)));
                    tc_buf_putc(b, (char)(0x80 | ((code >> 6) & 0x3F)));
                    tc_buf_putc(b, (char)(0x80 | (code & 0x3F)));
                } else {
                    tc_buf_putc(b, (char)(0xF0 | (code >> 18)));
                    tc_buf_putc(b, (char)(0x80 | ((code >> 12) & 0x3F)));
                    tc_buf_putc(b, (char)(0x80 | ((code >> 6) & 0x3F)));
                    tc_buf_putc(b, (char)(0x80 | (code & 0x3F)));
                }
            } else {
                /* Out of range or malformed: keep it verbatim. */
                tc_buf_write(b, s + i, semi - i + 1);
            }
        } else {
            tc_buf_write(b, s + i, semi - i + 1);
        }
        i = semi;
    }
}

/* ------------------------------------------------------------------ */
/* Names                                                              */
/* ------------------------------------------------------------------ */

static int xml_is_name_char(char c) {
    return isalnum((unsigned char)c) || c == '_' || c == '-' || c == '.' || c == ':';
}

static char *xml_read_name(xml_parser *x) {
    const char *start = x->p;
    while (x->p < x->end && xml_is_name_char(*x->p)) {
        x->p++;
    }
    if (x->p == start) {
        return NULL;
    }
    return tc_strndup(start, (size_t)(x->p - start));
}

/* ------------------------------------------------------------------ */
/* Parsing                                                            */
/* ------------------------------------------------------------------ */

static tc_status xml_parse_element(xml_parser *x, tc_xml_node *parent);

/* Reads the text content of the current element up to the next '<'. */
static tc_status xml_parse_text(xml_parser *x, tc_xml_node *parent) {
    const char *start = x->p;
    while (x->p < x->end && *x->p != '<') {
        x->p++;
    }
    if (x->p == start) {
        return TC_OK;
    }

    tc_buf b;
    tc_buf_init(&b);
    xml_decode_into(&b, start, (size_t)(x->p - start));
    if (b.oom) {
        tc_buf_free(&b);
        return tc_fail(TC_E_NOMEM, "xml: out of memory while decoding text");
    }

    /* Whitespace-only text between elements is formatting, not content. */
    const char *trimmed = b.data ? b.data : "";
    while (*trimmed && isspace((unsigned char)*trimmed)) {
        trimmed++;
    }
    size_t tlen = strlen(trimmed);
    while (tlen > 0 && isspace((unsigned char)trimmed[tlen - 1])) {
        tlen--;
    }
    if (tlen == 0) {
        tc_buf_free(&b);
        return TC_OK;
    }

    tc_xml_node *n = xml_new_node(TC_XML_TEXT);
    if (!n) {
        tc_buf_free(&b);
        return TC_E_NOMEM;
    }
    n->text = tc_strndup(trimmed, tlen);
    if (!n->text) {
        free(n);
        tc_buf_free(&b);
        return TC_E_NOMEM;
    }
    if (!xml_add_child(parent, n)) {
        free(n->text);
        free(n);
        tc_buf_free(&b);
        return TC_E_NOMEM;
    }
    tc_buf_free(&b);
    return TC_OK;
}

/* Parses attributes until '>' or '/>' is reached. Sets *self_closing. */
static tc_status xml_parse_attrs(xml_parser *x, tc_xml_node *node, int *self_closing) {
    *self_closing = 0;
    for (;;) {
        xml_skip_space(x);
        if (x->p >= x->end) {
            return tc_fail(TC_E_FORMAT, "xml: unterminated tag <%s>", node->name ? node->name : "?");
        }
        if (*x->p == '/') {
            x->p++;
            if (x->p < x->end && *x->p == '>') {
                x->p++;
                *self_closing = 1;
                return TC_OK;
            }
            return tc_fail(TC_E_FORMAT, "xml: malformed self-closing tag");
        }
        if (*x->p == '>') {
            x->p++;
            return TC_OK;
        }

        char *name = xml_read_name(x);
        if (!name) {
            return tc_fail(TC_E_FORMAT, "xml: malformed attribute name");
        }
        xml_skip_space(x);
        if (x->p >= x->end || *x->p != '=') {
            free(name);
            return tc_fail(TC_E_FORMAT, "xml: attribute without a value");
        }
        x->p++;
        xml_skip_space(x);
        if (x->p >= x->end || (*x->p != '"' && *x->p != '\'')) {
            free(name);
            return tc_fail(TC_E_FORMAT, "xml: attribute value is not quoted");
        }
        char quote = *x->p++;
        const char *vstart = x->p;
        while (x->p < x->end && *x->p != quote) {
            x->p++;
        }
        if (x->p >= x->end) {
            free(name);
            return tc_fail(TC_E_FORMAT, "xml: unterminated attribute value");
        }
        tc_buf vb;
        tc_buf_init(&vb);
        xml_decode_into(&vb, vstart, (size_t)(x->p - vstart));
        x->p++; /* closing quote */
        if (vb.oom) {
            tc_buf_free(&vb);
            free(name);
            return tc_fail(TC_E_NOMEM, "xml: out of memory while decoding an attribute");
        }
        char *value = tc_strdup(vb.data ? vb.data : "");
        tc_buf_free(&vb);
        if (!value || !xml_add_attr(node, name, value)) {
            free(name);
            free(value);
            return TC_E_NOMEM;
        }
    }
}

static tc_status xml_parse_element(xml_parser *x, tc_xml_node *parent) {
    /* Called with x->p pointing at '<'. */
    if (x->p + 1 >= x->end) {
        return tc_fail(TC_E_FORMAT, "xml: truncated document");
    }
    x->p++; /* consume '<' */

    if (x->p < x->end && *x->p == '/') {
        return tc_fail(TC_E_FORMAT, "xml: unexpected closing tag");
    }

    char *name = xml_read_name(x);
    if (!name) {
        return tc_fail(TC_E_FORMAT, "xml: malformed element name");
    }

    tc_xml_node *node = xml_new_node(TC_XML_ELEMENT);
    if (!node) {
        free(name);
        return TC_E_NOMEM;
    }
    node->name = name;

    int self_closing = 0;
    tc_status st = xml_parse_attrs(x, node, &self_closing);
    if (st != TC_OK) {
        tc_xml_free(node);
        return st;
    }
    if (self_closing) {
        if (!xml_add_child(parent, node)) {
            tc_xml_free(node);
            return TC_E_NOMEM;
        }
        return TC_OK;
    }

    /* Content until the matching closing tag. Nested elements of the same name
     * are handled by recursion, so no depth counter is needed here. */
    for (;;) {
        if (x->p >= x->end) {
            tc_xml_free(node);
            return tc_fail(TC_E_FORMAT, "xml: unterminated element <%s>", node->name);
        }
        if (*x->p == '<') {
            if (x->p + 1 < x->end && x->p[1] == '/') {
                x->p += 2;
                char *close = xml_read_name(x);
                if (!close) {
                    tc_xml_free(node);
                    return tc_fail(TC_E_FORMAT, "xml: malformed closing tag");
                }
                int match = strcmp(close, node->name) == 0;
                free(close);
                if (!match) {
                    tc_xml_free(node);
                    return tc_fail(TC_E_FORMAT, "xml: mismatched closing tag for <%s>", node->name);
                }
                xml_skip_space(x);
                if (x->p >= x->end || *x->p != '>') {
                    tc_xml_free(node);
                    return tc_fail(TC_E_FORMAT, "xml: malformed closing tag for <%s>", node->name);
                }
                x->p++;
                break;
            }
            if (x->p + 3 < x->end && strncmp(x->p, "<!--", 4) == 0) {
                x->p += 4;
                xml_skip_comment(x);
                continue;
            }
            if (x->p + 8 < x->end && strncmp(x->p, "<![CDATA[", 9) == 0) {
                x->p += 9;
                const char *cstart = x->p;
                while (x->p + 2 < x->end && strncmp(x->p, "]]>", 3) != 0) {
                    x->p++;
                }
                tc_buf cb;
                tc_buf_init(&cb);
                tc_buf_write(&cb, cstart, (size_t)(x->p - cstart));
                if (cb.oom) {
                    tc_buf_free(&cb);
                    tc_xml_free(node);
                    return TC_E_NOMEM;
                }
                if (cb.len > 0) {
                    tc_xml_node *tn = xml_new_node(TC_XML_TEXT);
                    if (!tn || !(tn->text = tc_strdup(cb.data))) {
                        free(tn);
                        tc_buf_free(&cb);
                        tc_xml_free(node);
                        return TC_E_NOMEM;
                    }
                    if (!xml_add_child(node, tn)) {
                        free(tn->text);
                        free(tn);
                        tc_buf_free(&cb);
                        tc_xml_free(node);
                        return TC_E_NOMEM;
                    }
                }
                tc_buf_free(&cb);
                if (x->p + 2 < x->end) {
                    x->p += 3;
                }
                continue;
            }
            if (x->p + 1 < x->end && x->p[1] == '?') {
                x->p += 2;
                xml_skip_pi(x);
                continue;
            }
            st = xml_parse_element(x, node);
            if (st != TC_OK) {
                tc_xml_free(node);
                return st;
            }
            continue;
        }
        st = xml_parse_text(x, node);
        if (st != TC_OK) {
            tc_xml_free(node);
            return st;
        }
    }

    if (!xml_add_child(parent, node)) {
        tc_xml_free(node);
        return TC_E_NOMEM;
    }
    return TC_OK;
}

tc_status tc_xml_parse(const char *text, size_t len, tc_xml_node **out) {
    if (!text || !out) {
        return tc_fail(TC_E_INVALID, "xml: null argument");
    }
    *out = NULL;

    xml_parser x;
    x.p = text;
    x.end = text + len;

    /* Skip a UTF-8 BOM: chapter files exported on Windows frequently have one
     * and it would otherwise be parsed as text content. */
    if (len >= 3 && (unsigned char)x.p[0] == 0xEF &&
        (unsigned char)x.p[1] == 0xBB && (unsigned char)x.p[2] == 0xBF) {
        x.p += 3;
    }

    tc_xml_node *root = xml_new_node(TC_XML_ELEMENT);
    if (!root) {
        return TC_E_NOMEM;
    }
    root->name = tc_strdup("#document");
    if (!root->name) {
        free(root);
        return TC_E_NOMEM;
    }

    /* Skip the prolog: declaration, comments, DOCTYPE and whitespace. */
    for (;;) {
        xml_skip_space(&x);
        if (x.p >= x.end) {
            break;
        }
        if (*x.p != '<') {
            tc_xml_free(root);
            return tc_fail(TC_E_FORMAT, "xml: content before the root element");
        }
        if (x.p + 3 < x.end && strncmp(x.p, "<!--", 4) == 0) {
            x.p += 4;
            xml_skip_comment(&x);
            continue;
        }
        if (x.p + 1 < x.end && x.p[1] == '?') {
            x.p += 2;
            xml_skip_pi(&x);
            continue;
        }
        if (x.p + 8 < x.end && tc_istarts_with(x.p, "<!DOCTYPE")) {
            xml_skip_doctype(&x);
            continue;
        }
        break;
    }

    if (x.p < x.end) {
        tc_status st = xml_parse_element(&x, root);
        if (st != TC_OK) {
            tc_xml_free(root);
            return st;
        }
    }

    if (root->child_count == 0) {
        tc_xml_free(root);
        return tc_fail(TC_E_FORMAT, "xml: no root element found");
    }
    *out = root;
    return TC_OK;
}

/* ------------------------------------------------------------------ */
/* Accessors                                                          */
/* ------------------------------------------------------------------ */

const tc_xml_node *tc_xml_child(const tc_xml_node *parent, const char *name) {
    if (!parent || !name) {
        return NULL;
    }
    for (size_t i = 0; i < parent->child_count; i++) {
        const tc_xml_node *c = &parent->children[i];
        if (c->kind == TC_XML_ELEMENT && c->name && strcmp(c->name, name) == 0) {
            return c;
        }
    }
    return NULL;
}

const tc_xml_node *tc_xml_child_ci(const tc_xml_node *parent, const char *name) {
    if (!parent || !name) {
        return NULL;
    }
    for (size_t i = 0; i < parent->child_count; i++) {
        const tc_xml_node *c = &parent->children[i];
        if (c->kind == TC_XML_ELEMENT && c->name && tc_ieq(c->name, name)) {
            return c;
        }
    }
    return NULL;
}

const char *tc_xml_attr(const tc_xml_node *node, const char *name) {
    if (!node || !name) {
        return NULL;
    }
    for (size_t i = 0; i < node->attr_count; i++) {
        if (tc_ieq(node->attrs[i].name, name)) {
            return node->attrs[i].value;
        }
    }
    return NULL;
}

const char *tc_xml_attr_or(const tc_xml_node *node, const char *name, const char *fallback) {
    const char *v = tc_xml_attr(node, name);
    return v ? v : fallback;
}

void tc_xml_text(const tc_xml_node *node, tc_buf *out) {
    if (!node || !out) {
        return;
    }
    for (size_t i = 0; i < node->child_count; i++) {
        const tc_xml_node *c = &node->children[i];
        if (c->kind == TC_XML_TEXT && c->text) {
            tc_buf_puts(out, c->text);
        }
    }
}
