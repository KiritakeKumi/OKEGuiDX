# The task pipeline

`pipeline.go` and its siblings are the Go port of
`Worker/ExecuteTaskService.cs` (842 lines). The legacy file is one
`WorkerDoWork` method plus a dozen private helpers, and it mixes four kinds of
decision in a single control flow:

1. what the profile says (re-encode or not, VFR or not, which container);
2. what the source turned out to be (frame count, frame rate, tracks);
3. which external tool runs next, in which order;
4. what the operator sees.

This package keeps those four apart. A **stage** is one of the decisions; it
takes a `*runState` and a `*reporter`, returns an error, and nothing else. The
stage sequence lives in one place (`Pipeline.runStages`) so the order can be
read off, and the state a stage needs is explicit rather than spread across
`TaskDetail`, `TaskProfile` and locals.

## Stage sequence

Mirrors `WorkerDoWork` line for line. Each stage names the legacy method it
replaces.

| # | Stage | Legacy source | Contract |
|---|---|---|---|
| 0 | `loadProfile` | — | Recover the profile and episode config. Fails when a re-encode has no episode config, or when a field the pipeline depends on is missing. Sets `isReEncode`, `reEncodeSlices`; both inputs stay read-only. |
| 1 | `stageInspect` | `GetVSPipeInfo` | Run `vspipe --info`; enforce the frame-rate check; for a re-encode, run the I-frame probe. Sets `frames`, `vsInfo`, `iFrames`. |
| 2 | `stagePrepare` | `DoPreparation` | Write the timecode file (VFR), load and write the chapters, derive the qpfile. Sets `timecodeFile`, `chapterFrames`, `qpValue`, `media.Chapter`. |
| 3 | `stagePlanReEncode` | `CheckReEncodeSlice`, `GenerateReEncodeJob` (layout) | Align the requested slices to the old release's I-frames, merge contiguous ones, lay out the parts. Sets `reEncodeSlices`, `parts`, `task.SliceParts`. |
| 4 | `stageDemux` | `ExtractSource`, `GenerateAudioJob`, `AddSubtitle` | Extract the source's tracks, detect empty and duplicate ones, queue the audio jobs and route the subtitles. Sets `srcAudio`, `srcSubs`, `audioJobs`. |
| 5 | `stageAudio` | `DoAllJobs` (AudioJob) | Transcode each queued track and route it to the main or the external container. |
| 6 | `stageMKA` | `GenerateMuxJob`, `DoAllJobs` (NewMkvEpisode/MKA) | Mux the external audio file, when any track was routed to it. |
| 7 | `stageVideo` / `stageReEncodeVideo` | `GenerateVideoJob`, `GenerateReEncodeJob`, `DoAllJobs` (VideoJob, SingleVideo) | Encode the whole file, or every part of a re-encode, muxing each part on its own. |
| 8 | `stageAppend` | `GenerateMuxJob` (AppendVideo), `DoAllJobs` (AppendVideo) | Join the re-encode parts into one video. |
| 9 | `stageMux` / `stageMergeOld` | `GenerateMuxJob`, `DoAllJobs` (NewMkvEpisode, NewMp4Episode, MergeOldRemux) | Write the deliverable: an episode mux, or a merge with the old release's remaining tracks. |
| 10 | `stageRPC` | `DoRPCheck` | Run the PSNR check, or record it as skipped. |
| 11 | `stageCleanup` | (UI action) | Delete the intermediate files, when the caller asked for it. |

Two conditions make the sequence non-linear, and both are in `runStages`:

- the demux/audio/MKA block runs only when `!IsReEncode || ReExtractSource`,
  because a re-encode that reuses the old release's tracks takes them at mux
  time instead;
- the container block is a merge (`--no-video` on the old release) only for a
  re-encode without `ReExtractSource`.

## Read-only inputs

The task the pipeline is handed carries its profile and episode config as shared
pointers: `TaskManager.cloneTask` copies the task's slices but deliberately
leaves `Profile` and `Config` alone, and every queue snapshot shares them with
the running worker. The pipeline therefore **never writes** to either. Everything
it derives from them lives in the `runState`:

- the reconciled re-encode flag (`IsReEncode || EnableReEncode`) is
  `runState.isReEncode`, not a write back to the profile;
- the re-encode slice array is `runState.reEncodeSlices`: a private copy, first
  normalized by `profile.ValidateEpisodeConfig` (which sorts and merges in
  place), then aligned to the old release's I-frames.

`pipeline_readonly_test.go` is the acceptance test: it runs a task and compares
the profile and config before and after, and it races a JSON reader against the
run so a reintroduced write fails under `-race`.

## Platform capabilities

Nothing in the pipeline branches on `runtime.GOOS`. The two platform-dependent
decisions go through `node.Capabilities`:

- **Demuxer.** `newDemuxer` picks eac3to when the node advertises
  `node.FeatureEac3to` and ffmpeg otherwise. eac3to is a closed Windows x86
  binary; ffmpeg is the all-platform implementation of the same behaviour
  (`internal/jobproc/demux/ffmpeg`, with `compat_test.go` pinning the two
  Options/Result shapes against each other). The pipeline normalises the two
  distinct Go types into one `demuxResult`, so the choice is one `if` in one
  place.
- **AAC.** `qaac.New` and `ffmpegqaac.Run` call `toolchain.EnsureAAC`, so an AAC
  profile on a node without `node.FeatureAAC` is refused with
  `okerr.ErrUnsupportedAAC` before any process starts. The pipeline calls it too,
  so the failure lands in the audio stage rather than inside a wrapper.
- **x265 build variant** is resolved by `toolchain` at discovery time and never
  mentioned here.
- **NUMA.** `engine.NUMANodeFrom(ctx)` reads the node the worker pool assigned
  and the pipeline turns it into x265's `--pools` mask through
  `platform.Numa.X265PoolsParam`.

## Errors

Every failure is a `*okerr.Error`. The summaries are the legacy
`Constants.*Smr` strings and `okerr.Render` produces the operator-facing
`Constants.*Msg` template, so the wording is unchanged:

| Failure | Summary |
|---|---|
| `vspipe --info` frame rate differs from the profile | `ErrFpsMismatch` |
| the source's track count differs from the profile's | `ErrAudioNumMismatch`, `ErrSubNumMismatch` |
| a re-encode slice is out of range or unalignable | `ErrReEncodeSlice` |
| the old release is longer than the script | `ErrReEncodeFrames` (from the I-frame probe) |
| the extracted format matches neither AAC path | `ErrAudioFormatMismatch` |
| the demuxer, an encoder or a muxer failed | `ErrEac3to`, `ErrX264`, `ErrX265`, `ErrSVTAV1`, `ErrMkvmerge`, `ErrLSmash` |
| a task was cancelled | `KindCanceled` with `任务已取消` |

## Re-encode slicing

The most involved branch, and the reason `checkReEncodeSlices` and `planParts`
are separate functions:

1. **Align.** Every requested range `[begin, end)` moves outwards to the old
   release's I-frames: `begin` to the nearest I-frame at or before it, `end` to
   the nearest one at or after it. A Matroska splice can only happen at a key
   frame, which is what the alignment is for. `end == -1` becomes the old
   release's frame count.
2. **Validate.** A slice starting at or past the last frame, or ending past it,
   is `Constants.reEncodeSliceErrorSmr`. A boundary with no I-frame is an error
   rather than the legacy silent move to frame 0 (DECISIONS-NEEDED.md B4).
3. **Merge.** Two slices that end up touching become one, because a zero-length
   gap would produce an empty part.
4. **Lay out.** `planParts` walks the merged list and emits alternating copied
   and encoded parts: the gaps become parts cut out of the old release with
   mkvmerge's `--split parts-frames:`, the slices become parts encoded with
   `vspipe -s/-e`. A leading gap becomes part 0 and a trailing gap becomes the
   last part. The part number is the legacy `partId`, and it is what names
   `_partN` files and what the "Part N/M" status shows.
5. **Encode and mux.** Each encoded part runs its own encoder invocation bounded
   by the part's range, then its own single-video mux. The `totalFrames` given to
   the encoder is the part's length, not the script's frame count: the wrappers
   use it both for the progress percentage and for the truncated-encode check.
6. **Append.** All part files are joined with `--append-to`.
7. **Chapter qpfiles.** A part's qpfile holds only the chapter marks inside it,
   rebased onto the part's own frame numbering.

## What the pipeline does not do

- It never starts a process. Every external tool is a `jobproc.Processor` and
  every stage drives one through `runProcessor`, which is also the single place
  `Close` is called.
- It never parses tool output. The wrappers do that and report through
  `jobproc.ProgressSink`.
- It never writes to the task queue. `PipelineOptions.UpdateTask` is the hook for
  the fields a `model.StatusEvent` cannot carry (the chapter status and the RPC
  result); the worker pool owns the queue.
- It never writes to the profile or the episode config it is given. Both are
  shared with the queue, which serves them to API clients; see "Read-only
  inputs" above.
- It never reads global state. `PipelineOptions` carries the toolchain, the
  profile loader, the NUMA allocator and the priorities.
