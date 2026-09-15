package foundation

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
)

func (f *fake) GetFunctionConfiguration(ctx context.Context, in *lambda.GetFunctionConfigurationInput, _ ...func(*lambda.Options)) (*lambda.GetFunctionConfigurationOutput, error) {
	out := &lambda.GetFunctionConfigurationOutput{}
	return out, f.respond("GetFunctionConfiguration", in, out)
}
func (f *fake) GetFunctionConcurrency(ctx context.Context, in *lambda.GetFunctionConcurrencyInput, _ ...func(*lambda.Options)) (*lambda.GetFunctionConcurrencyOutput, error) {
	out := &lambda.GetFunctionConcurrencyOutput{}
	return out, f.respond("GetFunctionConcurrency", in, out)
}
func (f *fake) GetFunctionEventInvokeConfig(ctx context.Context, in *lambda.GetFunctionEventInvokeConfigInput, _ ...func(*lambda.Options)) (*lambda.GetFunctionEventInvokeConfigOutput, error) {
	out := &lambda.GetFunctionEventInvokeConfigOutput{}
	return out, f.respond("GetFunctionEventInvokeConfig", in, out)
}
func (f *fake) GetSchedule(ctx context.Context, in *scheduler.GetScheduleInput, _ ...func(*scheduler.Options)) (*scheduler.GetScheduleOutput, error) {
	out := &scheduler.GetScheduleOutput{}
	return out, f.respond("GetSchedule", in, out)
}
func (f *fake) DescribeLogGroups(ctx context.Context, in *cloudwatchlogs.DescribeLogGroupsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
	out := &cloudwatchlogs.DescribeLogGroupsOutput{}
	return out, f.respond("DescribeLogGroups", in, out)
}

func cleanupFixture(t *testing.T) (*fake, config.Manifest, config.Cleanup) {
	t.Helper()
	f, m, _ := fixture(t)
	m.SchemaVersion = 6
	name := "devbox-" + m.Deployment + "-" + m.Owner
	region := m.Region + ":" + m.Account
	logName := "/aws/lambda/" + name + "-cleanup"
	env := map[string]string{"DEVBOX_ACCOUNT": m.Account, "DEVBOX_REGION": m.Region, "DEVBOX_DEPLOYMENT": m.Deployment, "DEVBOX_OWNER": m.Owner, "DEVBOX_LOG_GROUP": logName}
	envJSON, _ := json.Marshal(env)
	c := config.Cleanup{SchemaVersion: 1, Function: config.CleanupFunction{ARN: "arn:aws:lambda:" + region + ":function:" + name + "-cleanup", Runtime: "provided.al2023", Architecture: "x86_64", CodeSHA256: strings.Repeat("a", 64), EnvironmentSHA256: digest(envJSON), TimeoutSeconds: 180, ServiceTimeoutSeconds: 165, ReservedConcurrency: 1, MemoryMB: 256, AsyncMaxAgeSeconds: 300}, Schedule: config.CleanupSchedule{ARN: "arn:aws:scheduler:" + region + ":schedule/" + name + "-cleanup/" + name + "-cleanup", Name: name + "-cleanup", GroupName: name + "-cleanup", GroupARN: "arn:aws:scheduler:" + region + ":schedule-group/" + name + "-cleanup", State: "DISABLED", Expression: "rate(5 minutes)", FlexibleWindow: "OFF", MaxAgeSeconds: 300, RetryAttempts: 2, InputSHA256: digest([]byte(`{"schema_version":1}`))}, Logs: config.CleanupLogs{Name: logName, ARN: "arn:aws:logs:" + region + ":log-group:" + logName, RetentionDays: 30}}
	for suffix, r := range map[string]*config.Role{"cleanup": &c.ExecutionRole, "schedule": &c.SchedulerRole} {
		*r = m.Roles["instance"]
		r.ARN = "arn:aws:iam::" + m.Account + ":role/" + name + "-" + suffix
		r.PolicyName = "devbox-" + suffix
	}
	hash, _ := hex.DecodeString(c.Function.CodeSHA256)
	write := func(key string, value any) { b, _ := json.Marshal(value); f.responses[key] = string(b) }
	write("GetFunctionConfiguration", map[string]any{"FunctionArn": c.Function.ARN, "Runtime": c.Function.Runtime, "Architectures": []string{c.Function.Architecture}, "Handler": "bootstrap", "Role": c.ExecutionRole.ARN, "CodeSha256": base64.StdEncoding.EncodeToString(hash), "Timeout": 180, "MemorySize": 256, "State": "Active", "LastUpdateStatus": "Successful", "Environment": map[string]any{"Variables": env}})
	write("GetFunctionConcurrency", map[string]int{"ReservedConcurrentExecutions": 1})
	write("GetFunctionEventInvokeConfig", map[string]int{"MaximumEventAgeInSeconds": 300, "MaximumRetryAttempts": 0})
	write("GetSchedule", map[string]any{"Arn": c.Schedule.ARN, "Name": c.Schedule.Name, "GroupName": c.Schedule.GroupName, "State": "DISABLED", "ScheduleExpression": "rate(5 minutes)", "FlexibleTimeWindow": map[string]string{"Mode": "OFF"}, "Target": map[string]any{"Arn": c.Function.ARN, "RoleArn": c.SchedulerRole.ARN, "Input": `{"schema_version":1}`, "RetryPolicy": map[string]int{"MaximumEventAgeInSeconds": 300, "MaximumRetryAttempts": 2}}})
	write("DescribeLogGroups", map[string]any{"LogGroups": []map[string]any{{"LogGroupName": logName, "Arn": c.Logs.ARN + ":*", "RetentionInDays": 30}}})
	original := f.hook
	f.hook = func(method string, in any) (string, bool) {
		b, _ := json.Marshal(in)
		var fields map[string]any
		_ = json.Unmarshal(b, &fields)
		role, _ := fields["RoleName"].(string)
		if method == "ListRolePolicies" || method == "GetRolePolicy" {
			doc, _ := original(method, in)
			for _, r := range []config.Role{c.ExecutionRole, c.SchedulerRole} {
				if role == roleName(r.ARN) {
					doc = strings.ReplaceAll(doc, `"PolicyName":"`+role+`"`, `"PolicyName":"`+r.PolicyName+`"`)
					doc = strings.ReplaceAll(doc, `"PolicyNames":["`+role+`"]`, `"PolicyNames":["`+r.PolicyName+`"]`)
				}
			}
			return doc, true
		}
		return original(method, in)
	}
	m.Cleanup, _ = json.Marshal(c)
	return f, m, c
}

func TestCleanupHealthSeparatesConfigurationFromReadiness(t *testing.T) {
	f, m, c := cleanupFixture(t)
	checks := VerifyCleanup(context.Background(), CleanupClients{f, f, f, f}, m, m.Cleanup)
	for _, check := range checks {
		wantFail := check.Name == "cleanup_enabled" || check.Name == "cleanup_evidence"
		if (check.Err != nil) != wantFail {
			t.Fatalf("%s: %v", check.Name, check.Err)
		}
	}
	if len(checks) != 6 {
		t.Fatal(checks)
	}
	if aws.ToString(f.inputs["GetSchedule"].(*scheduler.GetScheduleInput).Name) != c.Schedule.Name || aws.ToString(f.inputs["GetFunctionConfiguration"].(*lambda.GetFunctionConfigurationInput).FunctionName) != c.Function.ARN {
		t.Fatal("unscoped health request")
	}
	c.Schedule.State = "ENABLED"
	c.Evidence = json.RawMessage(`{"schema_version":1}`)
	m.Cleanup, _ = json.Marshal(c)
	checks = VerifyCleanup(context.Background(), CleanupClients{}, m, m.Cleanup)
	if checks[len(checks)-1].Name != "cleanup_evidence" || checks[len(checks)-1].Err == nil {
		t.Fatal("descriptor alone claimed readiness")
	}
}
func TestCleanupHealthRejectsDrift(t *testing.T) {
	for _, tc := range []struct{ method, from, to, check string }{
		{"GetFunctionConfiguration", "provided.al2023", "provided.al2", "cleanup_function"},
		{"GetFunctionConfiguration", "x86_64", "arm64", "cleanup_function"},
		{"GetFunctionConfiguration", "\"Timeout\":180", "\"Timeout\":900", "cleanup_function"},
		{"GetFunctionConfiguration", "\"DEVBOX_OWNER\":", "\"UNTRUSTED_OWNER\":", "cleanup_function"},
		{"GetFunctionConcurrency", ":1", ":0", "cleanup_function"},
		{"GetFunctionEventInvokeConfig", "\"MaximumRetryAttempts\":0", "\"MaximumRetryAttempts\":2", "cleanup_function"},
		{"GetSchedule", "rate(5 minutes)", "rate(1 hour)", "cleanup_schedule"},
		{"GetSchedule", "\"Mode\":\"OFF\"", "\"Mode\":\"FLEXIBLE\"", "cleanup_schedule"},
		{"GetSchedule", "\"MaximumRetryAttempts\":2", "\"MaximumRetryAttempts\":185", "cleanup_schedule"},
		{"DescribeLogGroups", "\"RetentionInDays\":30", "\"RetentionInDays\":0", "cleanup_logs"},
	} {
		t.Run(tc.method+tc.from, func(t *testing.T) {
			f, m, _ := cleanupFixture(t)
			before := f.responses[tc.method]
			f.responses[tc.method] = strings.ReplaceAll(before, tc.from, tc.to)
			if before == f.responses[tc.method] {
				t.Fatal("bad fixture mutation")
			}
			checks := VerifyCleanup(context.Background(), CleanupClients{f, f, f, f}, m, m.Cleanup)
			for _, c := range checks {
				if c.Name == tc.check && c.Err == nil {
					t.Fatal("drift accepted")
				}
			}
		})
	}
}
func TestCleanupDescriptorRejectsScopeAndBounds(t *testing.T) {
	for _, mutate := range []func(*config.Cleanup){func(c *config.Cleanup) {
		c.Function.ARN = strings.Replace(c.Function.ARN, "123456789012", "999999999999", 1)
	}, func(c *config.Cleanup) { c.Function.CodeSHA256 = "bad" }, func(c *config.Cleanup) { c.Function.TimeoutSeconds = 900 }, func(c *config.Cleanup) { c.Schedule.GroupARN = c.Schedule.ARN }, func(c *config.Cleanup) { c.Schedule.RetryAttempts = 185 }, func(c *config.Cleanup) { c.Logs.RetentionDays = 0 }, func(c *config.Cleanup) { c.ExecutionRole = c.SchedulerRole }, func(c *config.Cleanup) { c.Evidence = json.RawMessage(`{"schema_version":2}`) }} {
		f, m, c := cleanupFixture(t)
		mutate(&c)
		raw, _ := json.Marshal(c)
		checks := VerifyCleanup(context.Background(), CleanupClients{f, f, f, f}, m, raw)
		if len(checks) != 1 || checks[0].Err == nil || len(f.calls) != 0 {
			t.Fatal("invalid descriptor reached AWS", checks, f.calls)
		}
	}
}
