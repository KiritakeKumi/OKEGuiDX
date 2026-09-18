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

SRCS := $(wildcard $(SRCDIR)/*.c)
OBJS := $(patsubst $(SRCDIR)/%.c,$(OBJDIR)/%.o,$(SRCS))

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

$(OBJDIR)/%.o: $(SRCDIR)/%.c | $(OBJDIR)
	$(CC) $(CSTD) $(CFLAGS) $(WARNINGS) -I$(INCDIR) -I$(SRCDIR) -c $< -o $@

$(LIB_STATIC): $(OBJS)
	$(AR) rcs $@ $^

$(LIB_SHARED): $(OBJS)
	$(CC) -shared -o $@ $^

# The CLI is what the Go engine uses by default: a sidecar process keeps the Go
# build free of cgo, so cross-compilation stays a matter of GOOS/GOARCH.
cli: $(OBJDIR)/tchapter

$(OBJDIR)/tchapter: $(SRCDIR)/tc_cli.c $(LIB_STATIC) | $(OBJDIR)
	$(CC) $(CSTD) $(CFLAGS) $(WARNINGS) -I$(INCDIR) -I$(SRCDIR) $< $(LIB_STATIC) -o $@

test: $(OBJDIR)/tc_test
	$(OBJDIR)/tc_test

$(OBJDIR)/tc_test: $(SRCDIR)/tc_test.c $(LIB_STATIC) | $(OBJDIR)
	$(CC) $(CSTD) $(CFLAGS) $(WARNINGS) -I$(INCDIR) -I$(SRCDIR) $< $(LIB_STATIC) -o $@

# clang-format is the single formatter for C sources in this repository.
fmt:
	clang-format -i $(SRCDIR)/*.c $(SRCDIR)/*.h $(INCDIR)/*.h

clean:
	rm -rf $(OBJDIR)
