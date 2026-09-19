# testdata provenance

Every fixture here is a byte-exact capture of `SvtAv1EncApp`'s **stderr**
stream. Nothing is invented.

The captures were made with the exact argument shape `New()` assembles:

```
--progress 2 <EncoderParam...> -b <output> -i - -o NUL
```

(`-o NUL` is what the frozen `video.Base` appends; see the package comment in
`svtav1.go` for why the recon stream has to go to the null device.)

| Fixture | Encoder | Emitting code |
|---|---|---|
| `v141_stdin.txt` | v1.4.1, built with `-DLOG_ENC_DONE=1` | `EbAppProcessCmd.c` progress `case 2`, `EbAppMain.c` summary + `all_done_encoding` |
| `v420_stdin.txt` | v4.2.0, the version pinned in `OKEGuiDX-tools/versions.lock` | `app_process_cmd.c` progress `case 2`, `app_main.c` summary |
| `error_color_format.txt` | v4.2.0 | `Svt[error]` from the library's config validation |
| `error_single_dash.txt` | v4.2.0 | `[SVT-Error]` from `app_config.c`'s token check |

Both successful captures encode the same source: 256x144 `testsrc2`, 24 fps,
120 frames, `--preset 10 --crf 35`.

Three details matter for the parser and are reproduced faithfully:

1. **Progress records end in a bare carriage return.** The app writes
   `"\rEncoding frame %4d ..."` (v1.4.1) or `"\rEncoding: ..."` (v4.2.0) with
   no newline, so a reader that only splits on `\n` — which is what
   `internal/proc`'s `bufio.Scanner` does — receives an entire run of records as
   a single scanned line. `svtav1.go` therefore searches within a line and takes
   the *last* match, not the first.

2. **The two successful captures use different progress dialects.** v1.4.1
   prints `Encoding frame   42 4795.06 kbps 3.91 fps`, which is the shape
   `SVTAV1Encoder.cs`'s regex was written for. v4.2.0 prints
   `Encoding:   42/120 Frames @ 3.91 fps | 4795.06 kb/s | Size: ...`, which the
   C# regex does **not** match. Both dialects are covered.

3. **v4.2.0 colourises its progress unconditionally**, even with stderr
   redirected to a file. The escapes are kept in the fixture because they are
   what the real stream contains; `stripANSI` removes them before matching.

`v141_stdin.txt` also carries the `all_done_encoding  120 frames` line that the
C# used as its primary end-of-encode signal. Upstream ships
`LOG_ENC_DONE 0` in `Source/API/EbDebugMacros.h`, and the tools recipe does not
override it, so release builds of v4.2.0 never print that line — which is why
the summary table is handled as well.
