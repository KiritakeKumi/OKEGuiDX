# testdata provenance

Both fixtures are the byte-exact line shapes `cli/muxer.c` (L-SMASH v2.14.5,
the version pinned in `OKEGuiDX-tools/versions.lock`) writes to **stderr**.
Nothing here is invented: each line maps to one `eprintf` call in that file.

| Fixture | Emitting code |
|---|---|
| `MP4 muxing mode\n` | `decide_brands()` |
| `Track N: <codec>\n` | `open_input_files()` -> `display_codec_name()` |
| `Importing: <bytes>\r` | `do_mux()`, once per 4 MiB of imported media |
| `Finalizing: [ xx.xx%]\r` | `moov_to_front_callback()`, once per 16 MiB written |
| `Muxing completed!\n` | `main()` |
| `Error: <message>\n` | `error_message()` / `muxer_error()` |

Two details matter for the parser and are reproduced faithfully:

1. The progress lines end in a bare carriage return, so a reader that only
   splits on `\n` receives many steps in a single line. `lsmash.go` splits on
   `\r` to reproduce what .NET's `StreamReader` (which treats `\r` as a line
   terminator) handed to the legacy `ProcessLine`.
2. `muxer_error()` writes the `Error: ` prefix and the message with two
   separate `eprintf` calls, so the text after the prefix is exactly what the
   legacy code stored as `LSMASH_ERROR` via `line.Substring(7)`.

`muxer_progress.log` covers a 400 MiB import; `muxer_error.log` fails after the
first import step.
