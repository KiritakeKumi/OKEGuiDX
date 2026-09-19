# libtchapter build.
#
# The library is plain C11 with no dependencies beyond libc, so the default
# configuration works on every target including riscv64. Cross-compiling is a
# matter of passing CC and a sysroot; no configure step is required.
#
# SPDX-License-Identifier: GPL-3.0-or-later

CC      ?= cc
AR      ?= ar
CFLAGS  ?= -O2 -g
CSTD    := -std=c11

WARNINGS := -Wall -Wextra -Wshadow -Wconversion -Wsign-conversion \
            -Wpointer-arith -Wcast-qual -Wstrict-prototypes \
            -Wmissing-prototypes -Wvla -Wundef

SRCDIR   := src
OBJDIR   := build
INCDIR   := include

# Parser implementations live in src/parsers/ so that each parser package adds
# exactly one file and the source list never has to be edited by hand.
SRCS := $(wildcard $(SRCDIR)/*.c) $(wildcard $(SRCDIR)/parsers/*.c)
# The test runner has its own main(); it is linked into the test binary only.
# The library excludes every test translation unit. Filtering out only the
# runner would leave the suites themselves linked into libtchapter.a, so anyone
# linking the library would also get the test code and its registration
# constructors.
LIB_SRCS := $(filter-out $(SRCDIR)/tc_test_main.c,$(filter-out $(wildcard $(SRCDIR)/tc_test_*.c),$(SRCS)))
LIB_OBJS := $(patsubst $(SRCDIR)/%.c,$(OBJDIR)/%.o,$(LIB_SRCS))
# The main file is listed separately because the wildcard below also matches it,
# which would link two definitions of main.
TEST_SRCS := $(wildcard $(SRCDIR)/tc_test_main.c) \
             $(filter-out $(SRCDIR)/tc_test_main.c,$(wildcard $(SRCDIR)/tc_test_*.c))
TEST_OBJS := $(patsubst $(SRCDIR)/%.c,$(OBJDIR)/%.o,$(TEST_SRCS))

LIB_STATIC := $(OBJDIR)/libtchapter.a

# On Windows a shared library is a .dll; elsewhere it is a .so.
ifeq ($(OS),Windows_NT)
  SHARED_EXT := dll
else
  UNAME_S := $(shell uname -s 2>/dev/null)
  ifeq ($(UNAME_S),Darwin)
    SHARED_EXT := dylib
  else
    SHARED_EXT := so
  endif
endif
LIB_SHARED := $(OBJDIR)/libtchapter.$(SHARED_EXT)

.PHONY: all static shared cli test clean fmt

all: static shared cli

static: $(LIB_STATIC)

shared: $(LIB_SHARED)

$(OBJDIR):
	@mkdir -p $(OBJDIR)

# Parser sources live one level deeper, so the object directory has to mirror
# that. The order-only prerequisite on the parent directory keeps -j safe.
$(OBJDIR)/%.o: $(SRCDIR)/%.c | $(OBJDIR)
	@mkdir -p $(dir $@)
	$(CC) $(CSTD) $(CFLAGS) $(WARNINGS) -I$(INCDIR) -I$(SRCDIR) -c $< -o $@

$(LIB_STATIC): $(LIB_OBJS)
	@rm -f $@
	$(AR) rcs $@ $^

$(LIB_SHARED): $(LIB_OBJS)
	$(CC) -shared -o $@ $^

# The CLI is what the Go engine uses by default: a sidecar process keeps the Go
# build free of cgo, so cross-compilation stays a matter of GOOS/GOARCH.
cli: $(OBJDIR)/tchapter

$(OBJDIR)/tchapter: $(SRCDIR)/tc_cli.c $(LIB_STATIC) | $(OBJDIR)
	$(CC) $(CSTD) $(CFLAGS) $(WARNINGS) -I$(INCDIR) -I$(SRCDIR) $< $(LIB_STATIC) -o $@

test: $(OBJDIR)/tc_test
	cd . && $(OBJDIR)/tc_test

$(OBJDIR)/tc_test: $(TEST_OBJS) $(LIB_STATIC) | $(OBJDIR)
	$(CC) $(CSTD) $(CFLAGS) $(WARNINGS) -I$(INCDIR) -I$(SRCDIR) $(TEST_OBJS) $(LIB_STATIC) -o $@

# clang-format is the single formatter for C sources in this repository.
fmt:
	clang-format -i $(SRCDIR)/*.c $(SRCDIR)/*.h $(SRCDIR)/parsers/*.c $(INCDIR)/*.h

clean:
	rm -rf $(OBJDIR)
