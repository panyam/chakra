#!/usr/bin/env bash
# Runs a command while holding a lock directory, so two Terminal-Bench batches
# never run at once (#289). `make tbench-stale RUN=1` runs its pick, trials
# and ingest under this lock in TB_JOBS, which every checkout shares.
#
#   tbench-lock.sh <lockdir> <command> [args...]
#
# A second caller waits, naming the holder's pid, then picks after the first
# batch's results exist, so the two pick disjoint cells. A lock whose pid is
# no longer running is taken over. The command's exit status comes through.
# macOS has no flock(1), so the lock is a directory with a pid file inside,
# and whoever creates the pid file holds it. Bash's noclobber creates it with
# O_EXCL, so exactly one caller wins. mkdir can't decide that: uutils mkdir
# (Ubuntu 26.04) reports success to both of two racing callers.
set -u

[ $# -ge 2 ] || { sed -n '5p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
lock=$1
shift
said= empty=0
while ! { mkdir -p "$lock" && (set -C; echo $$ >"$lock/pid"); } 2>/dev/null; do
	pid=$(cat "$lock/pid" 2>/dev/null || true)
	if [ -z "$pid" ]; then
		# The holder created the pid file and hasn't written to it yet, or
		# the last holder is removing the directory. Ten seconds of an empty
		# file means the holder died in between.
		empty=$((empty + 1))
		if [ $empty -lt 50 ]; then
			sleep 0.2
			continue
		fi
		pid=unknown
	fi
	empty=0
	if [ "$pid" = unknown ] || ! kill -0 "$pid" 2>/dev/null; then
		echo "tbench-lock: pid $pid is gone, taking over $lock" >&2
		rm -f "$lock/pid"
		continue
	fi
	if [ "$said" != "$pid" ]; then
		echo "tbench-lock: waiting for pid $pid, which holds $lock" >&2
		said=$pid
	fi
	sleep 2
done
trap 'rm -rf "$lock"' EXIT
trap 'exit 130' INT TERM

"$@"
