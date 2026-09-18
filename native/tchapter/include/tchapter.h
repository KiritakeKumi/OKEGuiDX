/*
 * tchapter.h - chapter parsing library with a stable C ABI.
 *
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Why a C library: the .NET implementation depended on libmp4v2, which only
 * ships prebuilt win-x86/x64 binaries and therefore blocks every non-x86 target
 * (INVENTORY.md §4). This library has no external dependencies beyond libc, so
 * `cc -O2` produces a working build on any architecture, including riscv64.
 *
 * The ABI is deliberately small and value-oriented: parsing returns an opaque
 * handle, data is read through accessors, and the caller frees the handle once.
 * Strings are owned by the handle and stay valid until tc_free.
 *
 * Threading: handles are not shared between threads. tc_last_error is
 * thread-local, so concurrent parses on different threads are safe.
 */
#ifndef TCHAPTER_H
#define TCHAPTER_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#if defined(_WIN32)
#  ifdef TCHAPTER_BUILD_SHARED
#    define TC_API __declspec(dllexport)
#  elif defined(TCHAPTER_USE_SHARED)
#    define TC_API __declspec(dllimport)
#  else
#    define TC_API
#  endif
#else
#  if defined(TCHAPTER_BUILD_SHARED)
#    define TC_API __attribute__((visibility("default")))
#  else
#    define TC_API
#  endif
#endif

/* ------------------------------------------------------------------ */
/* Version                                                            */
/* ------------------------------------------------------------------ */

#define TC_VERSION_MAJOR 1
#define TC_VERSION_MINOR 0
#define TC_VERSION_PATCH 0

/* ------------------------------------------------------------------ */
/* Status codes                                                       */
/* ------------------------------------------------------------------ */

typedef enum tc_status {
    TC_OK = 0,
    TC_E_IO = 1,      /* the file could not be opened or read */
    TC_E_FORMAT = 2,  /* the content does not match the requested format */
    TC_E_RANGE = 3,   /* an index or offset was out of range */
    TC_E_NOMEM = 4,   /* allocation failed */
    TC_E_INVALID = 5, /* an argument was invalid */
    TC_E_UNSUPPORTED = 6
} tc_status;

/* ------------------------------------------------------------------ */
/* Formats                                                            */
/* ------------------------------------------------------------------ */

typedef enum tc_format {
    TC_FMT_AUTO = 0,
    TC_FMT_MPLS,
    TC_FMT_CUE,
    TC_FMT_FLAC,
    TC_FMT_IFO,
    TC_FMT_MP4,
    TC_FMT_OGM,
    TC_FMT_VTT,
    TC_FMT_XML,
    TC_FMT_XPL,
    TC_FMT_TAK,
    TC_FMT_MATROSKA_XML,
    TC_FMT_BDMV
} tc_format;

/* ------------------------------------------------------------------ */
/* Data types                                                         */
/* ------------------------------------------------------------------ */

/* Opaque parse result. May contain several entries: an MPLS playlist or a CUE
 * sheet can describe multiple titles. */
typedef struct tc_data tc_data;

/* One chapter. `time_ns` is nanoseconds, which covers both the 90 kHz Blu-ray
 * clock and DVD's 1/100 s without losing precision. */
typedef struct tc_chapter {
    const char *name;
    int64_t time_ns;
    /* Frames since the start, when the source provided them; -1 when unknown. */
    int64_t frames;
} tc_chapter_t;

/* One entry (title) inside a tc_data. */
typedef struct tc_entry_info {
    /* Title of the entry; never NULL, may be empty. */
    const char *title;
    /* Source file the entry refers to; may be empty. */
    const char *source;
    /* Frame rate as a rational; den is never 0. */
    int64_t fps_num;
    int64_t fps_den;
    /* Duration in nanoseconds. */
    int64_t duration_ns;
    /* Number of chapters in this entry. */
    size_t chapter_count;
} tc_entry_info_t;

/* ------------------------------------------------------------------ */
/* Parsing                                                            */
/* ------------------------------------------------------------------ */

/* Parses a file. `fmt` may be TC_FMT_AUTO to detect the format from the
 * content and the file name.
 *
 * On success *out receives a handle that must be released with tc_free.
 * On failure *out is set to NULL and tc_last_error explains why. */
TC_API tc_status tc_parse_file(const char *path, tc_format fmt, tc_data **out);

/* Parses from memory. `name_hint` is used only for format detection and may be
 * NULL. The buffer does not need to stay alive after the call. */
TC_API tc_status tc_parse_mem(const void *buf, size_t len, tc_format fmt,
                              const char *name_hint, tc_data **out);

/* Detects the format of a file without fully parsing it. Returns TC_FMT_AUTO
 * when the format is not recognised. */
TC_API tc_format tc_detect_format(const char *path);

/* ------------------------------------------------------------------ */
/* Reading                                                            */
/* ------------------------------------------------------------------ */

/* Number of entries in the handle. */
TC_API size_t tc_entry_count(const tc_data *d);

/* Fills `out` for entry `index`. Returns TC_E_RANGE for a bad index. */
TC_API tc_status tc_entry_info(const tc_data *d, size_t index, tc_entry_info_t *out);

/* Number of chapters in an entry. */
TC_API size_t tc_chapter_count(const tc_data *d, size_t entry);

/* Fills `out` for chapter `index` of `entry`.
 *
 * The strings inside `out` are owned by the handle and remain valid until
 * tc_free. `frames` is -1 when the source did not provide frame numbers. */
TC_API tc_status tc_chapter_at(const tc_data *d, size_t entry, size_t index,
                               tc_chapter_t *out);

/* ------------------------------------------------------------------ */
/* Writing                                                            */
/* ------------------------------------------------------------------ */

/* Serialises one entry into a chapter format.
 *
 * `fmt` must be a writable text format: TC_FMT_OGM, TC_FMT_XML, TC_FMT_XPL,
 * TC_FMT_VTT or TC_FMT_CUE. Binary targets are rejected with
 * TC_E_UNSUPPORTED.
 *
 * `language` and `source_name` may be NULL. `index` selects which title of the
 * entry to write when the source had several. */
TC_API tc_status tc_save(const tc_data *d, size_t entry, tc_format fmt,
                         const char *path, const char *language,
                         const char *source_name);

/* Renders one entry to a caller-supplied buffer instead of a file.
 *
 * On entry *len holds the buffer size. On success *len is set to the number of
 * bytes written, excluding the terminating NUL. When the buffer is too small,
 * TC_E_RANGE is returned and *len is set to the required size, so callers can
 * retry. A NULL buffer with *len == 0 only reports the required size. */
TC_API tc_status tc_render(const tc_data *d, size_t entry, tc_format fmt,
                           const char *language, const char *source_name,
                           char *buf, size_t *len);

/* ------------------------------------------------------------------ */
/* Lifecycle and diagnostics                                          */
/* ------------------------------------------------------------------ */

/* Releases a handle. Passing NULL is allowed. */
TC_API void tc_free(tc_data *d);

/* Returns the message for the last failure on the calling thread. The pointer
 * is owned by the library and stays valid until the next failing call on the
 * same thread. Never returns NULL. */
TC_API const char *tc_last_error(void);

/* Returns the library version as "major.minor.patch". */
TC_API const char *tc_version(void);

/* Returns the name of a status code, for logging. */
TC_API const char *tc_status_name(tc_status status);

/* Returns the canonical file extension for a format, including the dot; an
 * empty string for TC_FMT_AUTO. */
TC_API const char *tc_format_extension(tc_format fmt);

#ifdef __cplusplus
} /* extern "C" */
#endif

#endif /* TCHAPTER_H */
