// Generates internal/engine/testdata/status_fixture.json by running the
// original C# StatusUpdate.FillValues, so the Go port is checked against
// recorded behaviour rather than against a re-reading of the source.
//
// Regenerate:
//
//	cd <temp>/csprobe            # any console project
//	copy this file and StatusUpdate.cs (from OKEGui/OKEGui/Job/) into it
//	dotnet run -- <repo>/internal/engine/testdata/status_fixture.json
//
// The file is kept in testdata/gen/ for reference; it is not compiled by the
// Go build.
using System;
using System.Collections.Generic;
using System.IO;
using System.Reflection;
using System.Text.Json;
using OKEGui;

class Gen
{
    public class In
    {
        public decimal? percent { get; set; }
        public double? est_total_ms { get; set; }
        public ulong? frames_done { get; set; }
        public ulong? frames_total { get; set; }
        public ulong? current_size { get; set; }
        public ulong? total_size { get; set; }
        public double? clip_pos_ms { get; set; }
        public double? clip_len_ms { get; set; }
        public double elapsed_ms { get; set; }
    }

    public class Want
    {
        public decimal percent { get; set; }
        public bool percent_known { get; set; }
        public string speed { get; set; }
        public bool eta_known { get; set; }
        public double eta_ms { get; set; }
        public ulong? frames_done { get; set; }
        public ulong? frames_total { get; set; }
        public ulong? current_size { get; set; }
        public ulong? projected_size { get; set; }
        public double? clip_pos_ms { get; set; }
    }

    public class Step { public In @in { get; set; } public Want want { get; set; } }
    public class Case { public string name { get; set; } public List<Step> steps { get; set; } = new(); }
    public class Remain { public double ms { get; set; } public string want { get; set; } }
    public class PctFmt { public double percent { get; set; } public string want { get; set; } }
    public class Root
    {
        public string note { get; set; }
        public List<Case> cases { get; set; } = new();
        public List<Remain> time_remain { get; set; } = new();
        public List<PctFmt> percent_format { get; set; } = new();
    }

    static readonly FieldInfo PctField =
        typeof(StatusUpdate).GetField("percentage", BindingFlags.NonPublic | BindingFlags.Instance);

    static In I() => new In();
    static Step S(In i) => new Step { @in = i };

    static Want Run(StatusUpdate u, In i)
    {
        if (i.percent.HasValue) u.PercentageDoneExact = i.percent;
        if (i.est_total_ms.HasValue) u.EstimatedTime = TimeSpan.FromMilliseconds(i.est_total_ms.Value);
        if (i.frames_done.HasValue) u.NbFramesDone = i.frames_done;
        if (i.frames_total.HasValue) u.NbFramesTotal = i.frames_total;
        if (i.current_size.HasValue) u.CurrentFileSize = i.current_size;
        if (i.total_size.HasValue) u.ProjectedFileSize = i.total_size;
        if (i.clip_pos_ms.HasValue) u.ClipPosition = TimeSpan.FromMilliseconds(i.clip_pos_ms.Value);
        if (i.clip_len_ms.HasValue) u.ClipLength = TimeSpan.FromMilliseconds(i.clip_len_ms.Value);
        u.TimeElapsed = TimeSpan.FromMilliseconds(i.elapsed_ms);

        // PercentageDoneExact is not nullable, so a sentinel written straight
        // into the private field tells us whether FillValues found a fraction.
        PctField.SetValue(u, -12345m);
        u.FillValues();
        decimal pct = (decimal)PctField.GetValue(u);
        bool known = pct != -12345m;
        if (!known) pct = 0m;

        TimeSpan? eta = u.EstimatedTime;
        return new Want
        {
            percent = pct,
            percent_known = known,
            speed = u.ProcessingSpeed,
            eta_known = eta.HasValue,
            eta_ms = eta?.TotalMilliseconds ?? 0,
            frames_done = u.NbFramesDone,
            frames_total = u.NbFramesTotal,
            current_size = u.CurrentFileSize,
            projected_size = u.ProjectedFileSize,
            clip_pos_ms = u.ClipPosition?.TotalMilliseconds,
        };
    }

    // Mirrors TaskStatus.TimeRemainStr (TaskStatus.cs lines 191-203).
    static string RemainStr(TimeSpan value)
    {
        string s = (int)value.TotalHours + value.ToString(@"\:mm\:ss");
        if (value.TotalHours > 24.0 * 7) s = "大于一周";
        return s;
    }

    // Mirrors TaskStatus.ProgressValue (TaskStatus.cs lines 112-132).
    static string PctStr(double v) => v < 0 ? "" : v.ToString("0.00") + "%";

    // Builds a case and immediately records the original C# output for every
    // step; all steps of a case share one StatusUpdate, like a real encode.
    static Case C(string name, params Step[] steps)
    {
        var c = new Case { name = name };
        c.steps.AddRange(steps);
        var u = new StatusUpdate(name);
        foreach (var s in c.steps)
            s.want = Run(u, s.@in);
        return c;
    }

    static void Main(string[] args)
    {
        var root = new Root
        {
            note = "Generated by running the original C# StatusUpdate.FillValues (OKEGui/OKEGui/Job/StatusUpdate.cs). "
                 + "Do not hand-edit; regenerate with testdata/gen/Gen.cs.",
        };

        root.cases.Add(C("percent_only", S(new In { percent = 25m, elapsed_ms = 10000 })));
        root.cases.Add(C("percent_zero", S(new In { percent = 0m, elapsed_ms = 10000 })));
        root.cases.Add(C("percent_100", S(new In { percent = 100m, elapsed_ms = 10000 })));
        root.cases.Add(C("percent_150", S(new In { percent = 150m, elapsed_ms = 9000 })));
        root.cases.Add(C("percent_fraction_33_3", S(new In { percent = 33.3m, elapsed_ms = 30000 })));
        root.cases.Add(C("est_total_only", S(new In { est_total_ms = 100000, elapsed_ms = 25000 })));
        root.cases.Add(C("est_total_zero_falls_through", S(new In { est_total_ms = 0, frames_done = 1, frames_total = 1000, elapsed_ms = 50000 })));
        root.cases.Add(C("frames_only", S(new In { frames_done = 50, frames_total = 200, elapsed_ms = 10000 })));
        root.cases.Add(C("frames_zero_total_falls_through_to_sizes", S(new In { frames_done = 1, frames_total = 0, current_size = 1, total_size = 1000, elapsed_ms = 10000 })));
        root.cases.Add(C("sizes_integer_division", S(new In { current_size = 250, total_size = 1000, elapsed_ms = 10000 })));
        root.cases.Add(C("sizes_exact_half", S(new In { current_size = 500, total_size = 1000, elapsed_ms = 10000 })));
        root.cases.Add(C("clip_only", S(new In { clip_pos_ms = 30000, clip_len_ms = 120000, elapsed_ms = 10000 })));
        root.cases.Add(C("clip_zero_len_unknown_fraction", S(new In { clip_pos_ms = 30000, clip_len_ms = 0, elapsed_ms = 10000 })));
        root.cases.Add(C("priority_percent_beats_all", S(new In { percent = 50m, est_total_ms = 100000, frames_done = 1, frames_total = 1000, current_size = 1, total_size = 1000, clip_pos_ms = 1000, clip_len_ms = 100000, elapsed_ms = 10000 })));
        root.cases.Add(C("priority_est_beats_frames", S(new In { est_total_ms = 100000, frames_done = 1, frames_total = 1000, elapsed_ms = 50000 })));
        root.cases.Add(C("priority_frames_beats_sizes", S(new In { frames_done = 1, frames_total = 1000, current_size = 500, total_size = 1000, elapsed_ms = 10000 })));
        root.cases.Add(C("priority_sizes_beat_clip", S(new In { current_size = 1, total_size = 1000, clip_pos_ms = 30000, clip_len_ms = 120000, elapsed_ms = 10000 })));
        root.cases.Add(C("derive_frames_done", S(new In { frames_total = 1000, percent = 33.3m })));
        root.cases.Add(C("derive_frames_total", S(new In { frames_done = 1, percent = 33.3m })));
        root.cases.Add(C("derive_projected_size", S(new In { current_size = 999, percent = 33.3m })));
        root.cases.Add(C("derive_clip_position", S(new In { clip_len_ms = 1000, percent = 33.3m })));
        root.cases.Add(C("derive_frames_done_from_size_fraction", S(new In { current_size = 500, total_size = 1000, frames_total = 1000 })));
        root.cases.Add(C("derive_frames_total_from_clip_fraction", S(new In { clip_pos_ms = 30000, clip_len_ms = 120000, frames_done = 60 })));
        root.cases.Add(C("replace_frames_done", S(new In { frames_done = 100, percent = 25m }), S(new In { frames_done = 200 })));
        root.cases.Add(C("elapsed_zero", S(new In { frames_done = 50, frames_total = 100, elapsed_ms = 0 })));
        root.cases.Add(C("speed_rounding_half_away", S(new In { frames_done = 5, elapsed_ms = 2000 })));
        root.cases.Add(C("speed_rounding_down", S(new In { frames_done = 1, elapsed_ms = 3000 })));
        root.cases.Add(C("speed_rounding_33", S(new In { frames_done = 100, elapsed_ms = 3000 })));
        root.cases.Add(C("speed_clip_position_precedence", S(new In { percent = 50m, clip_pos_ms = 30000, clip_len_ms = 120000, elapsed_ms = 10000 })));
        root.cases.Add(C("speed_from_fraction_times_cliplen", S(new In { percent = 50m, clip_len_ms = 120000, elapsed_ms = 10000 })));
        root.cases.Add(C("speed_without_fraction_from_clip", S(new In { clip_pos_ms = 30000, elapsed_ms = 10000 })));
        root.cases.Add(C("speed_without_fraction_from_frames", S(new In { frames_done = 50, elapsed_ms = 10000 })));

        // Sliding window: 12 updates, 1% apart, 6 s apart.
        var linear = new Case { name = "window_linear_12" };
        for (int i = 1; i <= 12; i++)
            linear.steps.Add(S(new In { frames_done = (ulong)i, frames_total = 100, elapsed_ms = i * 6000 }));
        root.cases.Add(C("window_linear_12", linear.steps.ToArray()));

        // Sliding window: progress stalls at 50%, 10 s apart.
        var stall = new Case { name = "window_stall_11" };
        for (int i = 1; i <= 11; i++)
            stall.steps.Add(S(new In { frames_done = 500, frames_total = 1000, elapsed_ms = i * 10000 }));
        root.cases.Add(C("window_stall_11", stall.steps.ToArray()));

        // Sliding window: updates less than five seconds apart use the fallback.
        root.cases.Add(C("window_sub5s",
            S(new In { frames_done = 100, frames_total = 1000, elapsed_ms = 3000 }),
            S(new In { frames_done = 200, frames_total = 1000, elapsed_ms = 6000 }),
            S(new In { frames_done = 300, frames_total = 1000, elapsed_ms = 9000 })));

        // Sliding window: the eleventh update reads slot 0 again.
        var wrapBack = new Case { name = "window_wrap_regression" };
        for (int i = 1; i <= 10; i++)
            wrapBack.steps.Add(S(new In { frames_done = (ulong)(i * 100), frames_total = 1000, elapsed_ms = i * 6000 }));
        wrapBack.steps.Add(S(new In { frames_done = 500, frames_total = 1000, elapsed_ms = 66000 }));
        root.cases.Add(C("window_wrap_regression", wrapBack.steps.ToArray()));

        var wrapFwd = new Case { name = "window_wrap_progress" };
        for (int i = 1; i <= 10; i++)
            wrapFwd.steps.Add(S(new In { frames_done = (ulong)(i * 100), frames_total = 1000, elapsed_ms = i * 6000 }));
        wrapFwd.steps.Add(S(new In { frames_done = 150, frames_total = 1000, elapsed_ms = 66000 }));
        root.cases.Add(C("window_wrap_progress", wrapFwd.steps.ToArray()));

        // The fallback branch must still advance the window index.
        var advance = new Case { name = "window_fallback_advances_index" };
        for (int i = 1; i <= 11; i++)
            advance.steps.Add(S(new In { frames_done = 500, frames_total = 1000, elapsed_ms = i * 10000 }));
        advance.steps.Add(S(new In { frames_done = 600, frames_total = 1000, elapsed_ms = 120000 }));
        root.cases.Add(C("window_fallback_advances_index", advance.steps.ToArray()));

        // A failed estimate leaves the previous one in place, because the
        // legacy exception handler swallowed the error after the assignment.
        root.cases.Add(C("sticky_eta_after_zero_fraction",
            S(new In { percent = 50m, elapsed_ms = 10000 }),
            S(new In { percent = 0m })));
        root.cases.Add(C("sticky_eta_after_zero_elapsed",
            S(new In { frames_done = 50, frames_total = 100, elapsed_ms = 10000 }),
            S(new In { elapsed_ms = 0 })));
        root.cases.Add(C("sticky_projected_size_after_zero_fraction",
            S(new In { current_size = 100, percent = 50m }),
            S(new In { percent = 0m })));

        foreach (var ts in new[]
        {
            TimeSpan.Zero,
            TimeSpan.FromMilliseconds(1500),
            TimeSpan.FromSeconds(5),
            TimeSpan.FromSeconds(59.9),
            TimeSpan.FromSeconds(65),
            TimeSpan.FromMinutes(59.99),
            TimeSpan.FromHours(1),
            TimeSpan.FromHours(25.5),
            TimeSpan.FromHours(100.75),
            TimeSpan.FromHours(168),
            TimeSpan.FromHours(168.0002777),
            TimeSpan.FromDays(30),
            TimeSpan.FromSeconds(-1.5),
            TimeSpan.FromSeconds(-5),
            TimeSpan.FromHours(-1.5),
            TimeSpan.FromHours(-100.75),
        })
            root.time_remain.Add(new Remain { ms = ts.TotalMilliseconds, want = RemainStr(ts) });

        foreach (var p in new[] { -1.0, 0.0, 0.005, 0.125, 12.5, 33.333333, 99.995, 100.0, 150.0, 2.675, 1.005 })
            root.percent_format.Add(new PctFmt { percent = p, want = PctStr(p) });

        var opts = new JsonSerializerOptions
        {
            WriteIndented = true,
            PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower,
            Encoder = System.Text.Encodings.Web.JavaScriptEncoder.UnsafeRelaxedJsonEscaping,
        };
        File.WriteAllText(args[0], JsonSerializer.Serialize(root, opts));
        Console.WriteLine("wrote " + args[0]);
    }
}
