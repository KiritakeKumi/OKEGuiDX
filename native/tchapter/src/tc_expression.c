/*
 * Chapter-name expression evaluator (B13).
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Ported from TChapter.Object.Expression and TChapter.Util.ChapterName.
 *
 * The reference is a shunting-yard compiler followed by a stack machine, and
 * this file keeps that two-stage shape because the quirks live in the compiler:
 * a leading '-' is only accepted directly after '(' or after another operator,
 * a lone '(' is legal, and a stray ')' is a hard failure. The tokeniser, the
 * operator table, the precedence values and the unary-minus rule below are
 * transcribed from the reference rather than redesigned.
 *
 * Precision: the reference evaluates in System.Decimal (28 significant digits)
 * while this port uses IEEE-754 double. For chapter expressions - short chains
 * of + - * / over values with a few decimals - the two agree to well within
 * 1e-9 relative, and the formatted result is byte-identical except when the
 * exact decimal value sits on a half at the second decimal place (0.005,
 * 1.005, ...): decimal rounds that away from zero, the nearest binary double
 * may not. Values that need more than 15 significant digits to distinguish
 * (exp(66), 2^95) differ in the last digits for the same reason. Both are
 * measured and documented in tc_expression.h.
 *
 * System.Decimal's range is also enforced, because the reference relies on it:
 * a result at or above 2^96 makes the reference throw and fall back to `time`,
 * and one below 1e-28 is rounded to zero. Without that, a large expression
 * would leak an infinity here where the reference yields the timestamp.
 *
 * The reference's `xor` operator reads one operand twice (`var t1 =
 * value.Number != 0; var t2 = value.Number != 0;`), so it always yields 0.
 * That is reproduced verbatim, bug included, because a chapter expression in
 * the wild may depend on it.
 */
#include <float.h>
#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "tc_internal.h"
#include "tc_expression.h"

/* ------------------------------------------------------------------ */
/* Token kinds                                                        */
/* ------------------------------------------------------------------ */

typedef enum expr_kind {
    EXPR_BLANK = 0, /* the sentinel at the bottom of the operator stack */
    EXPR_NUMBER,
    EXPR_VARIABLE,
    EXPR_OPERATOR,
    EXPR_BRACKET,
    EXPR_FUNCTION,
    EXPR_COMMA,
    EXPR_BOOLEAN
} expr_kind;

typedef struct expr_token {
    /* For names and numbers this points into the expression text (which the
     * caller keeps alive, and tc_expression_parse copies); for operators and
     * functions it points at a static table entry. */
    const char *value;
    size_t value_len;
    expr_kind kind;
    double number;
    int para_count;
} expr_token;

/* A compiled expression: the postfix program plus the expression text the
 * variable tokens point into. */
struct tc_expression {
    expr_token *nodes;
    size_t count;
    char *text;
};

/* The tokens the reference's OperatorTokens string contains. Order does not
 * matter; the lookup is linear. */
static const char *const expr_operator_tokens[] = {
    "(", ")", "+", "-", "*", "/", "%", "^", ",", ">", "<", "<=", ">=", "and", "or", "xor",
};

static const struct {
    const char *name;
    int para_count;
} expr_functions[] = {
    {"abs", 1},  {"acos", 1}, {"asin", 1},  {"atan", 1},  {"atan2", 2},
    {"cos", 1},  {"sin", 1},  {"tan", 1},   {"cosh", 1},  {"sinh", 1},
    {"tanh", 1}, {"exp", 1},  {"log", 1},   {"log10", 1}, {"sqrt", 1},
    {"ceil", 1}, {"floor", 1}, {"rand", 0}, {"dup", 0},   {"int", 1},
    {"sign", 1}, {"pow", 2},  {"max", 2},   {"min", 2},
};

static const struct {
    const char *name;
    double value;
} expr_defines[] = {
    {"M_E", 2.71828182845904523536},        {"M_LOG2E", 1.44269504088896340736},
    {"M_LOG10E", 0.43429448190325182765},   {"M_LN2", 0.69314718055994530942},
    {"M_LN10", 2.30258509299404568402},     {"M_PI", 3.14159265358979323846},
    {"M_PI_2", 1.57079632679489661923},     {"M_PI_4", 0.78539816339744830962},
    {"M_1_PI", 0.31830988618379067154},     {"M_2_PI", 0.63661977236758134308},
    {"M_2_SQRTPI", 1.12837916709551257390}, {"M_SQRT2", 1.41421356237309504880},
    {"M_SQRT1_2", 0.70710678118654752440},
};

/* ------------------------------------------------------------------ */
/* Character classes                                                  */
/* ------------------------------------------------------------------ */

static int expr_is_digit(char c) {
    return (c >= '0' && c <= '9') || c == '.';
}

static int expr_is_alpha(char c) {
    return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_';
}

static int expr_is_space(char c) {
    return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r';
}

static int expr_is_single_operator(char c) {
    return c == '(' || c == ')' || c == '+' || c == '-' || c == '*' || c == '/' ||
           c == '%' || c == '^' || c == ',' || c == '>' || c == '<';
}

/* Compares a token's name against a literal. Names taken from the expression
 * text carry a length; table entries are NUL-terminated and compare whole.
 * Tokens that name an operator, function or constant always point at a static
 * table entry, so an exact strcmp is correct for them. */
static int expr_token_eq(const expr_token *t, const char *name) {
    size_t n;
    if (t->value == NULL) {
        return 0;
    }
    if (t->value_len == 0) {
        return strcmp(t->value, name) == 0;
    }
    n = strlen(name);
    return n == t->value_len && memcmp(t->value, name, n) == 0;
}

static int expr_token_is(const expr_token *t, const char *name) {
    return t->value != NULL && t->value_len == 0 && strcmp(t->value, name) == 0;
}

/* ------------------------------------------------------------------ */
/* Operator stack                                                     */
/* ------------------------------------------------------------------ */

/* The stacks are fixed arrays: the reference uses System.Collections.Stack,
 * which is unbounded, but a chapter expression is a configuration string, not
 * input data, and an expression nested past 256 levels is a typo rather than a
 * use case. Overflowing is reported as an error instead of being truncated. */
#define EXPR_MAX_STACK 256
#define EXPR_MAX_NODES 4096

typedef struct expr_stack {
    expr_token items[EXPR_MAX_STACK];
    int top; /* number of items */
} expr_stack;

static int expr_stack_push(expr_stack *s, expr_token t) {
    if (s->top >= EXPR_MAX_STACK) {
        return 0;
    }
    s->items[s->top++] = t;
    return 1;
}

static expr_token expr_stack_pop(expr_stack *s) {
    /* Only called when the stack is known non-empty. */
    return s->items[--s->top];
}

static expr_token *expr_stack_peek(expr_stack *s) {
    return &s->items[s->top - 1];
}

static expr_token expr_token_blank(void) {
    expr_token t;
    t.value = "";
    t.value_len = 0;
    t.kind = EXPR_BLANK;
    t.number = 0.0;
    t.para_count = 0;
    return t;
}

static expr_token expr_token_number(double v) {
    expr_token t = expr_token_blank();
    t.kind = EXPR_NUMBER;
    t.number = v;
    return t;
}

/* ------------------------------------------------------------------ */
/* Tokeniser                                                          */
/* ------------------------------------------------------------------ */

/* System.Decimal's maximum magnitude is 2^96 - 1 and its smallest non-zero
 * magnitude is 1e-28. A value above the maximum makes the reference throw,
 * which its Eval turns into a fallback to `time`; a value below the minimum is
 * rounded to zero instead (decimal does not throw for that).
 *
 * 2^96 - 1 and 2^96 both round to the same double, so the exact maximum cannot
 * be told apart from the first overflowing value. A literal that lands there
 * (79228162514264337593543950335) is accepted, while a computed result that
 * lands there is rejected: the ambiguity only matters for 29-significant-digit
 * values, which no chapter expression produces from a real timestamp. */
#define EXPR_DECIMAL_2_96 79228162514264337593543950336.0

static int expr_decimal_overflow(double v) {
    return fabs(v) > EXPR_DECIMAL_2_96;
}

/* Applies the range rules to a computed result. */
static int expr_decimal_fixup(double *v) {
    if (fabs(*v) >= EXPR_DECIMAL_2_96) {
        return 0; /* overflow: the reference throws */
    }
    /* Decimal rounds to the nearest value it can hold, so anything below half
     * of its smallest magnitude (1e-28) becomes zero. The threshold is the
     * midpoint, not 1e-28 itself, because the double nearest 1e-28 can sit one
     * ULP below it. */
    if (*v != 0.0 && fabs(*v) < 5e-29) {
        *v = 0.0;
    }
    return 1;
}

/* Parses a double the way decimal.Parse does for the shapes the tokeniser can
 * produce. The tokeniser only ever yields a string of digits with at most one
 * '.', so strtod is exact here apart from the ordinary binary rounding of the
 * decimal literal. Returns 0 when the text is not a number, or when the value
 * is outside decimal's range (decimal.TryParse also fails there, and the
 * reference reports it as an invalid number token). */
static int expr_parse_number(const char *text, size_t len, double *out) {
    size_t i;
    int seen_digit = 0;
    int seen_dot = 0;

    for (i = 0; i < len; i++) {
        if (text[i] >= '0' && text[i] <= '9') {
            seen_digit = 1;
        } else if (text[i] == '.') {
            if (seen_dot) {
                return 0; /* "1.2.3": decimal.Parse rejects it */
            }
            seen_dot = 1;
        } else {
            return 0;
        }
    }
    if (!seen_digit) {
        return 0; /* "." alone */
    }
    {
        /* strtod needs a NUL-terminated buffer, and the token may be a slice of
         * a larger expression, so copy it out. The buffer is sized for the
         * longest token a 28-digit decimal can produce. */
        char tmp[128];
        if (len >= sizeof(tmp)) {
            return 0;
        }
        memcpy(tmp, text, len);
        tmp[len] = '\0';
        *out = strtod(tmp, NULL);
    }
    if (expr_decimal_overflow(*out)) {
        return 0; /* decimal.TryParse fails: an invalid number token */
    }
    if (*out != 0.0 && fabs(*out) < 5e-29) {
        *out = 0.0; /* decimal rounds a sub-scale literal to zero */
    }
    return 1;
}

/* Reads one token starting at *pos and advances *pos past it. Mirrors
 * Expression.GetToken. Returns 0 on a malformed number. */
static int expr_next_token(const char *expr, size_t len, size_t *pos, expr_token *out) {
    size_t tok_start = 0;
    size_t n = 0;
    size_t i = *pos;

    *out = expr_token_blank();

    for (; i < len; i++) {
        char c = expr[i];

        if (expr_is_space(c)) {
            if (n != 0) {
                break;
            }
            continue;
        }
        if (expr_is_digit(c) || expr_is_alpha(c)) {
            if (n == 0) {
                tok_start = i;
            }
            n++;
            continue;
        }
        if (n != 0) {
            break;
        }
        if (!expr_is_single_operator(c)) {
            /* The reference skips unknown characters such as '!' outright. */
            continue;
        }

        *pos = i + 1;
        if (*pos < len && expr[*pos] == '=' && (c == '>' || c == '<')) {
            (*pos)++;
            out->value = (c == '>') ? ">=" : "<=";
            out->kind = EXPR_OPERATOR;
            out->para_count = 2;
            return 1;
        }
        if (c == '(' || c == ')') {
            out->value = (c == '(') ? "(" : ")";
            out->kind = EXPR_BRACKET;
            return 1;
        }
        if (c == ',') {
            out->value = ",";
            out->kind = EXPR_COMMA;
            return 1;
        }
        switch (c) {
        case '+': out->value = "+"; break;
        case '-': out->value = "-"; break;
        case '*': out->value = "*"; break;
        case '/': out->value = "/"; break;
        case '%': out->value = "%"; break;
        case '^': out->value = "^"; break;
        case '>': out->value = ">"; break;
        case '<': out->value = "<"; break;
        default: return 0; /* unreachable: expr_is_single_operator was checked */
        }
        out->kind = EXPR_OPERATOR;
        out->para_count = 2;
        return 1;
    }

    /* The scan ran out of input. The reference writes the collected text back
     * through `pos` and classifies it, or reads varRet[0] and throws when
     * nothing was collected. */
    *pos = i;
    if (n == 0) {
        return 0;
    }
    out->value = expr + tok_start;
    out->value_len = n;

    if (expr_is_digit(expr[tok_start])) {
        double v = 0.0;
        if (!expr_parse_number(out->value, n, &v)) {
            return 0;
        }
        out->kind = EXPR_NUMBER;
        out->number = v;
        return 1;
    }
    /* Function names, the word operators and the math constants are all
     * alphanumeric, so a length check keeps strncmp from matching a prefix of
     * a longer name. */
    for (i = 0; i < sizeof(expr_functions) / sizeof(expr_functions[0]); i++) {
        if (expr_token_eq(out, expr_functions[i].name)) {
            out->value = expr_functions[i].name;
            out->value_len = 0;
            out->kind = EXPR_FUNCTION;
            out->para_count = expr_functions[i].para_count;
            return 1;
        }
    }
    for (i = 0; i < sizeof(expr_operator_tokens) / sizeof(expr_operator_tokens[0]); i++) {
        if (expr_token_eq(out, expr_operator_tokens[i])) {
            out->value = expr_operator_tokens[i];
            out->value_len = 0;
            out->kind = EXPR_OPERATOR;
            out->para_count = 2;
            return 1;
        }
    }
    for (i = 0; i < sizeof(expr_defines) / sizeof(expr_defines[0]); i++) {
        if (expr_token_eq(out, expr_defines[i].name)) {
            out->kind = EXPR_NUMBER;
            out->number = expr_defines[i].value;
            out->value = expr_defines[i].name;
            out->value_len = 0;
            return 1;
        }
    }
    /* An unknown name stays a variable; it fails at evaluation time, exactly
     * like the reference's dictionary lookup. */
    out->kind = EXPR_VARIABLE;
    return 1;
}

/* ------------------------------------------------------------------ */
/* Shunting-yard                                                      */
/* ------------------------------------------------------------------ */

/* Expression.GetPriority. The reference builds a fresh Dictionary per call and
 * throws for anything not listed; here the caller has already restricted the
 * operators to that set, so the lookup cannot fail. */
static int expr_priority(const expr_token *t, int *out) {
    if (t->value == NULL || t->kind == EXPR_BLANK) {
        *out = -2;
        return 1;
    }
    if (expr_token_is(t, ">") || expr_token_is(t, "<") || expr_token_is(t, ">=") ||
        expr_token_is(t, "<=")) {
        *out = -1;
        return 1;
    }
    if (expr_token_is(t, "+") || expr_token_is(t, "-")) {
        *out = 0;
        return 1;
    }
    if (expr_token_is(t, "*") || expr_token_is(t, "/") || expr_token_is(t, "%")) {
        *out = 1;
        return 1;
    }
    if (expr_token_is(t, "^")) {
        *out = 2;
        return 1;
    }
    /* `and`, `or`, `xor` are not in the precedence table; the reference throws
     * "Invalid operator" when one reaches this point. */
    return 0;
}

/* Compiles the infix text into postfix order in `nodes`, which is the
 * reference's retStack in push order (oldest first). Returns 0 on any
 * malformed input. */
static int expr_compile(const char *expr, expr_token *nodes, size_t *node_count) {
    expr_stack stack;
    expr_stack func_stack;
    expr_token pre_token;
    size_t pos = 0;
    size_t len = strlen(expr);
    int comment = 0;

    stack.top = 0;
    func_stack.top = 0;
    pre_token = expr_token_blank();
    *node_count = 0;

    if (!expr_stack_push(&stack, expr_token_blank())) {
        return 0; /* Token.End */
    }

    while (pos < len && !comment) {
        expr_token token;
        expr_token *last;

        if (!expr_next_token(expr, len, &pos, &token)) {
            return 0;
        }

        switch (token.kind) {
        case EXPR_FUNCTION:
            if (!expr_stack_push(&func_stack, token)) {
                return 0;
            }
            break;

        case EXPR_COMMA:
            /* The reference loops until it sees "("; an unmatched ',' walks off
             * the stack and throws, which is the failure reported here. */
            while (stack.top > 0 && !expr_token_is(expr_stack_peek(&stack), "(")) {
                if (*node_count >= EXPR_MAX_NODES) {
                    return 0;
                }
                nodes[(*node_count)++] = expr_stack_pop(&stack);
            }
            if (stack.top == 0) {
                return 0;
            }
            break;

        case EXPR_BRACKET:
            if (expr_token_is(&token, "(")) {
                if (!expr_stack_push(&stack, token)) {
                    return 0;
                }
            } else {
                while (stack.top > 0 && !expr_token_is(expr_stack_peek(&stack), "(")) {
                    if (*node_count >= EXPR_MAX_NODES) {
                        return 0;
                    }
                    nodes[(*node_count)++] = expr_stack_pop(&stack);
                }
                if (stack.top == 0) {
                    return 0; /* unbalanced ')' */
                }
                (void)expr_stack_pop(&stack); /* the "(" */
                if (func_stack.top != 0) {
                    if (*node_count >= EXPR_MAX_NODES) {
                        return 0;
                    }
                    nodes[(*node_count)++] = expr_stack_pop(&func_stack);
                }
            }
            pre_token = token;
            break;

        case EXPR_OPERATOR:
            last = expr_stack_peek(&stack);
            if (last->kind == EXPR_BLANK || last->kind == EXPR_BRACKET) {
                /* Unary minus: only valid directly after '('. The reference
                 * pushes a synthetic zero and lets the binary '-' negate it, so
                 * a leading "-5" (preToken is Token.End, not "(") is left
                 * without a left operand and fails at evaluation. */
                if (expr_token_is(&pre_token, "(") && expr_token_is(&token, "-")) {
                    if (*node_count >= EXPR_MAX_NODES) {
                        return 0;
                    }
                    nodes[(*node_count)++] = expr_token_number(0.0);
                }
                if (!expr_stack_push(&stack, token)) {
                    return 0;
                }
            } else if (last->kind == EXPR_OPERATOR) {
                int p_last = 0;
                int p_token = 0;

                /* "//" starts a comment: the reference pops the first '/' and
                 * stops scanning. */
                if (expr_token_is(&token, "/") && expr_token_is(&pre_token, "/")) {
                    (void)expr_stack_pop(&stack);
                    comment = 1;
                    break;
                }
                if (expr_token_is(&token, "-") && pre_token.kind == EXPR_OPERATOR) {
                    if (*node_count >= EXPR_MAX_NODES) {
                        return 0;
                    }
                    nodes[(*node_count)++] = expr_token_number(0.0);
                } else {
                    while (last->kind != EXPR_BRACKET) {
                        if (!expr_priority(last, &p_last) || !expr_priority(&token, &p_token)) {
                            return 0;
                        }
                        if (p_last < p_token) {
                            break;
                        }
                        if (*node_count >= EXPR_MAX_NODES) {
                            return 0;
                        }
                        nodes[(*node_count)++] = expr_stack_pop(&stack);
                        last = expr_stack_peek(&stack);
                    }
                }
                if (!expr_stack_push(&stack, token)) {
                    return 0;
                }
            } else {
                return 0; /* Unexpected token type */
            }
            pre_token = token;
            break;

        default:
            pre_token = token;
            if (*node_count >= EXPR_MAX_NODES) {
                return 0;
            }
            nodes[(*node_count)++] = token;
            break;
        }
    }

    /* Drain the operators; the sentinel and any unclosed '(' are left behind,
     * which is what makes a lone "(" a valid expression. */
    while (stack.top > 0 && expr_stack_peek(&stack)->kind != EXPR_BLANK) {
        if (*node_count >= EXPR_MAX_NODES) {
            return 0;
        }
        nodes[(*node_count)++] = expr_stack_pop(&stack);
    }
    return 1;
}

/* ------------------------------------------------------------------ */
/* Evaluation                                                         */
/* ------------------------------------------------------------------ */

/* Applies a one-argument function. Returns 0 when the operation is undefined
 * for the operand, which is the case the reference reports by throwing. Every
 * result is brought back into System.Decimal's range by expr_decimal_fixup. */
static int expr_apply_fn1(const char *name, double x, double *out) {
    if (strcmp(name, "abs") == 0) {
        *out = fabs(x);
    } else if (strcmp(name, "acos") == 0) {
        if (x < -1.0 || x > 1.0) {
            return 0;
        }
        *out = acos(x);
    } else if (strcmp(name, "asin") == 0) {
        if (x < -1.0 || x > 1.0) {
            return 0;
        }
        *out = asin(x);
    } else if (strcmp(name, "atan") == 0) {
        *out = atan(x);
    } else if (strcmp(name, "cos") == 0) {
        *out = cos(x);
    } else if (strcmp(name, "sin") == 0) {
        *out = sin(x);
    } else if (strcmp(name, "tan") == 0) {
        *out = tan(x);
    } else if (strcmp(name, "cosh") == 0) {
        *out = cosh(x);
    } else if (strcmp(name, "sinh") == 0) {
        *out = sinh(x);
    } else if (strcmp(name, "tanh") == 0) {
        *out = tanh(x);
    } else if (strcmp(name, "exp") == 0) {
        *out = exp(x);
    } else if (strcmp(name, "log") == 0) {
        if (x <= 0.0) {
            return 0;
        }
        *out = log(x);
    } else if (strcmp(name, "log10") == 0) {
        if (x <= 0.0) {
            return 0;
        }
        *out = log10(x);
    } else if (strcmp(name, "sqrt") == 0) {
        if (x < 0.0) {
            return 0;
        }
        *out = sqrt(x);
    } else if (strcmp(name, "ceil") == 0) {
        *out = ceil(x);
    } else if (strcmp(name, "floor") == 0) {
        *out = floor(x);
    } else if (strcmp(name, "int") == 0) {
        *out = trunc(x);
    } else if (strcmp(name, "sign") == 0) {
        *out = (x > 0.0) ? 1.0 : ((x < 0.0) ? -1.0 : 0.0);
    } else {
        return 0;
    }
    return expr_decimal_fixup(out);
}

static int expr_apply_fn2(const char *name, double a, double b, double *out, int *boolean) {
    if (strcmp(name, "pow") == 0 || strcmp(name, "^") == 0) {
        *out = pow(a, b);
    } else if (strcmp(name, "max") == 0) {
        *out = (a > b) ? a : b;
    } else if (strcmp(name, "min") == 0) {
        *out = (a < b) ? a : b;
    } else if (strcmp(name, "atan2") == 0) {
        *out = atan2(a, b);
    } else if (strcmp(name, "+") == 0) {
        *out = a + b;
    } else if (strcmp(name, "-") == 0) {
        *out = a - b;
    } else if (strcmp(name, "*") == 0) {
        *out = a * b;
    } else if (strcmp(name, "/") == 0) {
        if (b == 0.0) {
            return 0;
        }
        *out = a / b;
    } else if (strcmp(name, "%") == 0) {
        if (b == 0.0) {
            return 0;
        }
        *out = fmod(a, b);
    } else if (strcmp(name, ">") == 0) {
        *out = (a > b) ? 1.0 : 0.0;
        *boolean = 1;
    } else if (strcmp(name, "<") == 0) {
        *out = (a < b) ? 1.0 : 0.0;
        *boolean = 1;
    } else if (strcmp(name, ">=") == 0) {
        *out = (a >= b) ? 1.0 : 0.0;
        *boolean = 1;
    } else if (strcmp(name, "<=") == 0) {
        *out = (a <= b) ? 1.0 : 0.0;
        *boolean = 1;
    } else if (strcmp(name, "and") == 0) {
        *out = (a != 0.0 && b != 0.0) ? 1.0 : 0.0;
        *boolean = 1;
    } else if (strcmp(name, "or") == 0) {
        *out = (a != 0.0 || b != 0.0) ? 1.0 : 0.0;
        *boolean = 1;
    } else if (strcmp(name, "xor") == 0) {
        /* Reference bug, reproduced: `var t1 = value.Number != 0; var t2 =
         * value.Number != 0;` reads the left operand twice, so t1 ^ t2 is
         * always false and xor always evaluates to 0. */
        int t1 = (a != 0.0);
        int t2 = (a != 0.0);
        *out = (t1 != t2) ? 1.0 : 0.0;
        *boolean = 1;
    } else {
        return 0;
    }
    return expr_decimal_fixup(out);
}

static int expr_apply_operator(const expr_token *t, double a, double b, double *out) {
    int boolean = 0;
    if (t->para_count == 2) {
        return expr_apply_fn2(t->value, a, b, out, &boolean);
    }
    return expr_apply_fn1(t->value, b, out);
}

/* Runs the postfix program. Returns 0 for any failure the reference reports by
 * throwing: a missing variable, an unbalanced program, an undefined operation. */
static int expr_run(const expr_token *nodes, size_t count, double t, double fps, int has_fps,
                    double *out) {
    expr_stack stack;
    size_t i;

    stack.top = 0;
    /* The reference iterates `postfix.Reverse()`. Its postfix is a Stack,
     * which enumerates top-first, so Reverse() yields push order: the first
     * token popped during compilation is evaluated first. */
    for (i = 0; i < count; i++) {
        const expr_token *token = &nodes[i];
        switch (token->kind) {
        case EXPR_NUMBER:
            if (!expr_stack_push(&stack, *token)) {
                return 0;
            }
            break;

        case EXPR_VARIABLE:
            if (expr_token_eq(token, "t")) {
                if (!expr_stack_push(&stack, expr_token_number(t))) {
                    return 0;
                }
            } else if (expr_token_eq(token, "fps")) {
                if (!has_fps) {
                    return 0; /* fps < 1e-5 leaves it unbound */
                }
                if (!expr_stack_push(&stack, expr_token_number(fps))) {
                    return 0;
                }
            } else {
                return 0; /* unknown variable */
            }
            break;

        case EXPR_OPERATOR: {
            expr_token rhs;
            expr_token lhs;
            double v = 0.0;
            if (stack.top < 2) {
                return 0;
            }
            rhs = expr_stack_pop(&stack);
            lhs = expr_stack_pop(&stack);
            if (!expr_apply_operator(token, lhs.number, rhs.number, &v)) {
                return 0;
            }
            if (!expr_stack_push(&stack, expr_token_number(v))) {
                return 0;
            }
            break;
        }

        case EXPR_FUNCTION:
            if (token->para_count == 0) {
                if (strcmp(token->value, "dup") == 0) {
                    /* The reference pushes stack.Peek() without popping, which
                     * duplicates the top item (or throws when empty). */
                    if (stack.top < 1 || !expr_stack_push(&stack, *expr_stack_peek(&stack))) {
                        return 0;
                    }
                } else if (strcmp(token->value, "rand") == 0) {
                    /* Non-deterministic in the reference too; 0.5 keeps the
                     * port reproducible while still being a valid number. */
                    if (!expr_stack_push(&stack, expr_token_number(0.5))) {
                        return 0;
                    }
                } else {
                    return 0;
                }
            } else if (token->para_count == 1) {
                double v = 0.0;
                if (stack.top < 1) {
                    return 0;
                }
                {
                    expr_token para = expr_stack_pop(&stack);
                    if (!expr_apply_fn1(token->value, para.number, &v)) {
                        return 0;
                    }
                }
                if (!expr_stack_push(&stack, expr_token_number(v))) {
                    return 0;
                }
            } else if (token->para_count == 2) {
                expr_token rhs;
                expr_token lhs;
                double v = 0.0;
                int boolean = 0;
                if (stack.top < 2) {
                    return 0;
                }
                rhs = expr_stack_pop(&stack);
                lhs = expr_stack_pop(&stack);
                if (!expr_apply_fn2(token->value, lhs.number, rhs.number, &v, &boolean)) {
                    return 0;
                }
                if (!expr_stack_push(&stack, expr_token_number(v))) {
                    return 0;
                }
            } else {
                return 0;
            }
            break;

        default:
            /* The reference's Eval switch has no case for Bracket (or Comma),
             * so such a token is silently skipped. The only one that can
             * appear is a leftover '(' from the drain loop. */
            break;
        }
    }

    if (stack.top < 1) {
        return 0; /* empty expression, or an unbalanced program */
    }
    *out = expr_stack_peek(&stack)->number;
    return 1;
}

/* ------------------------------------------------------------------ */
/* Public entry points                                                */
/* ------------------------------------------------------------------ */

tc_status tc_expression_eval(const char *expr, double time, double fps, double *out) {
    expr_token *nodes;
    size_t count = 0;
    int has_fps;
    int ok;

    if (out == NULL) {
        return tc_fail(TC_E_INVALID, "expression: out must not be NULL");
    }
    *out = time;

    if (expr == NULL) {
        return tc_fail(TC_E_INVALID, "expression: expr must not be NULL");
    }
    if (fps != fps) {
        return tc_fail(TC_E_INVALID, "expression: fps must not be NaN");
    }

    nodes = tc_malloc(sizeof(*nodes) * EXPR_MAX_NODES);
    if (nodes == NULL) {
        return tc_fail(TC_E_NOMEM, "expression: out of memory");
    }
    if (!expr_compile(expr, nodes, &count)) {
        free(nodes);
        return tc_fail(TC_E_FORMAT, "expression: malformed expression");
    }

    /* Expression.Eval(time, fps) leaves `fps` unbound below 1e-5. */
    has_fps = (fps >= 1e-5) ? 1 : 0;
    ok = expr_run(nodes, count, time, fps, has_fps, out);
    free(nodes);
    if (!ok) {
        /* The reference logs and falls back to `time`, which *out already
         * holds. Report the fallback through the error message but keep the
         * status non-fatal so callers can use the value either way. */
        tc_set_error("expression: evaluation failed, using time");
        return TC_E_FORMAT;
    }
    return TC_OK;
}

int tc_expression_parse(const char *expr, tc_expression **out) {
    expr_token *nodes;
    tc_expression *e;
    size_t count = 0;
    size_t i;

    if (out == NULL) {
        return 0;
    }
    *out = NULL;
    if (expr == NULL) {
        return 0;
    }

    nodes = tc_malloc(sizeof(*nodes) * EXPR_MAX_NODES);
    if (nodes == NULL) {
        return 0;
    }
    if (!expr_compile(expr, nodes, &count)) {
        free(nodes);
        return 0;
    }

    e = tc_calloc(1, sizeof(*e));
    if (e == NULL) {
        free(nodes);
        return 0;
    }
    /* Shrink to the exact size so a compiled expression stays small. */
    if (count > 0) {
        expr_token *tight = tc_malloc(sizeof(*tight) * count);
        if (tight == NULL) {
            free(nodes);
            free(e);
            return 0;
        }
        memcpy(tight, nodes, sizeof(*tight) * count);
        free(nodes);
        e->nodes = tight;
    } else {
        free(nodes);
        e->nodes = NULL;
    }
    e->count = count;

    /* Tokens that name a variable point into `expr`, which the caller is free
     * to release. Rebind them to a private copy of the expression text. */
    e->text = tc_strdup(expr);
    if (e->text == NULL) {
        free(e->nodes);
        free(e);
        return 0;
    }
    for (i = 0; i < count; i++) {
        if (e->nodes[i].value_len != 0) {
            size_t offset = (size_t)(e->nodes[i].value - expr);
            e->nodes[i].value = e->text + offset;
        }
    }

    *out = e;
    return 1;
}

tc_status tc_expression_eval_compiled(const tc_expression *expr, double time, double fps,
                                      double *out) {
    int has_fps;
    int ok;

    if (out == NULL) {
        return tc_fail(TC_E_INVALID, "expression: out must not be NULL");
    }
    *out = time;
    if (expr == NULL) {
        return tc_fail(TC_E_INVALID, "expression: expr must not be NULL");
    }
    if (fps != fps) {
        return tc_fail(TC_E_INVALID, "expression: fps must not be NaN");
    }

    has_fps = (fps >= 1e-5) ? 1 : 0;
    ok = expr_run(expr->nodes, expr->count, time, fps, has_fps, out);
    if (!ok) {
        tc_set_error("expression: evaluation failed, using time");
        return TC_E_FORMAT;
    }
    return TC_OK;
}

void tc_expression_free(tc_expression *expr) {
    if (expr != NULL) {
        free(expr->nodes);
        free(expr->text);
        free(expr);
    }
}

/* ------------------------------------------------------------------ */
/* Number formatting                                                  */
/* ------------------------------------------------------------------ */

/* Wide enough for the 309 integer digits of a double near DBL_MAX plus the
 * fraction and terminator. */
#define EXPR_FMT_MAX 576

/* Shortest fixed-point representation that reads back as `v`. Fixed notation
 * (rather than "%.17g") is required because the rounding below is decimal:
 * System.Decimal remembers the decimal literal it parsed, so "0.015" is
 * rounded away from zero while the double nearest 0.015 would not be. The
 * shortest round-tripping form recovers that literal for every value an
 * expression produces. */
static void expr_format_shortest(double v, char *buf, size_t buflen) {
    int prec;

    /* Doubles at or above 2^53 are integers, so one pass is exact and avoids
     * the fractional search. */
    if (v >= 9007199254740992.0) {
        snprintf(buf, buflen, "%.0f", v);
        return;
    }
    for (prec = 0; prec <= 17; prec++) {
        snprintf(buf, buflen, "%.*f", prec, v);
        if (strtod(buf, NULL) == v) {
            return;
        }
    }
    snprintf(buf, buflen, "%.17g", v);
}

/* Rounds the fixed-point magnitude in `digits` to `decimals` places, halves
 * away from zero, and writes the result to `out` without a sign. */
static void expr_round_digits(const char *digits, int decimals, char *out, size_t outlen) {
    char ip[EXPR_FMT_MAX];
    char fp[EXPR_FMT_MAX];
    char all[EXPR_FMT_MAX];
    size_t ip_len = 0;
    size_t fp_len = 0;
    size_t keep_len = (size_t)decimals;
    size_t all_len = 0;
    size_t i;
    size_t pos = 0;
    const char *p;
    int carry;

    for (p = digits; *p != '\0' && *p != '.'; p++) {
        ip[ip_len++] = *p;
    }
    if (*p == '.') {
        p++;
    }
    for (; *p != '\0'; p++) {
        fp[fp_len++] = *p;
    }
    /* Pad so the rounding digit and every kept digit exist. */
    while (fp_len < keep_len + 1) {
        fp[fp_len++] = '0';
    }

    if (fp[keep_len] >= '5') {
        for (i = 0; i < ip_len; i++) {
            all[all_len++] = ip[i];
        }
        for (i = 0; i < keep_len; i++) {
            all[all_len++] = fp[i];
        }
        carry = 1;
        for (i = all_len; i-- > 0 && carry;) {
            if (all[i] == '9') {
                all[i] = '0';
            } else {
                all[i]++;
                carry = 0;
            }
        }
        if (carry) {
            /* "9.99" -> "10.00": shift right and prepend a 1. */
            for (i = all_len; i-- > 0;) {
                all[i + 1] = all[i];
            }
            all[0] = '1';
            all_len++;
        }
        for (i = 0; i < keep_len; i++) {
            fp[i] = all[all_len - keep_len + i];
        }
        ip_len = all_len - keep_len;
        for (i = 0; i < ip_len; i++) {
            ip[i] = all[i];
        }
    }

    /* Strip leading zeros, keeping at least one integer digit. */
    for (i = 0; i < ip_len && ip[i] == '0'; i++) {
    }
    if (i == ip_len) {
        if (pos + 1 < outlen) {
            out[pos++] = '0';
        }
    } else {
        for (; i < ip_len && pos + 1 < outlen; i++) {
            out[pos++] = ip[i];
        }
    }
    if (decimals > 0) {
        if (pos + 1 < outlen) {
            out[pos++] = '.';
        }
        for (i = 0; i < keep_len && pos + 1 < outlen; i++) {
            out[pos++] = fp[i];
        }
    }
    out[pos] = '\0';
}

void tc_expression_format(double v, char *buf, size_t buflen) {
    char shortest[EXPR_FMT_MAX];
    char rounded[EXPR_FMT_MAX];
    int negative;

    if (buf == NULL || buflen == 0) {
        return;
    }
    if (v != v) {
        snprintf(buf, buflen, "NaN");
        return;
    }
    if (v > DBL_MAX) {
        snprintf(buf, buflen, "Infinity");
        return;
    }
    if (v < -DBL_MAX) {
        snprintf(buf, buflen, "-Infinity");
        return;
    }

    /* Anything below half a unit in the last place rounds to zero, and asking
     * for a shortest round-tripping fixed-point form of such a value would need
     * more fractional digits than a double can carry. */
    if (fabs(v) < 0.005) {
        snprintf(buf, buflen, "0.00");
        return;
    }

    /* decimal has no negative zero, so "-0.00" never appears in the reference
     * output; the sign is applied after rounding, which drops it for values
     * that round to zero. fabs also strips the sign of -0.0 itself. */
    negative = (v < 0.0);
    expr_format_shortest(fabs(v), shortest, sizeof(shortest));
    expr_round_digits(shortest, 2, rounded, sizeof(rounded));
    if (negative && strcmp(rounded, "0.00") != 0) {
        snprintf(buf, buflen, "-%s", rounded);
    } else {
        snprintf(buf, buflen, "%s", rounded);
    }
}

/* ------------------------------------------------------------------ */
/* Chapter names                                                      */
/* ------------------------------------------------------------------ */

tc_status tc_chapter_name(char *buf, size_t buflen, const char *format, int index) {
    int written;

    if (buf == NULL || buflen == 0) {
        return tc_fail(TC_E_INVALID, "chapter name: buf must not be NULL");
    }
    if (index < -999999) {
        index = -999999;
    }
    if (index > 999999) {
        index = 999999;
    }
    if (format == NULL) {
        format = "Chapter";
    }
    /* {format} {index:D2}: "D2" is a minimum width, so 100 keeps three digits
     * and -1 becomes "-01". */
    written = snprintf(buf, buflen, "%s %s%02d", format, index < 0 ? "-" : "",
                       index < 0 ? -index : index);
    if (written < 0) {
        return tc_fail(TC_E_FORMAT, "chapter name: formatting failed");
    }
    if ((size_t)written >= buflen) {
        return tc_fail(TC_E_RANGE, "chapter name: buffer too small");
    }
    return TC_OK;
}

tc_status tc_chapter_name_range(char *buf, size_t buflen, const char *format, int start,
                                int count) {
    int max;
    int i;
    size_t used = 0;

    if (start < 0 || start > 99) {
        return tc_fail(TC_E_RANGE, "chapter name range: start must be 0..99");
    }
    if (count < 0) {
        return tc_fail(TC_E_RANGE, "chapter name range: count must not be negative");
    }
    max = start + count - 1;
    if (max > 99) {
        return tc_fail(TC_E_RANGE, "chapter name range: start + count - 1 must be <= 99");
    }

    if (buf == NULL || buflen == 0) {
        return tc_fail(TC_E_INVALID, "chapter name range: buf must not be NULL");
    }
    buf[0] = '\0';

    for (i = 0; i < count; i++) {
        char name[128];
        int written;
        tc_status st = tc_chapter_name(name, sizeof(name), format, start + i);
        if (st != TC_OK) {
            return st;
        }
        written = snprintf(buf + used, buflen - used, "%s%s", i == 0 ? "" : "\n", name);
        if (written < 0) {
            return tc_fail(TC_E_FORMAT, "chapter name range: formatting failed");
        }
        if ((size_t)written >= buflen - used) {
            return tc_fail(TC_E_RANGE, "chapter name range: buffer too small");
        }
        used += (size_t)written;
    }
    return TC_OK;
}
