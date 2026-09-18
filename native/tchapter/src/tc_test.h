/*
 * Minimal test harness for libtchapter.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Every parser ships its own test file that includes this header and declares a
 * suite with TC_SUITE. Suites register themselves through a constructor, so
 * adding a parser never means editing a central list, and twelve packages can be
 * written in parallel without touching the same file.
 *
 * GCC and Clang both support the constructor attribute; the local test run uses
 * mingw gcc and CI uses gcc, so no other compiler needs to be accommodated.
 */
#ifndef TC_TEST_H
#define TC_TEST_H

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_internal.h"

/* ------------------------------------------------------------------ */
/* Assertions                                                         */
/* ------------------------------------------------------------------ */

extern int tc_test_failures;
extern int tc_test_checks;
extern const char *tc_test_case;

#define TC_CHECK(cond)                                                       \
    do {                                                                     \
        tc_test_checks++;                                                    \
        if (!(cond)) {                                                       \
            tc_test_failures++;                                              \
            fprintf(stderr, "FAIL %s:%d [%s] %s\n", __FILE__, __LINE__,      \
                    tc_test_case, #cond);                                    \
        }                                                                    \
    } while (0)

#define TC_CHECK_EQ_INT(a, b)                                                \
    do {                                                                     \
        long long va_ = (long long)(a), vb_ = (long long)(b);                \
        tc_test_checks++;                                                    \
        if (va_ != vb_) {                                                    \
            tc_test_failures++;                                              \
            fprintf(stderr, "FAIL %s:%d [%s] %s = %lld, want %lld\n",        \
                    __FILE__, __LINE__, tc_test_case, #a, va_, vb_);         \
        }                                                                    \
    } while (0)

#define TC_CHECK_EQ_STR(a, b)                                                \
    do {                                                                     \
        const char *sa_ = (a), *sb_ = (b);                                   \
        tc_test_checks++;                                                    \
        if (!sa_ || !sb_ || strcmp(sa_, sb_) != 0) {                         \
            tc_test_failures++;                                              \
            fprintf(stderr, "FAIL %s:%d [%s] %s = \"%s\", want \"%s\"\n",    \
                    __FILE__, __LINE__, tc_test_case, #a,                    \
                    sa_ ? sa_ : "(null)", sb_ ? sb_ : "(null)");             \
        }                                                                    \
    } while (0)

#define TC_CHECK_CONTAINS(haystack, needle)                                  \
    do {                                                                     \
        const char *h_ = (haystack), *n_ = (needle);                         \
        tc_test_checks++;                                                    \
        if (!h_ || !n_ || !strstr(h_, n_)) {                                 \
            tc_test_failures++;                                              \
            fprintf(stderr, "FAIL %s:%d [%s] %s does not contain %s\n",      \
                    __FILE__, __LINE__, tc_test_case, #haystack, #needle);   \
        }                                                                    \
    } while (0)

/* Asserts that a parser reported failure. */
#define TC_CHECK_FAILS(expr)                                                 \
    do {                                                                     \
        tc_test_checks++;                                                    \
        if ((expr) == TC_OK) {                                               \
            tc_test_failures++;                                              \
            fprintf(stderr, "FAIL %s:%d [%s] %s unexpectedly succeeded\n",   \
                    __FILE__, __LINE__, tc_test_case, #expr);                \
        }                                                                    \
    } while (0)

/* ------------------------------------------------------------------ */
/* Suite registration                                                 */
/* ------------------------------------------------------------------ */

typedef void (*tc_test_fn)(void);

typedef struct tc_test_case_entry {
    const char *name;
    tc_test_fn fn;
} tc_test_case_entry;

#if defined(__GNUC__) || defined(__clang__)
#  define TC_CONSTRUCTOR __attribute__((constructor))
#else
#  define TC_CONSTRUCTOR
#endif

/* Registers a suite. Called automatically at startup. */
void tc_test_register(const char *name, tc_test_fn fn);

/* Declares a suite: TC_SUITE("ogm") { ...tests... } */
#define TC_SUITE(name)                                                       \
    static void tc_suite_fn_##name(void);                                    \
    static TC_CONSTRUCTOR void tc_suite_ctor_##name(void) {                  \
        tc_test_register(#name, tc_suite_fn_##name);                         \
    }                                                                        \
    static void tc_suite_fn_##name(void)

/* Sets the label used in failure messages for the current case. */
#define TC_CASE(label) tc_test_case = label

/* ------------------------------------------------------------------ */
/* Fixtures                                                           */
/* ------------------------------------------------------------------ */

/* Builds a path under the source tree's testdata directory. Test binaries are
 * run from the repository root (see the Makefile), so the path is relative. */
void tc_test_data_path(char *out, size_t outlen, const char *name);

/* Reads a testdata file into a NUL-terminated buffer the caller frees. */
tc_status tc_test_read_data(const char *name, char **out, size_t *len);

#endif /* TC_TEST_H */
