/*
 * Test runner for libtchapter.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Suites register themselves through constructors (see tc_test.h), so this file
 * never changes when a parser is added. That matters because the parser
 * packages are written in parallel.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_test.h"

int tc_test_failures = 0;
int tc_test_checks = 0;
const char *tc_test_case = "";

/* ------------------------------------------------------------------ */
/* Registry                                                           */
/* ------------------------------------------------------------------ */

/* Grown on demand; suites register before main runs, so no locking is needed. */
static tc_test_case_entry *g_suites = NULL;
static size_t g_suite_count = 0;
static size_t g_suite_cap = 0;

void tc_test_register(const char *name, tc_test_fn fn) {
    if (g_suite_count == g_suite_cap) {
        size_t cap = g_suite_cap ? g_suite_cap * 2 : 16;
        tc_test_case_entry *p = realloc(g_suites, cap * sizeof(*p));
        if (!p) {
            fprintf(stderr, "test registry: out of memory\n");
            exit(2);
        }
        g_suites = p;
        g_suite_cap = cap;
    }
    g_suites[g_suite_count].name = name;
    g_suites[g_suite_count].fn = fn;
    g_suite_count++;
}

/* ------------------------------------------------------------------ */
/* Fixtures                                                           */
/* ------------------------------------------------------------------ */

void tc_test_data_path(char *out, size_t outlen, const char *name) {
    /* The binary is run from native/tchapter, so testdata lives next to src. */
    snprintf(out, outlen, "testdata/%s", name);
}

tc_status tc_test_read_data(const char *name, char **out, size_t *len) {
    char path[1024];
    tc_test_data_path(path, sizeof(path), name);
    return tc_read_file(path, out, len);
}

/* ------------------------------------------------------------------ */
/* main                                                               */
/* ------------------------------------------------------------------ */

int main(int argc, char **argv) {
    const char *filter = argc > 1 ? argv[1] : NULL;

    if (g_suite_count == 0) {
        fprintf(stderr, "no test suites registered\n");
        return 2;
    }

    printf("running %zu suite(s)%s%s%s\n\n", g_suite_count,
           filter ? " matching " : "", filter ? filter : "", "");

    size_t ran = 0;
    for (size_t i = 0; i < g_suite_count; i++) {
        if (filter && !strstr(g_suites[i].name, filter)) {
            continue;
        }
        ran++;
        printf("-- %s\n", g_suites[i].name);
        fflush(stdout);
        g_suites[i].fn();
    }

    printf("\n%d checks, %d failures (%zu suite(s) run)\n",
           tc_test_checks, tc_test_failures, ran);
    free(g_suites);
    return tc_test_failures == 0 ? 0 : 1;
}
