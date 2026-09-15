#!/usr/bin/env python3
"""Controlled local tests only; no AWS clients, credentials, or endpoints."""
import contextlib
import copy
import datetime as dt
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import types
import unittest
import sys

sys.dont_write_bytecode = True

spec = importlib.util.spec_from_file_location("acceptance", Path(__file__).with_name("expiry-acceptance.py"))
acceptance = importlib.util.module_from_spec(spec)
spec.loader.exec_module(acceptance)


class AcceptanceHelperTests(unittest.TestCase):
    def schedule(self):
        return {
            "Arn": "arn:aws:scheduler:us-east-2:123456789012:schedule/dev/dev",
            "Name": "dev", "GroupName": "dev", "State": "DISABLED",
            "ScheduleExpression": "rate(5 minutes)", "ScheduleExpressionTimezone": "UTC",
            "FlexibleTimeWindow": {"Mode": "OFF"}, "ActionAfterCompletion": "NONE",
            "Description": "preserve this", "KmsKeyArn": "test-key",
            "CreationDate": "2026-09-14T00:00:00Z", "LastModificationDate": "2026-09-14T00:00:00Z",
            "Target": {"Arn": "function", "RoleArn": "role",
                       "Input": '{"execution_id":"<aws.scheduler.execution-id>"}',
                       "DeadLetterConfig": {"Arn": "queue"},
                       "RetryPolicy": {"MaximumEventAgeInSeconds": 300, "MaximumRetryAttempts": 2}},
        }

    def test_schedule_preserves_every_optional_setting_and_input_bytes(self):
        original = self.schedule()
        saved = copy.deepcopy(original)
        now = dt.datetime(2026, 9, 14, tzinfo=dt.timezone.utc)
        update = acceptance.schedule_update(original, "ENABLED", "2026-09-14T00:04:00Z", now)
        self.assertEqual(original, saved)
        for key in acceptance.SCHEDULE_WRITABLE - {"State", "StartDate"}:
            if key in original:
                self.assertEqual(update[key], original[key])
        self.assertEqual(update["State"], "ENABLED")
        self.assertEqual(update["StartDate"], "2026-09-14T00:04:00Z")
        self.assertFalse(set(update) & acceptance.SCHEDULE_METADATA)

    def test_schedule_rejects_unsafe_timing_and_unknown_fields(self):
        now = dt.datetime(2026, 9, 14, tzinfo=dt.timezone.utc)
        for start in (None, "2026-09-14T00:01:59Z", "2026-09-14T00:04:00+00:00"):
            with self.assertRaises(ValueError):
                acceptance.schedule_update(self.schedule(), "ENABLED", start, now)
        unknown = self.schedule()
        unknown["NewImportantAWSSetting"] = True
        with self.assertRaises(ValueError):
            acceptance.schedule_update(unknown, "DISABLED")
        expired = self.schedule()
        expired["EndDate"] = "2026-09-14T00:03:00Z"
        with self.assertRaises(ValueError):
            acceptance.schedule_update(expired, "ENABLED", "2026-09-14T00:04:00Z", now)

    def test_capture_preserves_argv_partial_output_status_and_prior_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "commands").mkdir(mode=0o700)
            literal = "$(touch SHOULD_NOT_EXIST); 'literal'"
            args = types.SimpleNamespace(run=directory, label="failed-command", cwd=directory,
                timeout=None, argv=[sys.executable, "-c", "import sys; print(sys.argv[1]); sys.exit(3)", literal])
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(acceptance.capture(args), 3)
            saved = root / "commands" / args.label
            self.assertEqual((saved / "stdout").read_text(), literal + "\n")
            self.assertFalse((root / "SHOULD_NOT_EXIST").exists())
            self.assertEqual(json.loads((saved / "result.json").read_text())["exit_code"], 3)
            with self.assertRaises(FileExistsError):
                acceptance.capture(args)

    def test_capture_timeout_keeps_known_ids(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "commands").mkdir(mode=0o700)
            args = types.SimpleNamespace(run=directory, label="partial", cwd=directory,
                timeout=0.2, argv=[sys.executable, "-c", "import time; print('i-0123456789abcdef0', flush=True); time.sleep(60)"])
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(acceptance.capture(args), 124)
            self.assertIn("i-0123456789abcdef0", (root / "commands/partial/stdout").read_text())
            self.assertEqual(json.loads((root / "commands/partial/result.json").read_text())["status"], "capture_timeout")

    def test_identity_index_includes_attempt_only_ids_without_claiming_authority(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "partial.json"
            source.write_text(json.dumps({"request_id": "a" * 32,
                "attempts": [{"attempt_id": "b" * 32, "instance_ids": ["i-0123456789abcdef0"]}],
                "instances": [], "untrusted_message": "i-11111111111111111"}))
            summary = acceptance.summarize_identities([source])
            self.assertEqual(summary["instance_ids"], ["i-0123456789abcdef0"])
            self.assertEqual(summary["request_and_attempt_ids"], ["a" * 32, "b" * 32])
            self.assertEqual(summary["authority"], "recovery_index_only")


if __name__ == "__main__":
    unittest.main()
