#!/usr/bin/env python3
"""Private acceptance captures and schedule-request preparation; no implicit AWS calls.

Only `capture` executes a command, as an explicit argv without a shell. Never pass
credential commands, private keys, raw state, or secret-valued arguments to it.
Derived identity summaries are recovery aids, never cleanup authorization.
"""

import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import uuid


def utc_now():
    return dt.datetime.now(dt.timezone.utc)


def timestamp(value=None):
    return (value or utc_now()).isoformat(timespec="seconds").replace("+00:00", "Z")


def sync_directory(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def write_json(path, value):
    with path.open("x", encoding="utf-8") as stream:
        json.dump(value, stream, indent=2, sort_keys=True)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())
    sync_directory(path.parent)


def digest(path):
    result = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            result.update(chunk)
    return result.hexdigest()


def private_directory(path):
    path = Path(path).resolve(strict=True)
    if not path.is_dir() or path.stat().st_mode & 0o077:
        raise ValueError("evidence directory must exist and have mode 0700")
    return path


def init_run(args):
    root = Path(args.root).expanduser()
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    private_directory(root)
    run = root / (utc_now().strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8])
    run.mkdir(mode=0o700)
    for name in ("commands", "config", "original-config", "artifacts", "migration", "observations"):
        (run / name).mkdir(mode=0o700)
    write_json(run / "run.json", {
        "schema_version": 1, "created_at": timestamp(),
        "preparation_revision": args.revision,
        "live_acceptance": "pending", "final_revision_checks": "pending",
    })
    sync_directory(root)
    print(run.resolve())
    return 0


def stop_process_group(process, sig):
    try:
        os.killpg(process.pid, sig)
    except ProcessLookupError:
        pass
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        pass
    # The direct child may exit before descendants that ignored the first signal.
    # Finish the entire capture group before returning control to the caller.
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    process.wait()


def capture(args):
    run = private_directory(args.run)
    if not re.fullmatch(r"[a-z0-9][a-z0-9_-]{0,79}", args.label):
        raise ValueError("capture label must be a short lowercase filename")
    argv = args.argv[1:] if args.argv[:1] == ["--"] else args.argv
    if not argv:
        raise ValueError("capture needs an explicit command after --")
    target = run / "commands" / args.label
    target.mkdir(mode=0o700)  # Refuse to overwrite earlier evidence, even failures.
    write_json(target / "command.json", {
        "argv": argv, "cwd": str(Path(args.cwd).resolve()),
        "started_at": timestamp(), "status": "started",
    })
    code, status = 1, "failed_to_start"
    stdout, stderr = target / "stdout", target / "stderr"
    try:
        with stdout.open("xb") as out, stderr.open("xb") as err:
            try:
                process = subprocess.Popen(argv, cwd=args.cwd, stdout=out, stderr=err,
                                           start_new_session=True)
                returncode = process.wait(timeout=args.timeout)
                code = returncode if returncode >= 0 else 128 - returncode
                status = "completed"
            except subprocess.TimeoutExpired:
                stop_process_group(process, signal.SIGTERM)
                code, status = 124, "capture_timeout"
            except KeyboardInterrupt:
                if "process" in locals():
                    stop_process_group(process, signal.SIGINT)
                code, status = 130, "interrupted"
            finally:
                out.flush()
                err.flush()
                os.fsync(out.fileno())
                os.fsync(err.fileno())
    finally:
        write_json(target / "result.json", {
            "finished_at": timestamp(), "exit_code": code, "status": status,
            "stdout_sha256": digest(stdout) if stdout.exists() else None,
            "stderr_sha256": digest(stderr) if stderr.exists() else None,
        })
    print(f"{args.label}: exit {code}; {target}")
    return code


# GetSchedule includes read-only metadata that UpdateSchedule must not receive.
SCHEDULE_WRITABLE = {
    "ActionAfterCompletion", "Description", "EndDate", "FlexibleTimeWindow",
    "GroupName", "KmsKeyArn", "Name", "ScheduleExpression",
    "ScheduleExpressionTimezone", "StartDate", "State", "Target",
}
SCHEDULE_METADATA = {"Arn", "CreationDate", "LastModificationDate"}


def schedule_update(original, state, start_date=None, now=None):
    if not isinstance(original, dict) or set(original) - SCHEDULE_WRITABLE - SCHEDULE_METADATA:
        raise ValueError("unknown GetSchedule fields; inspect before preparing an update")
    required = {"Name", "GroupName", "ScheduleExpression", "FlexibleTimeWindow", "Target", "State"}
    if not required.issubset(original) or original["ScheduleExpression"] != "rate(5 minutes)":
        raise ValueError("expected complete captured five-minute schedule")
    if state not in ("ENABLED", "DISABLED"):
        raise ValueError("invalid schedule state")
    update = {key: value for key, value in original.items() if key in SCHEDULE_WRITABLE}
    update["State"] = state
    if state == "ENABLED" and start_date is None:
        raise ValueError("acceptance activation requires an explicit future StartDate")
    if start_date is not None:
        if not re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ", start_date):
            raise ValueError("StartDate must be whole-second UTC ending in Z")
        start = dt.datetime.fromisoformat(start_date.replace("Z", "+00:00"))
        if start < (now or utc_now()) + dt.timedelta(minutes=2):
            raise ValueError("StartDate needs at least two minutes of disconnect lead")
        if "EndDate" in update:
            end = dt.datetime.fromisoformat(str(update["EndDate"]).replace("Z", "+00:00"))
            if end <= start:
                raise ValueError("preserved EndDate would prevent the requested run")
        update["StartDate"] = start_date
    return update


def prepare_schedule(args):
    original = json.loads(Path(args.source).read_text())
    update = schedule_update(original, args.state, args.start_date)
    write_json(Path(args.output), update)
    print(f"Prepared {args.output}; no AWS request sent. Review the full target and diff before use.")
    return 0


IDENTITY_PATTERNS = {
    "instance_ids": re.compile(r"i-(?:[0-9a-f]{8}|[0-9a-f]{17})"),
    "volume_ids": re.compile(r"vol-(?:[0-9a-f]{8}|[0-9a-f]{17})"),
    "fleet_ids": re.compile(r"fleet-[0-9a-f-]{36}"),
    "request_and_attempt_ids": re.compile(r"[0-9a-f]{32}"),
}
IDENTITY_KEYS = {"instance_id", "instance_ids", "InstanceId", "volume_id", "VolumeId",
                 "resource_id", "request_id", "attempt_id", "parent_attempt_id", "fleet_id"}


def summarize_identities(sources):
    result = {name: set() for name in IDENTITY_PATTERNS}
    deadlines = set()

    def visit(value, key=""):
        if isinstance(value, dict):
            for field, child in value.items():
                visit(child, field)
        elif isinstance(value, list):
            for child in value:
                visit(child, key)
        elif isinstance(value, str):
            if key in IDENTITY_KEYS:
                for kind, pattern in IDENTITY_PATTERNS.items():
                    if pattern.fullmatch(value):
                        result[kind].add(value)
            if key == "expires_at" and value:
                deadlines.add(value)

    for source in sources:
        visit(json.loads(Path(source).read_text()))
    return {"schema_version": 1, "authority": "recovery_index_only",
            "sources": [str(Path(source).resolve()) for source in sources],
            **{key: sorted(values) for key, values in result.items()},
            "expires_at": sorted(deadlines)}


def identities(args):
    write_json(Path(args.output), summarize_identities(args.sources))
    print(args.output)
    return 0


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    init = commands.add_parser("init")
    init.add_argument("--root", default="~/.local/state/devbox/acceptance/04-expiry")
    init.add_argument("--revision", required=True)
    init.set_defaults(run_command=init_run)
    cap = commands.add_parser("capture")
    cap.add_argument("--run", required=True)
    cap.add_argument("--label", required=True)
    cap.add_argument("--cwd", default=os.getcwd())
    cap.add_argument("--timeout", type=float, default=None,
                     help="optional outer timeout in seconds; preserve partial output")
    cap.add_argument("argv", nargs=argparse.REMAINDER)
    cap.set_defaults(run_command=capture)
    schedule = commands.add_parser("prepare-schedule")
    schedule.add_argument("source")
    schedule.add_argument("output")
    schedule.add_argument("--state", choices=("ENABLED", "DISABLED"), required=True)
    schedule.add_argument("--start-date")
    schedule.set_defaults(run_command=prepare_schedule)
    ids = commands.add_parser("identities")
    ids.add_argument("--output", required=True)
    ids.add_argument("sources", nargs="+")
    ids.set_defaults(run_command=identities)
    args = parser.parse_args()
    try:
        return args.run_command(args)
    except (OSError, ValueError) as err:
        print(f"acceptance helper: {err}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
