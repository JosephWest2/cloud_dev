#!/usr/bin/env python3
"""Controlled local tests only; no AWS clients, credentials, or endpoints."""
import contextlib
import copy
import datetime as dt
import importlib.util
import io
import json
import os
import signal
import subprocess
import time
from unittest import mock
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


    def make_stub(self, directory, delay=0):
        stub = Path(directory) / "aws-stub"
        stub.write_text("#!/usr/bin/env python3\nimport json,pathlib,sys,time\n"
            + "p=pathlib.Path(" + repr(str(directory)) + ")\n"
            + "with (p/'calls').open('a') as f: f.write(json.dumps(sys.argv[1:])+'\\n')\n"
            + "print('{\"ScheduleArn\":\"stand-in\"}',flush=True)\n"
            + "print('stand-in diagnostic',file=sys.stderr,flush=True)\n"
            + "time.sleep(" + repr(delay) + ")\n")
        stub.chmod(0o700)
        return stub

    def activation_args(self, directory, request, label="activate", stub=None):
        path = Path(directory) / (label + ".json")
        path.write_text(json.dumps(request))
        return types.SimpleNamespace(run=directory, label=label, cwd=directory,
            input=str(path), sha256=acceptance.digest(path), profile="test", region="us-east-2",
            aws_executable=str(stub or self.make_stub(directory)))

    def test_activation_rechecks_reviewed_bytes_and_fresh_time_before_standin_dispatch(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); (root / "commands").mkdir(mode=0o700)
            now = acceptance.utc_now()
            prepared = acceptance.schedule_update(self.schedule(), "ENABLED",
                acceptance.timestamp(now - dt.timedelta(minutes=1)), now - dt.timedelta(minutes=5))
            args = self.activation_args(directory, prepared, "stale")
            self.assertEqual(acceptance.activate_schedule(args), 2)
            self.assertFalse((root / "calls").exists())
            stale = json.loads((root / "commands/stale/result.json").read_text())
            self.assertEqual(stale["activation"], "not_dispatched")
            future = acceptance.schedule_update(self.schedule(), "ENABLED",
                acceptance.timestamp(now + dt.timedelta(minutes=5)), now)
            args = self.activation_args(directory, future, "future")
            self.assertEqual(acceptance.activate_schedule(args), 0)
            call = json.loads((root / "calls").read_text().splitlines()[0])
            sent = Path(call[call.index("--cli-input-json") + 1].removeprefix("file://"))
            self.assertEqual(sent.read_bytes(), Path(args.input).read_bytes())
            self.assertNotEqual(sent, Path(args.input))
            args = self.activation_args(directory, future, "changed")
            Path(args.input).write_text(json.dumps(dict(future, Description="changed after review")))
            self.assertEqual(acceptance.activate_schedule(args), 2)
            self.assertEqual(len((root / "calls").read_text().splitlines()), 1)

    def test_activation_lost_ack_is_unknown_without_retry(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory); (root / "commands").mkdir(mode=0o700)
            now=acceptance.utc_now()
            future=acceptance.schedule_update(self.schedule(), "ENABLED", acceptance.timestamp(now+dt.timedelta(minutes=5)), now)
            args=self.activation_args(directory,future,stub=self.make_stub(directory,10))
            with mock.patch.object(acceptance,"ACTIVATION_BUDGET",0.2):
                self.assertEqual(acceptance.activate_schedule(args),124)
            result=json.loads((root/'commands/activate/result.json').read_text())
            self.assertEqual(result['activation'],'outcome_unknown_reconcile_before_disconnect')
            self.assertEqual(len((root/'calls').read_text().splitlines()),1)

    def test_term_hup_int_finalize_partial_evidence_and_stop_descendants_under_repeated_signals(self):
        helper=Path(__file__).with_name('expiry-acceptance.py')
        for first in (signal.SIGTERM,signal.SIGHUP,signal.SIGINT):
            with self.subTest(signal=first), tempfile.TemporaryDirectory() as directory:
                root=Path(directory); (root/'commands').mkdir(mode=0o700)
                child=root/'child.py'
                child.write_text("import os,signal,subprocess,sys,time,pathlib\n"
                    "for s in (signal.SIGINT,signal.SIGTERM,signal.SIGHUP): signal.signal(s,signal.SIG_IGN)\n"
                    "p=subprocess.Popen([sys.executable,'-c','import time; time.sleep(60)'])\n"
                    "print('i-0123456789abcdef0',flush=True)\n"
                    "print('partial stderr',file=sys.stderr,flush=True)\n"
                    "pathlib.Path(sys.argv[1]).write_text(str(os.getpid())+' '+str(p.pid))\n"
                    "time.sleep(60)\n")
                process=subprocess.Popen([sys.executable,str(helper),'capture','--run',directory,
                    '--label','signaled','--',sys.executable,str(child),str(root/'ready')],
                    stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True)
                pids=[]
                try:
                    deadline=time.monotonic()+5
                    while not (root/'ready').exists() and time.monotonic()<deadline: time.sleep(0.01)
                    pids=[int(value) for value in (root/'ready').read_text().split()]
                    os.kill(process.pid,first)
                    for repeated in (signal.SIGHUP,signal.SIGINT,signal.SIGTERM):
                        time.sleep(0.05); os.kill(process.pid,repeated)
                    process.communicate(timeout=8)
                    self.assertEqual(process.returncode,128+first)
                    saved=root/'commands/signaled'
                    result=json.loads((saved/'result.json').read_text())
                    self.assertEqual(result['signal'],first)
                    self.assertEqual(result['stdout_sha256'],acceptance.digest(saved/'stdout'))
                    self.assertEqual(result['stderr_sha256'],acceptance.digest(saved/'stderr'))
                    self.assertIn('i-0123456789abcdef0',(saved/'stdout').read_text())
                    self.assertIn('partial stderr',(saved/'stderr').read_text())
                    for pid in pids:
                        stat=Path(f'/proc/{pid}/stat')
                        self.assertTrue(not stat.exists() or stat.read_text().split()[2]=='Z',f'descendant {pid} still running')
                finally:
                    if process.poll() is None: process.kill(); process.wait()
                    if pids:
                        try: os.killpg(pids[0],signal.SIGKILL)
                        except ProcessLookupError: pass

    def test_sourced_failure_wrapper_preserves_failures_payloads_outputs_and_unique_cases(self):
        wrapper=Path(__file__).with_name('expiry-failure-capture.sh').resolve()
        helper=Path(__file__).with_name('expiry-acceptance.py').resolve()
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory); (root/'commands').mkdir(mode=0o700)
            (root/'campaign').mkdir(mode=0o700)
            stub=root/'aws-stub'
            stub.write_text("#!/usr/bin/env python3\nimport pathlib,sys\n"
                "print('{\"partial\":true}',flush=True)\n"
                "print('denied stand-in',file=sys.stderr,flush=True)\n"
                "pathlib.Path(sys.argv[-1]).write_text('partial output artifact')\n"
                "sys.exit(7)\n")
            stub.chmod(0o700)
            script = """
source "$WRAPPER"
for name in first second; do
  begin_failure_case "$name" || exit
  printf '{"execution_id":"%s"}\\n' "$CASE_ID" > "$CASE_DIR/payload.json"
  awsc invoke --capture-output "$CASE_DIR/response" lambda invoke --payload "fileb://$CASE_DIR/payload.json" "$CASE_DIR/response" > "$CASE_DIR/convenience.json"
  code=$?
  test "$code" -eq 7 || exit 91
done
"""
            environment=dict(os.environ,WRAPPER=str(wrapper),ACCEPTANCE_HELPER=str(helper),
                ACCEPTANCE_RUN=directory,CAMPAIGN_DIR=str(root/'campaign'),SETUP_PROFILE='test',REGION='us-east-2',AWS_EXECUTABLE=str(stub))
            result=subprocess.run(['bash','-c',script],env=environment,capture_output=True,text=True)
            self.assertEqual(result.returncode,0,result.stderr)
            captures=list((root/'commands').iterdir()); self.assertEqual(len(captures),2)
            source_paths=[]
            for saved in captures:
                record=json.loads((saved/'result.json').read_text())
                command=json.loads((saved/'command.json').read_text())
                self.assertEqual(record['exit_code'],7)
                self.assertIn('denied stand-in',(saved/'stderr').read_text())
                self.assertEqual(record['stderr_sha256'],acceptance.digest(saved/'stderr'))
                self.assertEqual((saved/record['output_artifacts'][0]['snapshot']).read_text(),'partial output artifact')
                original=command['inputs'][0]; source_paths.append(original['source'])
                self.assertEqual(original['sha256'],acceptance.digest(saved/original['snapshot']))
            self.assertEqual(len(set(source_paths)),2)

    def test_relative_output_is_resolved_against_command_cwd_and_reuse_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory); (root/'commands').mkdir(mode=0o700)
            cwd=root/'different-cwd'; cwd.mkdir()
            args=types.SimpleNamespace(run=directory,label='relative',cwd=str(cwd),timeout=None,
                output_file=['response.json'],argv=[sys.executable,'-c',
                    "from pathlib import Path; Path('response.json').write_text('relative artifact')"])
            self.assertEqual(acceptance.capture(args),0)
            saved=root/'commands/relative'
            result=json.loads((saved/'result.json').read_text())
            self.assertEqual((saved/result['output_artifacts'][0]['snapshot']).read_text(),'relative artifact')
            args.label='reuse'
            with self.assertRaises(ValueError):
                acceptance.capture(args)
            self.assertEqual((cwd/'response.json').read_text(),'relative artifact')
            self.assertFalse(json.loads((root/'commands/reuse/result.json').read_text())['command_started'])

    def test_saved_restore_continues_after_failed_park_and_never_enables(self):
        wrapper=Path(__file__).with_name('expiry-failure-capture.sh').resolve()
        helper=Path(__file__).with_name('expiry-acceptance.py').resolve()
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory); (root/'commands').mkdir(mode=0o700)
            campaign=root/'campaign';campaign.mkdir(mode=0o700)
            (campaign/'schedule.parked.json').write_text('{"State":"DISABLED"}')
            (campaign/'scheduler-policy.original.json').write_text('{"Statement":[]}')
            (campaign/'restore-scheduler-policy.armed').touch()
            stub=root/'aws-stub'
            stub.write_text("#!/usr/bin/env python3\nimport sys\n"
                "print('{\"partial\":true}',flush=True)\n"
                "print('restore stand-in denial',file=sys.stderr,flush=True)\nsys.exit(7)\n")
            stub.chmod(0o700)
            environment=dict(os.environ,WRAPPER=str(wrapper),ACCEPTANCE_HELPER=str(helper),
                ACCEPTANCE_RUN=directory,CAMPAIGN_DIR=str(campaign),SETUP_PROFILE='test',REGION='us-east-2',
                AWS_EXECUTABLE=str(stub),SCHED_ROLE_NAME='role',SCHED_POLICY='inline',PIPE_NAME='pipe',
                FUNCTION_ARN='function',SCHEDULE_NAME='schedule',SCHEDULE_GROUP='group')
            result=subprocess.run(['bash','-c',
                'source "$WRAPPER"; prepare_failure_restore && "$CAMPAIGN_DIR/restore.sh"'],
                env=environment,capture_output=True,text=True)
            self.assertEqual(result.returncode,1,result.stderr)
            saved=list((root/'commands').iterdir())
            self.assertEqual(len(saved),5)  # Failed park + policy + all three readbacks.
            commands=[json.loads((path/'command.json').read_text()) for path in saved]
            self.assertTrue(any('put-role-policy' in command['argv'] for command in commands))
            self.assertEqual(sum('update-schedule' in command['argv'] for command in commands),1)
            summary=json.loads(next((campaign/'cases').glob('restore-*/result.json')).read_text())
            self.assertEqual(summary['status'],'RESTORE_FAILED')
            self.assertEqual(summary['failed_steps'],5)
            self.assertFalse(summary['normal_schedule_restored'])

    def test_signal_child_exit_race_drains_late_writers_before_final_hashes(self):
        helper=Path(__file__).with_name('expiry-acceptance.py').resolve()
        for trial in range(5):
            with self.subTest(trial=trial), tempfile.TemporaryDirectory() as directory:
                root=Path(directory); (root/'commands').mkdir(mode=0o700)
                child=root/'child.py'
                child.write_text("import os,pathlib,subprocess,sys,time\n"
                    "root=pathlib.Path(sys.argv[1])\n"
                    "p=subprocess.Popen([sys.executable,'-c',"
                    "'import signal,time; signal.signal(signal.SIGTERM,signal.SIG_IGN); "
                    "time.sleep(.3); print(\"late descendant output\",flush=True); time.sleep(30)'])\n"
                    "(root/'ready').write_text(str(os.getpid())+' '+str(p.pid))\n"
                    "print('parent output',flush=True)\n"
                    "while not (root/'finish').exists(): time.sleep(.001)\n")
                process=subprocess.Popen([sys.executable,str(helper),'capture','--run',directory,
                    '--label','race','--',sys.executable,str(child),directory],
                    stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True)
                pids=[]
                try:
                    deadline=time.monotonic()+5
                    while not (root/'ready').exists() and time.monotonic()<deadline: time.sleep(.001)
                    pids=[int(value) for value in (root/'ready').read_text().split()]
                    process.send_signal(signal.SIGTERM)
                    (root/'finish').write_text('finish')
                    process.communicate(timeout=8)
                    self.assertEqual(process.returncode,143)
                    saved=root/'commands/race'
                    result=json.loads((saved/'result.json').read_text())
                    self.assertEqual(result['status'],'interrupted')
                    self.assertTrue(result['streams_stable'])
                    time.sleep(.4)  # A surviving descendant would append after hashing.
                    self.assertEqual(result['stdout_sha256'],acceptance.digest(saved/'stdout'))
                    stat=Path(f'/proc/{pids[1]}/stat')
                    self.assertTrue(not stat.exists() or stat.read_text().split()[2]=='Z')
                finally:
                    if process.poll() is None: process.kill();process.wait()
                    if pids:
                        try: os.killpg(pids[0],signal.SIGKILL)
                        except ProcessLookupError: pass

    def test_output_artifact_error_keeps_command_exit_partial_streams_and_final_metadata(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory); (root/'commands').mkdir(mode=0o700)
            args=types.SimpleNamespace(run=directory,label='bad-artifact',cwd=directory,timeout=None,
                output_file=['artifact'],argv=[sys.executable,'-c',
                    "import pathlib,sys; print('partial stdout'); print('partial stderr',file=sys.stderr); "
                    "pathlib.Path('artifact').mkdir();sys.exit(7)"])
            self.assertEqual(acceptance.capture(args),1)
            saved=root/'commands/bad-artifact'
            result=json.loads((saved/'result.json').read_text())
            self.assertEqual(result['command_exit_code'],7)
            self.assertEqual(result['status'],'evidence_capture_failed')
            self.assertEqual(result['output_artifacts'][0]['capture_error'],'IsADirectoryError')
            self.assertEqual(result['stdout_sha256'],acceptance.digest(saved/'stdout'))
            self.assertEqual(result['stderr_sha256'],acceptance.digest(saved/'stderr'))
            self.assertIn('partial stdout',(saved/'stdout').read_text())
            self.assertIn('partial stderr',(saved/'stderr').read_text())


    def test_campaign_finish_disarms_only_after_verified_parked_recovery(self):
        wrapper=Path(__file__).with_name('expiry-failure-capture.sh').resolve()
        helper=Path(__file__).with_name('expiry-acceptance.py').resolve()
        for scenario in ('success','failed-park','unsettled-pipe'):
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as directory:
                root=Path(directory); (root/'commands').mkdir(mode=0o700)
                campaign=root/'campaign'; campaign.mkdir(mode=0o700)
                parked=acceptance.schedule_update(self.schedule(),'DISABLED')
                (campaign/'schedule.parked.json').write_text(json.dumps(parked))
                (campaign/'schedule.restore.json').write_text(json.dumps(dict(parked,State='ENABLED')))
                (campaign/'concurrency.original.json').write_text('{"ReservedConcurrentExecutions":1}')
                (campaign/'pipe.original.json').write_text('{"DesiredState":"RUNNING"}')
                for resource in ('concurrency','pipe'):
                    (campaign/f'restore-{resource}.armed').touch()
                state=root/'state.json'
                state.write_text(json.dumps({'schedule':dict(parked,State='ENABLED'),'concurrency':0,'pipe':'STOPPED'}))
                stub=root/'aws-stub'
                stub.write_text('''#!/usr/bin/env python3
import json,os,pathlib,sys
root=pathlib.Path(os.environ['STUB_ROOT']); args=sys.argv[1:]
p=root/'state.json'; state=json.loads(p.read_text()); output={}
with (root/'calls').open('a') as f:f.write(json.dumps(args)+'\\n')
def value(flag):return args[args.index(flag)+1]
if 'update-schedule' in args:
    request=json.loads(pathlib.Path(value('--cli-input-json').removeprefix('file://')).read_text())
    if request['State']=='DISABLED' and os.environ['SCENARIO']=='failed-park':sys.exit(7)
    state['schedule']=request
elif 'put-function-concurrency' in args:state['concurrency']=int(value('--reserved-concurrent-executions'))
elif 'delete-function-concurrency' in args:state['concurrency']=None
elif 'start-pipe' in args:state['pipe']='RUNNING'
elif 'stop-pipe' in args:state['pipe']='STOPPED'
elif 'get-schedule' in args:output=dict(state['schedule'],Arn='stand-in')
elif 'get-function-concurrency' in args:output={'ReservedConcurrentExecutions':state['concurrency']}
elif 'describe-pipe' in args:
    output={'DesiredState':state['pipe'],'CurrentState':'STARTING' if os.environ['SCENARIO']=='unsettled-pipe' else state['pipe']}
else:sys.exit(91)
p.write_text(json.dumps(state));print(json.dumps(output))
''')
                stub.chmod(0o700)
                sleep=root/'sleep'; sleep.write_text('#!/bin/sh\nexit 0\n'); sleep.chmod(0o700)
                environment=dict(os.environ,WRAPPER=str(wrapper),ACCEPTANCE_HELPER=str(helper),
                    ACCEPTANCE_RUN=directory,CAMPAIGN_DIR=str(campaign),SETUP_PROFILE='test',REGION='us-east-2',
                    AWS_EXECUTABLE=str(stub),STUB_ROOT=directory,SCENARIO=scenario,PATH=directory+':'+os.environ['PATH'],
                    SCHED_ROLE_NAME='role',SCHED_POLICY='policy',PIPE_NAME='pipe',FUNCTION_ARN='function',
                    SCHEDULE_NAME='dev',SCHEDULE_GROUP='dev')
                result=subprocess.run(['bash','-c','''
set -e
source "$WRAPPER"
prepare_failure_restore
arm_failure_restore
if finish_failure_campaign; then
  test "$SCENARIO" = success
  test "$FAILURE_RECOVERY_ARMED" = 0
  test -z "$(trap -p EXIT INT TERM HUP)"
  if arm_failure_restore; then exit 92; fi
  awsc normal-enable scheduler update-schedule --cli-input-json "file://$CAMPAIGN_DIR/schedule.restore.json"
  awsc verify-enable scheduler get-schedule --name dev --group-name dev > "$CAMPAIGN_DIR/normal.json"
  exit 0
else
  test "$SCENARIO" != success
  test "$FAILURE_RECOVERY_ARMED" = 1
  test -n "$(trap -p EXIT)"
  test ! -e "$CAMPAIGN_DIR/finished.json"
  exit 7
fi
'''],env=environment,capture_output=True,text=True,timeout=20)
                self.assertEqual(result.returncode,0 if scenario=='success' else (1 if scenario=='failed-park' else 7),result.stderr)
                final=json.loads(state.read_text())
                restores=list((campaign/'cases').glob('restore-*/result.json'))
                calls=[json.loads(line) for line in (root/'calls').read_text().splitlines()]
                if scenario=='success':
                    self.assertEqual(final['schedule']['State'],'ENABLED')
                    self.assertEqual(json.loads((campaign/'normal.json').read_text())['State'],'ENABLED')
                    self.assertEqual(len(restores),1,'normal EXIT must not run recovery again')
                    self.assertEqual(sum('update-schedule' in call for call in calls),2)
                    self.assertEqual(json.loads((campaign/'finished.json').read_text())['status'],'CAMPAIGN_FINISHED_PARKED')
                else:
                    self.assertEqual(len(restores),2,'failed finish must run generated restore on EXIT')
                    self.assertFalse((campaign/'finished.json').exists())
                    self.assertFalse((campaign/'normal.json').exists())
                    if scenario=='failed-park':
                        self.assertEqual(final['concurrency'],0,'failed parking must retain concurrency guard')
                        self.assertFalse(any('put-function-concurrency' in call for call in calls))
                    else:self.assertEqual(final['schedule']['State'],'DISABLED')
                self.assertTrue((campaign/'restore.sh').is_file())
                self.assertEqual((campaign/'tools/expiry-failure-capture.sh').read_bytes(),wrapper.read_bytes())


if __name__ == "__main__":
    unittest.main()
