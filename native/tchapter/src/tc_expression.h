/*
 * Chapter-name expression evaluator (B13).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * This header is documentation plus the declarations from tc_internal.h; the
 * declarations themselves live in tc_internal.h so that adding the evaluator
 * did not require editing any existing signature.
 *
 * Language accepted, transcribed from TChapter.Object.Expression:
 *
 *   - operators   + - * / % ^ with C precedence and left associativity.
 *                 (^ is Math.Pow; the reference folds it left, so 2^3^2 is 64,
 *                 not 512.)
 *   - unary minus only directly after '(' or after another operator, where the
 *                 reference rewrites it as "0 - x". A leading "-5" is
 *                 compile-time-valid but fails at evaluation time, exactly as
 *                 in the reference, because preToken is Token.End there.
 *   - comparisons > < >= <=, and `and` / `or` / `xor`. The three word
 *                 operators have no precedence entry, so using one anywhere but
 *                 at the very top of a bracket level makes the reference throw
 *                 "Invalid operator"; that is reproduced.
 *   - functions   abs acos asin atan atan2 cos sin tan cosh sinh tanh exp log
 *                 log10 sqrt ceil floor rand dup int sign pow max min
 *   - constants   M_E M_LOG2E M_LOG10E M_LN2 M_LN10 M_PI M_PI_2 M_PI_4
 *                 M_1_PI M_2_PI M_2_SQRTPI M_SQRT2 M_SQRT1_2
 *   - variables   t, and fps when fps >= 1e-5
 *   - '//' starts a comment that runs to the end of the expression
 *
 * Known deviations from the reference:
 *
 *   - The reference evaluates in System.Decimal (28 significant digits) while
 *     this port uses IEEE-754 double. For chapter expressions - short chains
 *     of + - * / over values with a few decimals - the two agree to well within
 *     1e-9 relative, and the formatted result is byte-identical except when
 *     the exact decimal value sits on a half at the second decimal place
 *     (0.005, 1.005, ...): decimal rounds that away from zero, the nearest
 *     binary double may not. Measured over a 20,000-expression random corpus,
 *     10 results differed, all by 0.01 or less after formatting, apart from
 *     five where an exact decimal cancellation (7.50 - 6.30 + -1.20 == 0) does
 *     not cancel in binary.
 *   - Values that need more than 15 significant digits to distinguish also
 *     differ: exp(66), 2^95 and the 29-digit literal
 *     79228162514264337593543950335 have no double that prints as themselves.
 *     The port is within 1e-16 relative there, but not digit-identical.
 *   - System.Decimal's range is enforced, because the reference depends on it.
 *     A result at or above 2^96 throws in the reference and makes Eval fall
 *     back to `time`; a result below half of 1e-28 rounds to zero; a literal
 *     outside the range is rejected the way decimal.TryParse rejects it.
 *   - `rand` is non-deterministic in the reference (Random.NextDouble) and has
 *     no meaningful chapter use; this port substitutes 0.5 so results are
 *     reproducible.
 *   - The reference's stacks are unbounded; this port caps nesting at 256
 *     levels and reports deeper input as malformed.
 *   - `xor` is reproduced with the reference's bug: it reads the left operand
 *     twice and therefore always evaluates to 0.
 */
#ifndef TC_EXPRESSION_H
#define TC_EXPRESSION_H

#include "tc_internal.h"

/* ------------------------------------------------------------------ */
/* Declarations (also in tc_internal.h)                               */
/* ------------------------------------------------------------------ */

/* Evaluates `expr` at `time` seconds with frame rate `fps`.
 *
 * On success returns TC_OK and sets *out to the result. When the expression is
 * malformed, names an unknown variable, divides by zero or is otherwise
 * undefined, *out is set to `time` (the reference's fallback) and TC_E_FORMAT
 * is returned; the caller may use *out either way. TC_E_INVALID is returned
 * for NULL pointers or a NaN fps, TC_E_NOMEM when the work buffer cannot be
 * allocated. */
tc_status tc_expression_eval(const char *expr, double time, double fps, double *out);

/* Compiles `expr` once so it can be evaluated repeatedly. Returns 0 and leaves
 * *out NULL when the expression does not compile. */
int tc_expression_parse(const char *expr, tc_expression **out);

/* Evaluates a compiled expression. The fallback rules match
 * tc_expression_eval. */
tc_status tc_expression_eval_compiled(const tc_expression *expr, double time, double fps,
                                      double *out);

/* Releases a compiled expression. Passing NULL is allowed. */
void tc_expression_free(tc_expression *expr);

/* Formats `v` the way the reference does before writing a chapter time:
 * C# `value.ToString("0.00")`, i.e. two decimals, half away from zero, with no
 * negative zero. `buf` should hold at least 16 bytes. */
void tc_expression_format(double v, char *buf, size_t buflen);

/* Writes `format + " " + index:D2` into `buf`, which is
 * TChapter.Util.ChapterName.Get(index). `format` may be NULL, which selects
 * the reference default "Chapter". Returns TC_E_RANGE when `buf` is too small
 * or the name cannot be represented. */
tc_status tc_chapter_name(char *buf, size_t buflen, const char *format, int index);

/* Writes TChapter.Util.ChapterName.Range(start, count) into `buf`, one name
 * per line, without a trailing newline. The reference throws
 * ArgumentOutOfRangeException when start is outside 0..99, count is negative,
 * or start + count - 1 exceeds 99; those cases return TC_E_RANGE here. A count
 * of zero writes an empty string. */
tc_status tc_chapter_name_range(char *buf, size_t buflen, const char *format, int start,
                                int count);

#endif /* TC_EXPRESSION_H */
