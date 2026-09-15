#!/usr/bin/env python3
"""Private acceptance captures and schedule-request preparation; no implicit AWS calls.

Only explicit capture/activation commands execute argv, always without a shell. Never pass
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
import time
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


class CatchableSignals:
    """Record the first INT/TERM/HUP; repeated signals cannot abort finalization."""
    def __enter__(self):
        self.received = None
        self.originals = {}
        for number in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
            self.originals[number] = signal.signal(number, self.handle)
        return self

    def handle(self, number, _frame):
        if self.received is None:
            self.received = number

    def __exit__(self, *_args):
        for number, original in self.originals.items():
            signal.signal(number, original)


def stop_process_group(process, sig):
    try:
        os.killpg(process.pid, sig)
    except ProcessLookupError:
        pass
    try:
        process.wait(timeout=2)
    except subprocess.TimeoutExpired:
        pass
    # The direct child can exit before descendants that ignored the first signal.
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    process.wait(timeout=2)
    # Wait for the remaining Linux process-group members to stop before hashing
    # regular files that descendants inherited as stdout/stderr. Zombies cannot
    # write; an uninterruptible live member leaves the capture unverified.
    deadline = time.monotonic() + 2
    while True:
        active = False
        for entry in Path("/proc").iterdir():
            if not entry.name.isdigit():
                continue
            try:
                fields = (entry / "stat").read_text().rsplit(")", 1)[1].split()
                if int(fields[2]) == process.pid and fields[0] not in ("Z", "X"):
                    active = True
                    break
            except (FileNotFoundError, ProcessLookupError, PermissionError):
                continue
        if not active:
            return True
        if time.monotonic() >= deadline:
            return False
        time.sleep(0.01)


def snapshot_inputs(argv, target, cwd):
    rewritten, snapshots = [], []
    for argument in argv:
        prefix = next((p for p in ("file://", "fileb://") if argument.startswith(p)), None)
        if prefix is None:
            rewritten.append(argument)
            continue
        source = Path(argument[len(prefix):])
        if not source.is_absolute():
            source = Path(cwd) / source
        source = source.resolve(strict=True)
        destination = target / ("input-%03d" % len(snapshots))
        with destination.open("xb") as stream:
            stream.write(source.read_bytes())
            stream.flush()
            os.fsync(stream.fileno())
        snapshots.append({"source": str(source), "snapshot": destination.name,
                          "sha256": digest(destination)})
        rewritten.append(prefix + str(destination))
    return rewritten, snapshots


def capture(args, guard=None, after=None):
    run = private_directory(args.run)
    if not re.fullmatch(r"[a-z0-9][a-z0-9_-]{0,79}", args.label):
        raise ValueError("capture label must be a short lowercase filename")
    argv = args.argv[1:] if args.argv[:1] == ["--"] else args.argv
    if not argv:
        raise ValueError("capture needs an explicit command after --")
    if args.timeout is not None and args.timeout <= 0:
        raise ValueError("capture timeout must be positive")
    target = run / "commands" / args.label
    target.mkdir(mode=0o700)
    sync_directory(target.parent)
    code, status, spawned = 1, "failed_to_start", False
    stdout, stderr = target / "stdout", target / "stderr"
    outputs, capture_errors = [], []
    streams_stable = True
    command_exit_code = None
    with CatchableSignals() as signals:
        try:
            argv, inputs = snapshot_inputs(argv, target, args.cwd)
            for value in getattr(args, "output_file", []):
                path = Path(value)
                if not path.is_absolute():
                    path = Path(args.cwd) / path
                path = path.resolve()
                if path.exists():
                    raise ValueError("capture output file already exists; choose a new case/step")
                outputs.append(path)
            write_json(target / "command.json", {
                "argv": argv, "original_argv": args.argv,
                "cwd": str(Path(args.cwd).resolve()), "inputs": inputs,
                "started_at": timestamp(), "status": "started",
            })
            with stdout.open("xb") as out, stderr.open("xb") as err:
                try:
                    if signals.received:
                        code, status = 128 + signals.received, "interrupted_before_dispatch"
                    else:
                        # All copying/persistence precedes the final UTC/hash guard.
                        if guard is not None:
                            guard(argv, target)
                        if signals.received:
                            code, status = 128 + signals.received, "interrupted_before_dispatch"
                        else:
                            deadline = time.monotonic() + args.timeout if args.timeout is not None else None
                            process = subprocess.Popen(argv, cwd=args.cwd, stdout=out, stderr=err,
                                env=getattr(args, "environment", None), start_new_session=True)
                            spawned = True
                            while True:
                                if signals.received:
                                    stop_process_group(process, signals.received)
                                    code, status = 128 + signals.received, "interrupted"
                                    break
                                if deadline is not None and time.monotonic() >= deadline:
                                    stop_process_group(process, signal.SIGTERM)
                                    code, status = 124, "capture_timeout"
                                    break
                                try:
                                    returncode = process.wait(timeout=0.05)
                                    command_exit_code = returncode if returncode >= 0 else 128 - returncode
                                    code, status = command_exit_code, "completed"
                                    # A signal can arrive while wait() returns normally.
                                    # The common shutdown below still drains descendants.
                                    if signals.received:
                                        code, status = 128 + signals.received, "interrupted"
                                    break
                                except subprocess.TimeoutExpired:
                                    pass
                except ValueError as failure:
                    code, status = 2, "dispatch_rejected"
                    err.write((str(failure) + "\n").encode())
                finally:
                    if spawned:
                        try:
                            streams_stable = stop_process_group(process, signals.received or signal.SIGTERM)
                            if not streams_stable:
                                capture_errors.append({"stage": "shutdown", "code": "process_group_still_running"})
                        except (OSError, subprocess.TimeoutExpired) as failure:
                            streams_stable = False
                            capture_errors.append({"stage": "shutdown", "code": type(failure).__name__})
                        if process.returncode is not None:
                            command_exit_code = process.returncode if process.returncode >= 0 else 128 - process.returncode
                        if signals.received:
                            code, status = 128 + signals.received, "interrupted"
                    for name, stream in (("stdout", out), ("stderr", err)):
                        try:
                            stream.flush()
                            os.fsync(stream.fileno())
                        except OSError as failure:
                            capture_errors.append({"stage": name, "code": type(failure).__name__})
        finally:
            artifacts = []
            for number, source in enumerate(outputs):
                item = {"source": str(source), "present": source.exists()}
                if source.exists():
                    destination = target / ("output-%03d" % number)
                    try:
                        with destination.open("xb") as stream:
                            stream.write(source.read_bytes())
                            stream.flush()
                            os.fsync(stream.fileno())
                        item.update(snapshot=destination.name, sha256=digest(destination))
                    except OSError as failure:
                        item["capture_error"] = type(failure).__name__
                        capture_errors.append({"stage": "output_artifact", "source": str(source), "code": type(failure).__name__})
                artifacts.append(item)
            hashes = {}
            for name, source in (("stdout", stdout), ("stderr", stderr)):
                try:
                    hashes[name + "_sha256"] = digest(source) if source.exists() else None
                except OSError as failure:
                    hashes[name + "_sha256"] = None
                    capture_errors.append({"stage": name + "_hash", "code": type(failure).__name__})
            if capture_errors:
                code, status = 1, "evidence_capture_failed"
            result = {
                "finished_at": timestamp(), "exit_code": code, "status": status,
                "command_started": spawned, "command_exit_code": command_exit_code,
                "signal": signals.received, "streams_stable": streams_stable,
                **hashes, "output_artifacts": artifacts, "capture_errors": capture_errors,
            }
            if after is not None:
                after(result)
                code = result["exit_code"]
            write_json(target / "result.json", result)
        print(f"{args.label}: exit {code}; {target}", file=sys.stderr)
        if getattr(args, "passthrough", False) and stdout.exists():
            sys.stdout.buffer.write(stdout.read_bytes())
            sys.stdout.buffer.flush()
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


ACTIVATION_BUDGET = 45
DISCONNECT_MARGIN = 120
ACTIVATION_LEAD = 180  # 45s bounded CLI + shutdown allowance + 120s disconnect.


def activate_schedule(args):
    if not re.fullmatch(r"[0-9a-f]{64}", args.sha256):
        raise ValueError("activation requires the reviewed lowercase SHA-256")
    started = {}

    def guard(argv, target):
        input_arg = argv[argv.index("--cli-input-json") + 1]
        snapshot = Path(input_arg.removeprefix("file://"))
        if digest(snapshot) != args.sha256:
            raise ValueError("reviewed schedule bytes changed; no dispatch")
        request = json.loads(snapshot.read_text())
        start_value = request.get("StartDate")
        if request.get("State") != "ENABLED" or start_value is None:
            raise ValueError("activation requires an enabled request with StartDate")
        schedule_update(request, "ENABLED", start_value)
        start = dt.datetime.fromisoformat(start_value.replace("Z", "+00:00"))
        # No input-copy, disk write, credential loading, or user review after this
        # final sample and before the bounded command starts.
        checked = utc_now()
        if (start - checked).total_seconds() < ACTIVATION_LEAD:
            raise ValueError("activation needs 180 seconds of fresh lead; no dispatch")
        started.update(start=start, checked=checked)

    def after(result):
        result["reviewed_input_sha256"] = args.sha256
        if not result["command_started"]:
            result["activation"] = "not_dispatched"
            return
        result["dispatch_checked_at"] = timestamp(started["checked"])
        result["first_occurrence"] = timestamp(started["start"])
        remaining = (started["start"] - utc_now()).total_seconds()
        result["disconnect_margin_seconds"] = remaining
        if result["exit_code"] == 0 and remaining >= DISCONNECT_MARGIN:
            result["activation"] = "acknowledged_pending_readback"
        else:
            # A failed/slow/interrupted CLI can follow AWS acceptance. Never send
            # a second update automatically or tell the user it is safe to leave.
            result["activation"] = "outcome_unknown_reconcile_before_disconnect"
            if result["exit_code"] == 0:
                result["exit_code"], result["status"] = 1, "activation_margin_lost"

    args.argv = [args.aws_executable, "--profile", args.profile, "--region", args.region,
                 "--cli-connect-timeout", "5", "--cli-read-timeout", "15", "--no-cli-pager",
                 "scheduler", "update-schedule", "--cli-input-json", "file://" + str(Path(args.input).resolve())]
    args.timeout = ACTIVATION_BUDGET
    args.output_file = []
    args.environment = dict(os.environ, AWS_MAX_ATTEMPTS="1", AWS_RETRY_MODE="standard")
    return capture(args, guard=guard, after=after)


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
    cap.add_argument("--passthrough", action="store_true", help="replay captured stdout after finalization")
    cap.add_argument("--output-file", action="append", default=[], help="snapshot an explicit command output file")
    cap.add_argument("argv", nargs=argparse.REMAINDER)
    cap.set_defaults(run_command=capture)
    schedule = commands.add_parser("prepare-schedule")
    schedule.add_argument("source")
    schedule.add_argument("output")
    schedule.add_argument("--state", choices=("ENABLED", "DISABLED"), required=True)
    schedule.add_argument("--start-date")
    schedule.set_defaults(run_command=prepare_schedule)
    activation = commands.add_parser("activate-schedule")
    activation.add_argument("--run", required=True)
    activation.add_argument("--label", required=True)
    activation.add_argument("--cwd", default=os.getcwd())
    activation.add_argument("--input", required=True)
    activation.add_argument("--sha256", required=True)
    activation.add_argument("--profile", required=True)
    activation.add_argument("--region", required=True)
    activation.add_argument("--aws-executable", default="aws")
    activation.set_defaults(run_command=activate_schedule)
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
