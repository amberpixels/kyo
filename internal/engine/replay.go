package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/amberpixels/kyo/internal/aggregator"
	"github.com/amberpixels/kyo/internal/recording"
	"github.com/amberpixels/kyo/internal/source"
)

// defaultReplaySeconds is how long a replay takes when KYO_REPLAY_SECONDS is
// not set.
const defaultReplaySeconds = 12

// Replay plays a recording back through the live view (or plain milestones)
// and the summary, squeezed into KYO_REPLAY_SECONDS, and returns the recorded
// exit code. The view shows the recorded times, not the replay's.
func Replay(ctx context.Context, path string, opt Options) (int, error) {
	opt = withDefaults(opt)
	total, err := replayDuration(opt.Env)
	if err != nil {
		return 2, err
	}
	f, err := os.Open(path)
	if err != nil {
		return 1, err
	}
	rec, err := recording.Read(f)
	_ = f.Close()
	if err != nil {
		return 1, fmt.Errorf("%s: %w", path, err)
	}
	if len(rec.Items) == 0 {
		return 1, fmt.Errorf("%s: no events to replay", path)
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pace := newPace(rec.Start.Time, rec.End.Time, total, opt.Now())
	wall := opt.Now
	opt.Now = func() time.Time { return pace.clock(wall()) }

	agg := aggregator.New(opt.Now)
	view := startView(opt, agg, rec.Start.Time, rec.Start.Check)
	defer view.close()

	for _, it := range rec.Items {
		if err := pace.wait(ctx, it.Time, wall); err != nil {
			view.stop()
			view.clear()
			if errors.Is(err, context.Canceled) {
				return 130, nil
			}
			return 1, err
		}
		if it.Event != nil {
			agg.Feed(*it.Event)
		} else {
			agg.Stderr(it.Line)
		}
	}
	view.stop()
	agg.Finish(rec.End.Time)
	snap, elapsed := agg.Snapshot(), rec.End.Time.Sub(rec.Start.Time)
	view.end(snap, elapsed)
	report(opt, ending{
		snap: snap, elapsed: elapsed, logPath: rec.Start.LogPath, check: rec.Start.Check,
		src: sourceFromName(rec.End.Source), noPlugin: rec.End.NoPlugin, late: rec.End.Late,
	})
	if !rec.Ended {
		fmt.Fprintln(opt.Stderr, "kyo: the recording stops before the run ended; its exit code is unknown")
		return 1, nil
	}
	return rec.End.ExitCode, nil
}

// replayDuration reads KYO_REPLAY_SECONDS: a non-negative number of seconds.
func replayDuration(env []string) (time.Duration, error) {
	v := getenv(env, "KYO_REPLAY_SECONDS")
	if v == "" {
		return defaultReplaySeconds * time.Second, nil
	}
	secs, err := strconv.ParseFloat(v, 64)
	if err != nil || secs < 0 {
		return 0, fmt.Errorf("KYO_REPLAY_SECONDS=%q: want a number of seconds, 0 or more", v)
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// sourceFromName reads a recording's source name; an unknown one reads as
// the plugin, which adds no note to the summary.
func sourceFromName(name string) source.Source {
	for src, n := range sourceNames {
		if n == name {
			return src
		}
	}
	return source.Plugin
}

// pace maps the recording's span onto the replay's: an item at recorded time
// t is due when the replay has run (t - start) / span of total, and the clock
// the view reads runs span / total times faster than the wall.
type pace struct {
	start, end time.Time // the recording's span
	total      time.Duration
	wallStart  time.Time
}

func newPace(start, end time.Time, total time.Duration, wallStart time.Time) pace {
	return pace{start: start, end: end, total: total, wallStart: wallStart}
}

// due is how far into the replay an item recorded at t is fed.
func (p pace) due(t time.Time) time.Duration {
	span := p.end.Sub(p.start)
	if span <= 0 || p.total <= 0 {
		return 0
	}
	off := min(max(t.Sub(p.start), 0), span)
	return time.Duration(float64(off) / float64(span) * float64(p.total))
}

// clock is the recorded time the replay stands at when the wall reads now.
func (p pace) clock(now time.Time) time.Time {
	span := p.end.Sub(p.start)
	if span <= 0 || p.total <= 0 {
		return p.end
	}
	ran := min(max(now.Sub(p.wallStart), 0), p.total)
	return p.start.Add(time.Duration(float64(ran) / float64(p.total) * float64(span)))
}

// wait blocks until the item recorded at t is due, or ctx ends.
func (p pace) wait(ctx context.Context, t time.Time, wall func() time.Time) error {
	d := p.due(t) - wall().Sub(p.wallStart)
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
