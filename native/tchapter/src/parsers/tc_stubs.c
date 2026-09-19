/*
 * Not-yet-implemented parser stubs.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Every parser that has not been written yet lives here as a single stub, so the
 * library links and the CLI reports a clear "not implemented" error instead of
 * failing to build. Each parser package replaces exactly one function below with
 * a real implementation in its own file, which keeps parallel work free of
 * merge conflicts.
 *
 * When a parser lands, delete its stub from this file and add the new
 * translation unit to the Makefile's source list (the wildcard picks up
 * the parser sources automatically).
 */
#include <stddef.h>

#include "tc_internal.h"

