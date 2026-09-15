#!/usr/bin/env bash
# Source this file. Commands execute only when an explicit awsc/restore call runs.
# Required: ACCEPTANCE_RUN, CAMPAIGN_DIR, SETUP_PROFILE, REGION, ACCEPTANCE_HELPER.
# AWS_EXECUTABLE may point to an explicit local stand-in for offline tests.

begin_failure_case() {
  umask 077
  local name=$1
  [[ "$name" =~ ^[a-z0-9][a-z0-9-]{0,19}$ ]] || return 2
  local unique
  unique=$(python3 -c 'import uuid; print(uuid.uuid4().hex[:12])') || return
  CASE_ID="${name}-${unique}"
  CASE_DIR="$CAMPAIGN_DIR/cases/$CASE_ID"
  mkdir -p -m 700 "$CAMPAIGN_DIR/cases" || return
  mkdir -m 700 "$CASE_DIR" || return
}

awsc() {
  local step=$1
  shift
  [[ "$step" =~ ^[a-z0-9][a-z0-9-]{0,23}$ ]] || return 2
  : "${CASE_ID:?begin_failure_case first}" "${ACCEPTANCE_RUN:?}" "${ACCEPTANCE_HELPER:?}"
  : "${SETUP_PROFILE:?}" "${REGION:?}"
  local unique
  unique=$(python3 -c 'import uuid; print(uuid.uuid4().hex[:8])') || return
  local label="${CASE_ID}-${step}-${unique}"
  local -a options=()
  if [[ "${1:-}" == --capture-output ]]; then
    options+=(--output-file "$2")
    shift 2
  fi
  AWSC_LAST_CAPTURE="$ACCEPTANCE_RUN/commands/$label"
  python3 "$ACCEPTANCE_HELPER" capture --run "$ACCEPTANCE_RUN" --label "$label" \
    --timeout 45 --passthrough "${options[@]}" -- \
    "${AWS_EXECUTABLE:-aws}" --profile "$SETUP_PROFILE" --region "$REGION" \
    --cli-connect-timeout 5 --cli-read-timeout 15 --no-cli-pager "$@"
}

# Do not use errexit inside recovery: preserve every repair attempt, and never
# re-enable if an earlier restore failed. Source snapshots remain immutable.
restore_step() {
  if awsc "$@" > "$CASE_DIR/$1.json"; then
    return 0
  else
    local code=$?
    RESTORE_FAILURES=$((RESTORE_FAILURES + 1))
    printf 'Restore step %s failed (exit %s); evidence: %s\n' "$1" "$code" "$AWSC_LAST_CAPTURE" >&2
    return "$code"
  fi
}

failure_quiesce() {
  # Keep the function guarded while accepted Scheduler/Lambda attempts settle.
  local count
  for ((count=0; count<60; count++)); do sleep 10 || return; done
}

restore_failure_campaign() {
  begin_failure_case restore || return
  RESTORE_FAILURES=0
  local parked=false
  if restore_step restore-park scheduler update-schedule --cli-input-json "file://$CAMPAIGN_DIR/schedule.parked.json"; then
    parked=true
  fi
  if [[ -f "$CAMPAIGN_DIR/restore-scheduler-policy.armed" ]]; then
    restore_step restore-policy iam put-role-policy --role-name "$SCHED_ROLE_NAME" \
      --policy-name "$SCHED_POLICY" --policy-document "file://$CAMPAIGN_DIR/scheduler-policy.original.json" || :
  fi
  if [[ -f "$CAMPAIGN_DIR/restore-pipe.armed" ]]; then
    local desired
    desired=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["DesiredState"])' "$CAMPAIGN_DIR/pipe.original.json") || desired=INVALID
    case "$desired" in
      RUNNING) restore_step restore-pipe pipes start-pipe --name "$PIPE_NAME" || : ;;
      STOPPED) restore_step restore-pipe pipes stop-pipe --name "$PIPE_NAME" || : ;;
      *) RESTORE_FAILURES=$((RESTORE_FAILURES + 1)) ;;
    esac
  fi
  if [[ -f "$CAMPAIGN_DIR/restore-concurrency.armed" ]]; then
    if [[ "$parked" == true ]] && failure_quiesce; then
      local original
      original=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("ReservedConcurrentExecutions","ABSENT"))' "$CAMPAIGN_DIR/concurrency.original.json") || original=INVALID
      if [[ "$original" == ABSENT ]]; then
        restore_step restore-concurrency lambda delete-function-concurrency --function-name "$FUNCTION_ARN" || :
      elif [[ "$original" =~ ^[0-9]+$ ]]; then
        restore_step restore-concurrency lambda put-function-concurrency --function-name "$FUNCTION_ARN" --reserved-concurrent-executions "$original" || :
      else
        RESTORE_FAILURES=$((RESTORE_FAILURES + 1))
      fi
    else
      RESTORE_FAILURES=$((RESTORE_FAILURES + 1))
    fi
  fi
  # Acknowledgment is not final health. Readbacks/polling and exact comparisons
  # remain required before the separately reviewed normal schedule restoration.
  restore_step read-schedule scheduler get-schedule --name "$SCHEDULE_NAME" --group-name "$SCHEDULE_GROUP" || :
  restore_step read-concurrency lambda get-function-concurrency --function-name "$FUNCTION_ARN" || :
  restore_step read-pipe pipes describe-pipe --name "$PIPE_NAME" || :
  local status=RESTORE_PARKED_REVIEW_REQUIRED
  if ((RESTORE_FAILURES)); then status=RESTORE_FAILED; fi
  python3 - "$CASE_DIR/result.json" "$status" "$RESTORE_FAILURES" <<'PY'
import json,os,sys
with open(sys.argv[1], 'x') as stream:
    json.dump({'status':sys.argv[2], 'failed_steps':int(sys.argv[3]),
               'normal_schedule_restored':False}, stream)
    stream.write('\n');stream.flush();os.fsync(stream.fileno())
PY
  printf '%s: %s\n' "$status" "$CASE_DIR/result.json" >&2
  ((RESTORE_FAILURES == 0))
}

# Save an executable recovery entry point before any mutation. These are paths
# and scoped names only, not credentials. It stays usable after shell/session loss.
prepare_failure_restore() {
  mkdir -m 700 "$CAMPAIGN_DIR/tools" || return
  cp -- "${BASH_SOURCE[0]}" "$CAMPAIGN_DIR/tools/expiry-failure-capture.sh" || return
  cp -- "$ACCEPTANCE_HELPER" "$CAMPAIGN_DIR/tools/expiry-acceptance.py" || return
  python3 - "$CAMPAIGN_DIR/restore.sh" "$ACCEPTANCE_RUN" "$CAMPAIGN_DIR" "$SETUP_PROFILE" "$REGION" \
    "${AWS_EXECUTABLE:-aws}" "$SCHED_ROLE_NAME" "$SCHED_POLICY" "$PIPE_NAME" "$FUNCTION_ARN" "$SCHEDULE_NAME" "$SCHEDULE_GROUP" <<'PY'
import os,shlex,sys
names=['ACCEPTANCE_RUN','CAMPAIGN_DIR','SETUP_PROFILE','REGION','AWS_EXECUTABLE',
       'SCHED_ROLE_NAME','SCHED_POLICY','PIPE_NAME','FUNCTION_ARN','SCHEDULE_NAME','SCHEDULE_GROUP']
with open(sys.argv[1], 'x') as stream:
    stream.write('#!/usr/bin/env bash\numask 077\n')
    for name,value in zip(names,sys.argv[2:]):stream.write(name+'='+shlex.quote(value)+'\n')
    stream.write('ACCEPTANCE_HELPER="$CAMPAIGN_DIR/tools/expiry-acceptance.py"\n')
    stream.write('source "$CAMPAIGN_DIR/tools/expiry-failure-capture.sh"\n')
    stream.write('exec 9>"$CAMPAIGN_DIR/recovery.lock"\nflock -n 9 || exit 1\n')
    stream.write("trap '' INT TERM HUP\nrestore_failure_campaign\n")
    stream.flush();os.fsync(stream.fileno())
os.chmod(sys.argv[1],0o700)
PY
}

arm_failure_restore() {
  [[ -x "$CAMPAIGN_DIR/restore.sh" ]] || return 2
  [[ ! -e "$CAMPAIGN_DIR/finished.json" ]] || return 2
  exec 8>"$CAMPAIGN_DIR/recovery.lock"
  flock -n 8 || return 1
  trap 'failure_restore_trap $?' EXIT
  trap 'failure_restore_trap 130' INT
  trap 'failure_restore_trap 143' TERM
  trap 'failure_restore_trap 129' HUP
  FAILURE_RECOVERY_ARMED=1
}

# End this campaign while it is still parked. Normal schedule restoration is a
# separate reviewed operation after this succeeds; failures retain all traps.
finish_failure_campaign() {
  [[ "${FAILURE_RECOVERY_ARMED:-0}" == 1 ]] || return 2
  restore_failure_campaign || return
  python3 - "$CAMPAIGN_DIR" "$CASE_DIR" "$ACCEPTANCE_HELPER" <<'PY'
import hashlib,importlib.util,json,os,pathlib,sys
sys.dont_write_bytecode=True
campaign,case=map(pathlib.Path,sys.argv[1:3])
spec=importlib.util.spec_from_file_location('acceptance',sys.argv[3])
helper=importlib.util.module_from_spec(spec);spec.loader.exec_module(helper)
def read(path): return json.loads(path.read_text())
expected=read(campaign/'schedule.parked.json')
actual=read(case/'read-schedule.json')
if expected.get('State') != 'DISABLED' or {
    k:v for k,v in actual.items() if k in helper.SCHEDULE_WRITABLE
} != expected:
    raise SystemExit('Finish refused: parked schedule readback differs; recovery remains armed')
key='ReservedConcurrentExecutions'
if read(case/'read-concurrency.json').get(key) != read(campaign/'concurrency.original.json').get(key):
    raise SystemExit('Finish refused: concurrency readback differs; recovery remains armed')
desired=read(campaign/'pipe.original.json')['DesiredState']
pipe=read(case/'read-pipe.json')
if desired not in ('RUNNING','STOPPED') or pipe.get('DesiredState') != desired or pipe.get('CurrentState') != desired:
    raise SystemExit('Finish refused: Pipe has not settled; recovery remains armed')
result=read(case/'result.json')
if result['status'] != 'RESTORE_PARKED_REVIEW_REQUIRED' or result['failed_steps'] != 0:
    raise SystemExit('Finish refused: failed recovery; recovery remains armed')
paths=[case/name for name in ('result.json','read-schedule.json','read-concurrency.json','read-pipe.json')]
with (campaign/'finished.json').open('x') as stream:
    json.dump({'status':'CAMPAIGN_FINISHED_PARKED','normal_schedule_restored':False,
               'readback_sha256':{str(p.relative_to(campaign)):hashlib.sha256(p.read_bytes()).hexdigest() for p in paths}},stream)
    stream.write('\n');stream.flush();os.fsync(stream.fileno())
PY
  local checked=$?
  ((checked == 0)) || return "$checked"
  trap - EXIT INT TERM HUP
  FAILURE_RECOVERY_ARMED=0
  flock -u 8
  exec 8>&-
}

failure_restore_trap() {
  local original=$1
  trap - EXIT
  trap '' INT TERM HUP
  flock -u 8
  "$CAMPAIGN_DIR/restore.sh"
  local restored=$?
  if ((restored != 0)); then original=1; fi
  exit "$original"
}
