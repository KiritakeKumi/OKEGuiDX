/*
 * Tests for the chapter-name expression evaluator (B13).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * The main case replays testdata/expression.in against testdata/expression.out,
 * a 100-line corpus generated from the reference implementation. Expected
 * values were produced by running the actual TChapter.Object.Expression code
 * (a small .NET probe linking Expression.cs) and formatted with
 * `ToString("0.00")`, so the fixture is ground truth rather than a
 * reimplementation.
 */
#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_test.h"

/* ------------------------------------------------------------------ */
/* Fixture replay                                                     */
/* ------------------------------------------------------------------ */

static char *read_whole(const char *name, size_t *len) {
    char *buf = NULL;
    if (tc_test_read_data(name, &buf, len) != TC_OK) {
        return NULL;
    }
    return buf;
}

/* Splits `text` on '\n', stripping a trailing '\r'. Returns the number of
 * lines; the caller frees the returned array. */
static char **split_lines(char *text, size_t *count) {
    size_t cap = 64;
    size_t n = 0;
    char **lines = malloc(cap * sizeof(*lines));
    char *p = text;

    if (!lines) {
        return NULL;
    }
    while (*p) {
        char *start = p;
        char *nl = strchr(p, '\n');
        if (nl) {
            *nl = '\0';
            p = nl + 1;
        } else {
            p = start + strlen(start);
        }
        {
            size_t len = strlen(start);
            if (len > 0 && start[len - 1] == '\r') {
                start[len - 1] = '\0';
            }
        }
        if (n == cap) {
            char **bigger = realloc(lines, cap * 2 * sizeof(*lines));
            if (!bigger) {
                free(lines);
                return NULL;
            }
            lines = bigger;
            cap *= 2;
        }
        lines[n++] = start;
    }
    *count = n;
    return lines;
}

static void test_fixture_corpus(void) {
    size_t in_len = 0;
    size_t out_len = 0;
    char *input = read_whole("expression.in", &in_len);
    char *expected = read_whole("expression.out", &out_len);
    size_t in_count = 0;
    size_t out_count = 0;
    char **in_lines;
    char **out_lines;
    size_t i;
    int matched = 0;

    TC_CHECK(input != NULL);
    TC_CHECK(expected != NULL);
    if (!input || !expected) {
        free(input);
        free(expected);
        return;
    }

    in_lines = split_lines(input, &in_count);
    out_lines = split_lines(expected, &out_count);
    TC_CHECK(in_lines != NULL);
    TC_CHECK(out_lines != NULL);
    if (!in_lines || !out_lines) {
        free(in_lines);
        free(out_lines);
        free(input);
        free(expected);
        return;
    }

    /* The corpus is 100 lines; both files must describe the same number so a
     * truncated fixture cannot silently pass. */
    TC_CHECK_EQ_INT(in_count, out_count);
    TC_CHECK(in_count >= 100);

    for (i = 0; i < in_count && i < out_count; i++) {
        char got[64];
        double value = 0.0;
        tc_status st;

        if (in_lines[i][0] == '\0') {
            continue;
        }
        st = tc_expression_eval(in_lines[i], 0.0, 0.0, &value);
        if (st != TC_OK) {
            fprintf(stderr, "FAIL %s:%d [%s] line %d: eval failed (status %d) for: %s\n",
                    __FILE__, __LINE__, tc_test_case, (int)i + 1, (int)st, in_lines[i]);
            tc_test_failures++;
            tc_test_checks++;
            continue;
        }
        tc_expression_format(value, got, sizeof(got));
        tc_test_checks++;
        if (strcmp(got, out_lines[i]) != 0) {
            tc_test_failures++;
            fprintf(stderr, "FAIL %s:%d [%s] line %d: got \"%s\", want \"%s\" for: %s\n",
                    __FILE__, __LINE__, tc_test_case, (int)i + 1, got, out_lines[i],
                    in_lines[i]);
        } else {
            matched++;
        }
    }

    /* Guard against the loop silently skipping everything. */
    TC_CHECK_EQ_INT(matched, (int)out_count);

    free(in_lines);
    free(out_lines);
    free(input);
    free(expected);
}

/* Compares a formatted result against the fixture. An exact match is the norm.
 * The fallback exists for values whose exact decimal result needs more than 15
 * significant digits, which a double cannot represent: there the port is
 * allowed to differ by up to 1e-9 relative, matching the documented deviation
 * from System.Decimal. */
static int value_matches(const char *got, const char *want) {
    double g;
    double w;
    char *end_g = NULL;
    char *end_w = NULL;

    if (strcmp(got, want) == 0) {
        return 1;
    }
    g = strtod(got, &end_g);
    w = strtod(want, &end_w);
    if (end_g == got || *end_g != '\0' || end_w == want || *end_w != '\0') {
        return 0;
    }
    {
        double scale = fabs(w) > 1.0 ? fabs(w) : 1.0;
        return fabs(g - w) / scale <= 1e-9;
    }
}

/* The second fixture covers the error paths, the operator and function set,
 * fps binding and the decimal range limits. Its expected column was produced by
 * the same oracle, and the companion .err file flags the lines where the
 * reference threw and therefore fell back to `time` (0 here). A "1" flag means
 * the port must report a failure too. */
static void test_fixture_edge(void) {
    size_t in_len = 0;
    size_t out_len = 0;
    size_t err_len = 0;
    char *input = read_whole("expression_edge.in", &in_len);
    char *expected = read_whole("expression_edge.out", &out_len);
    char *errs = read_whole("expression_edge.err", &err_len);
    size_t in_count = 0;
    size_t out_count = 0;
    size_t err_count = 0;
    char **in_lines;
    char **out_lines;
    char **err_lines;
    size_t i;
    int exact = 0;
    int approximate = 0;
    int rejected = 0;

    TC_CHECK(input != NULL);
    TC_CHECK(expected != NULL);
    TC_CHECK(errs != NULL);
    if (!input || !expected || !errs) {
        free(input);
        free(expected);
        free(errs);
        return;
    }

    in_lines = split_lines(input, &in_count);
    out_lines = split_lines(expected, &out_count);
    err_lines = split_lines(errs, &err_count);
    TC_CHECK(in_lines != NULL);
    TC_CHECK(out_lines != NULL);
    TC_CHECK(err_lines != NULL);
    if (!in_lines || !out_lines || !err_lines) {
        free(in_lines);
        free(out_lines);
        free(err_lines);
        free(input);
        free(expected);
        free(errs);
        return;
    }
    TC_CHECK_EQ_INT(in_count, out_count);
    TC_CHECK_EQ_INT(in_count, err_count);

    for (i = 0; i < in_count && i < out_count && i < err_count; i++) {
        char got[64];
        double value = 0.0;
        tc_status st;

        if (in_lines[i][0] == '\0') {
            continue;
        }
        /* `time` is zero here, which is the value the oracle used. */
        st = tc_expression_eval(in_lines[i], 0.0, 0.0, &value);
        tc_test_checks++;
        if (err_lines[i][0] == '1') {
            if (st == TC_OK) {
                tc_test_failures++;
                fprintf(stderr,
                        "FAIL %s:%d [%s] line %d: expected rejection for: %s\n", __FILE__,
                        __LINE__, tc_test_case, (int)i + 1, in_lines[i]);
            } else {
                rejected++;
            }
            continue;
        }
        if (st != TC_OK) {
            tc_test_failures++;
            fprintf(stderr, "FAIL %s:%d [%s] line %d: eval failed (status %d) for: %s\n",
                    __FILE__, __LINE__, tc_test_case, (int)i + 1, (int)st, in_lines[i]);
            continue;
        }
        tc_expression_format(value, got, sizeof(got));
        if (strcmp(got, out_lines[i]) == 0) {
            exact++;
        } else if (value_matches(got, out_lines[i])) {
            approximate++;
        } else {
            tc_test_failures++;
            fprintf(stderr, "FAIL %s:%d [%s] line %d: got \"%s\", want \"%s\" for: %s\n",
                    __FILE__, __LINE__, tc_test_case, (int)i + 1, got, out_lines[i],
                    in_lines[i]);
        }
    }

    TC_CHECK(exact > 150);
    TC_CHECK(rejected > 15);
    TC_CHECK(exact + approximate + rejected > 230);

    free(in_lines);
    free(out_lines);
    free(err_lines);
    free(input);
    free(expected);
    free(errs);
}

/* ------------------------------------------------------------------ */
/* Arithmetic and tokenising oddities                                 */
/* ------------------------------------------------------------------ */

static double eval_ok(const char *expr, double time) {
    double v = -12345.0;
    tc_status st = tc_expression_eval(expr, time, 0.0, &v);
    TC_CHECK_EQ_INT(st, TC_OK);
    return v;
}

static void test_operators(void) {
    TC_CHECK_EQ_INT((long long)eval_ok("( 2 + 3 )", 0.0), 5);
    TC_CHECK_EQ_INT((long long)eval_ok("( 2 - 3 )", 0.0), -1);
    TC_CHECK_EQ_INT((long long)eval_ok("( 2 * 3 )", 0.0), 6);
    TC_CHECK_EQ_INT((long long)eval_ok("( 8 / 2 )", 0.0), 4);
    /* % is C# decimal remainder, which follows the sign of the left operand. */
    TC_CHECK_EQ_INT((long long)eval_ok("( 10 % 3 )", 0.0), 1);
    TC_CHECK_EQ_INT((long long)eval_ok("( -10 % 3 )", 0.0), -1);
    TC_CHECK_EQ_INT((long long)eval_ok("( 10 % -3 )", 0.0), 1);
    /* ^ is Math.Pow and, like every binary operator here, folds left. */
    TC_CHECK_EQ_INT((long long)eval_ok("( 2 ^ 3 )", 0.0), 8);
    TC_CHECK_EQ_INT((long long)eval_ok("( 2 ^ 3 ^ 2 )", 0.0), 64);
    /* Precedence. */
    TC_CHECK_EQ_INT((long long)eval_ok("( 1 + 2 * 3 )", 0.0), 7);
    TC_CHECK_EQ_INT((long long)eval_ok("( ( 1 + 2 ) * 3 )", 0.0), 9);
    TC_CHECK_EQ_INT((long long)eval_ok("( 6 / 2 * 3 )", 0.0), 9);
    TC_CHECK_EQ_INT((long long)eval_ok("( 1 - 2 - 3 )", 0.0), -4);
}

static void test_unary_minus(void) {
    /* Only valid after '(' or another operator; the reference rewrites it as
     * "0 - x" during compilation. */
    TC_CHECK_EQ_INT((long long)eval_ok("( -5 )", 0.0), -5);
    TC_CHECK_EQ_INT((long long)eval_ok("( 1 - -2 )", 0.0), 3);
    TC_CHECK_EQ_INT((long long)eval_ok("( 1 - - 2 )", 0.0), 3);
    TC_CHECK_EQ_INT((long long)eval_ok("( 1--2 )", 0.0), 3);
    TC_CHECK_EQ_INT((long long)eval_ok("( 2 * - 3 )", 0.0), -6);
    TC_CHECK_EQ_INT((long long)eval_ok("( - - 5 )", 0.0), 5);
    TC_CHECK_EQ_INT((long long)eval_ok("( - - - 5 )", 0.0), -5);
    /* -2^3 is (-2)^3 here because the synthetic zero makes the minus the left
     * operand of '^', exactly as in the reference. */
    TC_CHECK_EQ_INT((long long)eval_ok("( - 2 ^ 3 )", 0.0), -8);
}

static void test_leading_minus_fails(void) {
    /* A leading '-' compiles (the token is pushed) but leaves no left operand,
     * so evaluation fails and the value falls back to `time`. */
    double v = 0.0;
    tc_status st = tc_expression_eval("-5", 7.0, 0.0, &v);
    TC_CHECK(st != TC_OK);
    TC_CHECK(v == 7.0);

    st = tc_expression_eval("- 5", 7.0, 0.0, &v);
    TC_CHECK(st != TC_OK);
    TC_CHECK(v == 7.0);
}

static void test_comparisons_and_logic(void) {
    TC_CHECK_EQ_INT((long long)eval_ok("( 1 < 2 )", 0.0), 1);
    TC_CHECK_EQ_INT((long long)eval_ok("( 2 < 1 )", 0.0), 0);
    TC_CHECK_EQ_INT((long long)eval_ok("( 1 <= 1 )", 0.0), 1);
    TC_CHECK_EQ_INT((long long)eval_ok("( 2 >= 2 )", 0.0), 1);
    TC_CHECK_EQ_INT((long long)eval_ok("( 2 > 1 )", 0.0), 1);
    TC_CHECK_EQ_INT((long long)eval_ok("( 1 + 2 > 2 )", 0.0), 1);

    /* and/or only work at the very top of a bracket level: they have no
     * precedence entry, so anywhere else the reference throws. */
    TC_CHECK_EQ_INT((long long)eval_ok("( 1 and 1 )", 0.0), 1);
    TC_CHECK_EQ_INT((long long)eval_ok("( 1 and 0 )", 0.0), 0);
    TC_CHECK_EQ_INT((long long)eval_ok("( 0 or 1 )", 0.0), 1);
    TC_CHECK_EQ_INT((long long)eval_ok("( 0 or 0 )", 0.0), 0);
    TC_CHECK_EQ_INT((long long)eval_ok("( t and 1 )", 5.0), 1);

    {
        double v = 0.0;
        tc_status st = tc_expression_eval("( 1 + 1 and 1 )", 3.0, 0.0, &v);
        TC_CHECK(st != TC_OK);
        TC_CHECK(v == 3.0);
    }
}

static void test_xor_bug_is_reproduced(void) {
    /* The reference reads the left operand twice, so xor is always 0. Keeping
     * the bug is deliberate: see tc_expression.h. */
    TC_CHECK_EQ_INT((long long)eval_ok("( 1 xor 1 )", 0.0), 0);
    TC_CHECK_EQ_INT((long long)eval_ok("( 1 xor 0 )", 0.0), 0);
    TC_CHECK_EQ_INT((long long)eval_ok("( 0 xor 1 )", 0.0), 0);
    TC_CHECK_EQ_INT((long long)eval_ok("( 0 xor 0 )", 0.0), 0);
    TC_CHECK_EQ_INT((long long)eval_ok("( 5 xor 0 )", 0.0), 0);
    TC_CHECK_EQ_INT((long long)eval_ok("( 0 xor 5 )", 0.0), 0);
}

static void test_functions(void) {
    TC_CHECK_EQ_INT((long long)eval_ok("abs ( -3 )", 0.0), 3);
    TC_CHECK_EQ_INT((long long)eval_ok("ceil ( 1.2 )", 0.0), 2);
    TC_CHECK_EQ_INT((long long)eval_ok("floor ( 1.8 )", 0.0), 1);
    TC_CHECK_EQ_INT((long long)eval_ok("int ( 1.8 )", 0.0), 1);
    TC_CHECK_EQ_INT((long long)eval_ok("int ( -1.8 )", 0.0), -1);
    TC_CHECK_EQ_INT((long long)eval_ok("sign ( -3 )", 0.0), -1);
    TC_CHECK_EQ_INT((long long)eval_ok("sign ( 0 )", 0.0), 0);
    TC_CHECK_EQ_INT((long long)eval_ok("sign ( 3 )", 0.0), 1);
    TC_CHECK_EQ_INT((long long)eval_ok("pow ( 2 , 10 )", 0.0), 1024);
    TC_CHECK_EQ_INT((long long)eval_ok("max ( 3 , 5 )", 0.0), 5);
    TC_CHECK_EQ_INT((long long)eval_ok("min ( 3 , 5 )", 0.0), 3);
    TC_CHECK_EQ_INT((long long)eval_ok("sqrt ( 4 )", 0.0), 2);
    TC_CHECK_EQ_INT((long long)eval_ok("log10 ( 100 )", 0.0), 2);
    TC_CHECK_EQ_INT((long long)eval_ok("cos ( 0 )", 0.0), 1);
    TC_CHECK_EQ_INT((long long)eval_ok("sin ( 0 )", 0.0), 0);
    TC_CHECK_EQ_INT((long long)eval_ok("exp ( 0 )", 0.0), 1);
    TC_CHECK(fabs(eval_ok("atan2 ( 1 , 1 )", 0.0) - 0.785398163397448) < 1e-12);
}

static void test_constants(void) {
    TC_CHECK(fabs(eval_ok("M_PI", 0.0) - 3.14159265358979323846) < 1e-12);
    TC_CHECK(fabs(eval_ok("M_E", 0.0) - 2.71828182845904523536) < 1e-12);
    TC_CHECK(fabs(eval_ok("( M_PI / 2 )", 0.0) - 1.57079632679489661923) < 1e-12);
}

static void test_variables(void) {
    char buf[32];
    double v = 0.0;

    TC_CHECK_EQ_INT(tc_expression_eval("t", 12.5, 0.0, &v), TC_OK);
    TC_CHECK(v == 12.5);

    TC_CHECK_EQ_INT(tc_expression_eval("( t * 2 )", 12.5, 0.0, &v), TC_OK);
    TC_CHECK(v == 25.0);

    TC_CHECK_EQ_INT(tc_expression_eval("fps", 0.0, 24000.0 / 1001.0, &v), TC_OK);
    TC_CHECK(fabs(v - 23.976023976) < 1e-6);

    /* Below 1e-5 the reference does not bind fps, so naming it fails and the
     * value falls back to `time`. */
    TC_CHECK_EQ_INT(tc_expression_eval("fps", 12.5, 0.0, &v), TC_E_FORMAT);
    TC_CHECK(v == 12.5);
    TC_CHECK_EQ_INT(tc_expression_eval("fps", 12.5, 0.000001, &v), TC_E_FORMAT);
    TC_CHECK(v == 12.5);
    /* At the threshold it is bound. */
    TC_CHECK_EQ_INT(tc_expression_eval("( fps * 1 )", 12.5, 0.00001, &v), TC_OK);
    TC_CHECK(v == 0.00001);

    /* An unknown variable fails; the reference's dictionary lookup throws. */
    TC_CHECK_EQ_INT(tc_expression_eval("( foo + 1 )", 9.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK(v == 9.0);

    tc_expression_format(3.14159265358979323846, buf, sizeof(buf));
    TC_CHECK_EQ_STR(buf, "3.14");
    tc_expression_format(-0.5, buf, sizeof(buf));
    TC_CHECK_EQ_STR(buf, "-0.50");
    tc_expression_format(0.0, buf, sizeof(buf));
    TC_CHECK_EQ_STR(buf, "0.00");
}

/* ------------------------------------------------------------------ */
/* Error paths                                                        */
/* ------------------------------------------------------------------ */

static void test_error_paths(void) {
    double v = 0.0;

    /* Division and modulo by zero. */
    TC_CHECK_EQ_INT(tc_expression_eval("( 1 / 0 )", 5.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK(v == 5.0);
    TC_CHECK_EQ_INT(tc_expression_eval("( 1 % 0 )", 5.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK(v == 5.0);
    TC_CHECK_EQ_INT(tc_expression_eval("( 0 / 0 )", 5.0, 0.0, &v), TC_E_FORMAT);

    /* Unbalanced brackets. ")" is always fatal; a lone "(" compiles, but the
     * leftover bracket leaves the value stack empty, so evaluation fails and
     * falls back to `time` (matching the reference, whose Eval skips bracket
     * tokens and then finds an empty stack). */
    TC_CHECK_EQ_INT(tc_expression_eval(")", 5.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK(v == 5.0);
    TC_CHECK_EQ_INT(tc_expression_eval("( 1 + 2 ) )", 5.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK(v == 5.0);
    /* "( 1 + 2" compiles and evaluates to 3; "( 1 + )" does not, because the
     * ')' flushes the '+' with only one operand on the value stack. */
    TC_CHECK_EQ_INT(tc_expression_eval("( 1 + 2", 0.0, 0.0, &v), TC_OK);
    TC_CHECK(v == 3.0);
    TC_CHECK_EQ_INT(tc_expression_eval("( 1 + )", 5.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK(v == 5.0);
    TC_CHECK_EQ_INT(tc_expression_eval("(", 5.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK(v == 5.0);

    /* Empty and blank expressions yield nothing to evaluate. */
    TC_CHECK_EQ_INT(tc_expression_eval("", 5.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK(v == 5.0);
    TC_CHECK_EQ_INT(tc_expression_eval("   ", 5.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK_EQ_INT(tc_expression_eval("+", 5.0, 0.0, &v), TC_E_FORMAT);

    /* Illegal characters are skipped by the reference, so "!1" is just 1 and
     * an unknown name becomes a variable that fails to look up. */
    TC_CHECK_EQ_INT(tc_expression_eval("1 +", 5.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK_EQ_INT(tc_expression_eval("not", 5.0, 0.0, &v), TC_E_FORMAT);

    /* Malformed numbers are compile errors. */
    TC_CHECK_EQ_INT(tc_expression_eval("1e5", 5.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK_EQ_INT(tc_expression_eval("( 1.2.3 + 1 )", 5.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK_EQ_INT(tc_expression_eval(".", 5.0, 0.0, &v), TC_E_FORMAT);

    /* Domain errors that the reference reports by throwing. */
    TC_CHECK_EQ_INT(tc_expression_eval("sqrt ( -1 )", 5.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK_EQ_INT(tc_expression_eval("log ( -1 )", 5.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK_EQ_INT(tc_expression_eval("log ( 0 )", 5.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK_EQ_INT(tc_expression_eval("acos ( 2 )", 5.0, 0.0, &v), TC_E_FORMAT);

    /* Bad arguments. */
    TC_CHECK_EQ_INT(tc_expression_eval(NULL, 0.0, 0.0, &v), TC_E_INVALID);
    TC_CHECK_EQ_INT(tc_expression_eval("1", 0.0, 0.0, NULL), TC_E_INVALID);
}

static void test_deep_nesting_is_bounded(void) {
    /* The reference's stack is unbounded; this port caps depth and reports
     * anything deeper as malformed rather than overflowing. */
    char expr[4096];
    size_t pos = 0;
    double v = 0.0;
    int i;

    for (i = 0; i < 600; i++) {
        expr[pos++] = '(';
    }
    expr[pos++] = '1';
    for (i = 0; i < 600; i++) {
        expr[pos++] = ')';
    }
    expr[pos] = '\0';

    TC_CHECK_EQ_INT(tc_expression_eval(expr, 5.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK(v == 5.0);

    /* A depth the port supports must still evaluate. */
    pos = 0;
    for (i = 0; i < 100; i++) {
        expr[pos++] = '(';
    }
    expr[pos++] = '1';
    for (i = 0; i < 100; i++) {
        expr[pos++] = ')';
    }
    expr[pos] = '\0';
    TC_CHECK_EQ_INT(tc_expression_eval(expr, 0.0, 0.0, &v), TC_OK);
    TC_CHECK(v == 1.0);
}

/* ------------------------------------------------------------------ */
/* Compiled expressions                                               */
/* ------------------------------------------------------------------ */

static void test_compiled_round_trip(void) {
    tc_expression *e = NULL;
    double v = 0.0;

    TC_CHECK(tc_expression_parse("( t * 2 + 1 )", &e) != 0);
    TC_CHECK(e != NULL);
    if (e) {
        TC_CHECK_EQ_INT(tc_expression_eval_compiled(e, 10.0, 0.0, &v), TC_OK);
        TC_CHECK(v == 21.0);
        TC_CHECK_EQ_INT(tc_expression_eval_compiled(e, 0.5, 0.0, &v), TC_OK);
        TC_CHECK(v == 2.0);
        tc_expression_free(e);
    }

    e = NULL;
    /* "( 1 + )" compiles but fails at evaluation: the ')' flushes the '+'
     * with a single operand. */
    TC_CHECK(tc_expression_parse("( 1 + )", &e) != 0);
    if (e) {
        v = 3.0;
        TC_CHECK_EQ_INT(tc_expression_eval_compiled(e, 3.0, 0.0, &v), TC_E_FORMAT);
        TC_CHECK(v == 3.0);
        tc_expression_free(e);
    }
    e = NULL;
    TC_CHECK(tc_expression_parse("( 1 + 2 ) )", &e) == 0);
    TC_CHECK(e == NULL);
    TC_CHECK(tc_expression_parse(NULL, &e) == 0);

    /* Evaluation of a compiled expression reports the same failures. */
    e = NULL;
    TC_CHECK(tc_expression_parse("( 1 / 0 )", &e) != 0);
    if (e) {
        v = 3.0;
        TC_CHECK_EQ_INT(tc_expression_eval_compiled(e, 3.0, 0.0, &v), TC_E_FORMAT);
        TC_CHECK(v == 3.0);
        tc_expression_free(e);
    }
    tc_expression_free(NULL); /* must not crash */
}

/* A compiled expression must not borrow the caller's text: the tokens that name
 * a variable point into it. */
static void test_compiled_owns_its_text(void) {
    tc_expression *e = NULL;
    double v = 0.0;
    char *source = malloc(32);

    TC_CHECK(source != NULL);
    if (!source) {
        return;
    }
    strcpy(source, "( t * 2 + 1 )");
    TC_CHECK(tc_expression_parse(source, &e) != 0);
    /* Overwrite the source with something that would evaluate differently. */
    strcpy(source, "( 9 * 9 * 9 )");
    if (e) {
        TC_CHECK_EQ_INT(tc_expression_eval_compiled(e, 10.0, 0.0, &v), TC_OK);
        TC_CHECK(v == 21.0);
        tc_expression_free(e);
    }
    free(source);
}

/* Values whose exact decimal result needs more than 15 significant digits
 * cannot be represented as a double. This pins the documented limitation so a
 * future change cannot silently make it worse: the port reports decimal's
 * overflow instead of leaking an infinity, and a literal with more digits than
 * decimal can hold is rejected the way decimal.TryParse rejects it. */
static void test_decimal_range_limits(void) {
    double v = 0.0;

    /* 2^96 is the first value that does not fit in System.Decimal. */
    TC_CHECK_EQ_INT(tc_expression_eval("( 2 ^ 96 )", 5.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK(v == 5.0);
    TC_CHECK_EQ_INT(tc_expression_eval("( 2 ^ 100 )", 5.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK_EQ_INT(tc_expression_eval("( 100000000000000000000 * 100000000000000000000 )",
                                       5.0, 0.0, &v),
                    TC_E_FORMAT);
    TC_CHECK(v == 5.0);

    /* 1e-28 is decimal's smallest representable magnitude; it does not
     * underflow, and formats as 0.00. The double lands within an ULP of it. */
    TC_CHECK_EQ_INT(tc_expression_eval("( 1 / 10000000000000000000000000000 )", 5.0, 0.0, &v),
                    TC_OK);
    TC_CHECK(fabs(v - 1e-28) < 1e-42);
    TC_CHECK_EQ_INT(
        tc_expression_eval("( 0.0000000000000000000000000001 * 0.1 )", 5.0, 0.0, &v), TC_OK);
    TC_CHECK(v == 0.0);
    /* A literal with more decimals than decimal can hold is also rounded. */
    TC_CHECK_EQ_INT(tc_expression_eval("0.00000000000000000000000000001", 5.0, 0.0, &v),
                    TC_OK);
    TC_CHECK(v == 0.0);

    /* A 30-digit literal does not fit in decimal at all, so parsing rejects it
     * (the reference reports "Invalid number token"). */
    TC_CHECK_EQ_INT(
        tc_expression_eval("( 792281625142643375935439503350 )", 5.0, 0.0, &v), TC_E_FORMAT);
    TC_CHECK(v == 5.0);

    /* Formatting an out-of-range value never emits an exponent or "inf". */
    {
        char buf[64];
        tc_expression_format(1e308, buf, sizeof(buf));
        TC_CHECK(strchr(buf, 'e') == NULL);
        TC_CHECK(strstr(buf, "inf") == NULL);
        tc_expression_format(-1e308, buf, sizeof(buf));
        TC_CHECK(strchr(buf, 'e') == NULL);
    }
}

/* ------------------------------------------------------------------ */
/* Formatting                                                         */
/* ------------------------------------------------------------------ */

static void check_format(double v, const char *want) {
    char buf[64];
    tc_expression_format(v, buf, sizeof(buf));
    tc_test_checks++;
    if (strcmp(buf, want) != 0) {
        tc_test_failures++;
        fprintf(stderr, "FAIL %s:%d [%s] format(%.17g) = \"%s\", want \"%s\"\n", __FILE__,
                __LINE__, tc_test_case, v, buf, want);
    }
}

static void test_formatting(void) {
    check_format(11.1, "11.10");
    check_format(-0.4, "-0.40");
    check_format(0.0, "0.00");
    check_format(3.14159265358979323846, "3.14");
    /* Negative zero and tiny negatives format as zero: decimal has no -0. */
    check_format(-0.0, "0.00");
    check_format(-0.001, "0.00");
    check_format(-0.005, "-0.01");
    check_format(0.005, "0.01");
    check_format(0.015, "0.02");
    check_format(2.675, "2.68");
    check_format(-2.675, "-2.68");
    check_format(99.995, "100.00");
    check_format(0.125, "0.13");
    check_format(1e6, "1000000.00");
}

/* ------------------------------------------------------------------ */
/* Chapter names                                                      */
/* ------------------------------------------------------------------ */

static void check_name(int index, const char *want) {
    char buf[64];
    tc_status st = tc_chapter_name(buf, sizeof(buf), NULL, index);
    TC_CHECK_EQ_INT(st, TC_OK);
    TC_CHECK_EQ_STR(buf, want);
}

static void test_chapter_names(void) {
    check_name(0, "Chapter 00");
    check_name(1, "Chapter 01");
    check_name(9, "Chapter 09");
    check_name(10, "Chapter 10");
    check_name(99, "Chapter 99");
    /* D2 is a minimum width, so 100 does not truncate. */
    check_name(100, "Chapter 100");
    check_name(1000, "Chapter 1000");
    check_name(-1, "Chapter -01");
    check_name(-5, "Chapter -05");

    {
        char buf[64];
        TC_CHECK_EQ_INT(tc_chapter_name(buf, sizeof(buf), "Part", 3), TC_OK);
        TC_CHECK_EQ_STR(buf, "Part 03");

        /* Too small a buffer is a range error rather than a truncation. */
        TC_CHECK_EQ_INT(tc_chapter_name(buf, 4, NULL, 1), TC_E_RANGE);
        TC_CHECK_EQ_INT(tc_chapter_name(NULL, 0, NULL, 1), TC_E_INVALID);
    }
}

static void test_chapter_range(void) {
    char buf[256];
    tc_status st;

    st = tc_chapter_name_range(buf, sizeof(buf), NULL, 0, 0);
    TC_CHECK_EQ_INT(st, TC_OK);
    TC_CHECK_EQ_STR(buf, "");

    st = tc_chapter_name_range(buf, sizeof(buf), NULL, 0, 2);
    TC_CHECK_EQ_INT(st, TC_OK);
    TC_CHECK_EQ_STR(buf, "Chapter 00\nChapter 01");

    st = tc_chapter_name_range(buf, sizeof(buf), NULL, 98, 2);
    TC_CHECK_EQ_INT(st, TC_OK);
    TC_CHECK_EQ_STR(buf, "Chapter 98\nChapter 99");

    st = tc_chapter_name_range(buf, sizeof(buf), NULL, 99, 1);
    TC_CHECK_EQ_INT(st, TC_OK);
    TC_CHECK_EQ_STR(buf, "Chapter 99");

    /* The reference's ArgumentOutOfRangeException cases. */
    TC_CHECK_EQ_INT(tc_chapter_name_range(buf, sizeof(buf), NULL, 100, 1), TC_E_RANGE);
    TC_CHECK_EQ_INT(tc_chapter_name_range(buf, sizeof(buf), NULL, 0, -1), TC_E_RANGE);
    TC_CHECK_EQ_INT(tc_chapter_name_range(buf, sizeof(buf), NULL, 0, 100), TC_E_RANGE);
    TC_CHECK_EQ_INT(tc_chapter_name_range(buf, sizeof(buf), NULL, 99, 2), TC_E_RANGE);
    TC_CHECK_EQ_INT(tc_chapter_name_range(buf, sizeof(buf), NULL, -1, 1), TC_E_RANGE);
}

/* ------------------------------------------------------------------ */
/* Comments and whitespace                                            */
/* ------------------------------------------------------------------ */

static void test_comments(void) {
    double v = 0.0;

    /* "//" stops the scan; whatever was compiled so far still evaluates. The
     * reference's postfix stack enumerates top-first, so Reverse() hands the
     * evaluator the operators before the operands: only the tokens that were
     * already moved to the output stack take part. */
    TC_CHECK_EQ_INT(tc_expression_eval("( 1 + 2 ) // comment", 0.0, 0.0, &v), TC_OK);
    TC_CHECK(v == 3.0);
    TC_CHECK_EQ_INT(tc_expression_eval("1 // trailing", 0.0, 0.0, &v), TC_OK);
    TC_CHECK(v == 1.0);
    /* The '+' has not been flushed yet, so this evaluates to 1 + 2 = 3. */
    TC_CHECK_EQ_INT(tc_expression_eval("( 1 + 2 // comment )", 0.0, 0.0, &v), TC_OK);
    TC_CHECK(v == 3.0);
    /* A comment-only expression leaves nothing on the stack. */
    TC_CHECK_EQ_INT(tc_expression_eval("// full comment", 5.0, 0.0, &v), TC_E_FORMAT);
}

/* ------------------------------------------------------------------ */
/* Suite                                                              */
/* ------------------------------------------------------------------ */

TC_SUITE(expression) {
    TC_CASE("fixture_corpus");         test_fixture_corpus();
    TC_CASE("fixture_edge");           test_fixture_edge();
    TC_CASE("operators");              test_operators();
    TC_CASE("unary_minus");            test_unary_minus();
    TC_CASE("leading_minus");          test_leading_minus_fails();
    TC_CASE("comparisons");            test_comparisons_and_logic();
    TC_CASE("xor_bug");                test_xor_bug_is_reproduced();
    TC_CASE("functions");              test_functions();
    TC_CASE("constants");              test_constants();
    TC_CASE("variables");              test_variables();
    TC_CASE("error_paths");            test_error_paths();
    TC_CASE("deep_nesting");           test_deep_nesting_is_bounded();
    TC_CASE("compiled");               test_compiled_round_trip();
    TC_CASE("compiled_owns_text");     test_compiled_owns_its_text();
    TC_CASE("decimal_range");          test_decimal_range_limits();
    TC_CASE("formatting");             test_formatting();
    TC_CASE("chapter_names");          test_chapter_names();
    TC_CASE("chapter_range");          test_chapter_range();
    TC_CASE("comments");               test_comments();
}
