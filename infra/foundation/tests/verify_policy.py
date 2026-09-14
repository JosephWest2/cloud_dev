#!/usr/bin/env python3
"""Read-only IAM simulation of the policy rendered by OpenTofu's mock provider.

This supplements static checks; it cannot prove EC2's actual authorization
context, dependent-action behavior, Spot capacity, or restricted-role launches.
Run with a setup profile allowed to call iam:SimulateCustomPolicy. Fixture ARNs
are synthetic and no resources are created, tagged, passed, or terminated.
"""

import argparse
import copy
import json
from pathlib import Path
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tofu-output", required=True)
    parser.add_argument("--aws-profile", required=True)
    args = parser.parse_args()
    policy = manifest = None
    for line in Path(args.tofu_output).read_text().splitlines():
        event = json.loads(line)
        if event.get("type") != "test_state":
            continue
        values = event["test_state"]["values"]
        if "deployment_manifest" not in values.get("outputs", {}):
            continue
        for resource in values["root_module"]["resources"]:
            if resource["address"] == "aws_iam_role_policy.operator":
                policy = resource["values"]["policy"]
                manifest = values["outputs"]["deployment_manifest"]["value"]
    if not policy or manifest["schema_version"] != 5:
        raise RuntimeError("expected a mock-applied v5 manifest and rendered operator policy")

    region, account = manifest["region"], manifest["account"]
    ec2 = f"arn:aws:ec2:{region}:{account}"
    image = manifest["images"]["agent"]
    template = f"{ec2}:launch-template/{image['launch_template_id']}"
    subnet = f"{ec2}:subnet/{manifest['subnet_ids'][0]}"
    instance, volume, fleet = (f"{ec2}:{kind}/fixture" for kind in ("instance", "volume", "fleet"))
    tags = {
        "ManagedBy": "devbox", "Deployment": manifest["deployment"], "Owner": manifest["owner"],
        "Profile": "agent", "Name": "smoke", "BaseName": "smoke", "NamingVersion": "1",
        "RequestId": "a" * 32, "BatchId": "a" * 32, "AttemptId": "b" * 32,
        "CreatedAt": "2026-09-14T00:00:00Z", "Group": "smoke-batch",
    }
    contexts = {"aws:RequestedRegion": region, "ec2:InstanceProfile": manifest["instance_profile_arn"],
                "ec2:LaunchTemplate": template, "ec2:Subnet": subnet,
                "ec2:MetadataHttpTokens": "required", "ec2:InstanceMarketType": "spot",
                "ec2:Encrypted": True, "ec2:VolumeType": "gp3", "ec2:AssociatePublicIpAddress": True}
    # IAM can report keys referenced by unrelated statements on a denial. Do
    # not fill those keys with a global launch context: that concealed EC2's
    # preliminary CreateFleet checks, which lack launch tags and properties.
    known_context_keys = set(contexts) | {
        "ec2:CreateAction", "iam:PassedToService", "ssm:SessionDocumentAccessCheck",
        "s3:prefix", "aws:TagKeys",
    }
    known_context_keys.update("aws:RequestTag/" + key for key in tags)
    for prefix in ["ec2:ResourceTag/", "ssm:resourceTag/"]:
        known_context_keys.update(prefix + key for key in ["ManagedBy", "Owner", "Deployment"])
    cases = []

    def launch_case(name, allowed, api="CreateFleet", mutate=None):
        ctx, request_tags = copy.deepcopy(contexts), tags.copy()
        requested_template, requested_subnet = template, subnet
        if mutate:
            requested_template, requested_subnet = mutate(ctx, request_tags, template, subnet)
        ctx.update({"aws:RequestTag/" + key: value for key, value in request_tags.items()})
        ctx["aws:TagKeys"] = list(request_tags)
        dependencies = [f"arn:aws:ec2:{region}::image/{image['ami_id']}", requested_template, requested_subnet]
        operations = []
        if api == "CreateFleet":
            # The live instant-Fleet dry run authorizes volume/* before it has
            # template properties or tags. Model all preliminary dependencies
            # conservatively with region only; the fleet itself has its tags.
            operations.append(("ec2:CreateFleet", [instance, volume] + dependencies,
                               {"aws:RequestedRegion": region}))
            fleet_context = {key: value for key, value in ctx.items()
                             if key == "aws:RequestedRegion" or key == "aws:TagKeys" or key.startswith("aws:RequestTag/")}
            operations.append(("ec2:CreateFleet", [fleet], fleet_context))
        # AWS checks template-contained resources through RunInstances for
        # Fleet too. Every check must pass, including the actual NIC and volume
        # constraints; a permissive preliminary check alone cannot launch.
        dependencies.extend([f"{ec2}:security-group/{manifest['security_group_id']}",
                             f"{ec2}:network-interface/fixture"])
        operations.append(("ec2:RunInstances", [instance, volume] + dependencies, ctx))
        tagged = [instance, volume] + ([fleet] if api == "CreateFleet" else [])
        operations.append(("ec2:CreateTags", tagged, {**ctx, "ec2:CreateAction": api}))
        cases.append((name, allowed, operations))

    launch_case("Spot creation authorization chain", True)
    launch_case("On-Demand creation authorization chain", True, mutate=lambda c, t, lt, sn: (c.update({"ec2:InstanceMarketType": "on-demand"}) or lt, sn))
    launch_case("legacy seven-tag RunInstances", True, api="RunInstances", mutate=lambda c, t, lt, sn: (
        [t.pop(k) for k in ["BaseName", "NamingVersion", "BatchId", "AttemptId", "Group"]] and lt, sn))
    for tag in ["ManagedBy", "Owner", "Deployment", "Profile", "NamingVersion"]:
        def wrong_tag(c, t, lt, sn, key=tag):
            t[key] = "foreign"
            return lt, sn
        launch_case("reject wrong " + tag, False, mutate=wrong_tag)
    for tag in ["RequestId", "AttemptId", "BaseName", "BatchId"]:
        def missing_tag(c, t, lt, sn, key=tag):
            t.pop(key)
            return lt, sn
        launch_case("reject missing " + tag, False, mutate=missing_tag)
    launch_case("reject foreign subnet", False, mutate=lambda c, t, lt, sn: (c.update({"ec2:Subnet": f"{ec2}:subnet/foreign"}) or lt, f"{ec2}:subnet/foreign"))
    launch_case("reject foreign template", False, mutate=lambda c, t, lt, sn: (c.update({"ec2:LaunchTemplate": f"{ec2}:launch-template/foreign"}) or f"{ec2}:launch-template/foreign", sn))
    launch_case("reject unencrypted volume", False, mutate=lambda c, t, lt, sn: (c.update({"ec2:Encrypted": False}) or lt, sn))
    launch_case("reject non-gp3 volume", False, mutate=lambda c, t, lt, sn: (c.update({"ec2:VolumeType": "gp2"}) or lt, sn))
    launch_case("reject foreign instance profile", False, mutate=lambda c, t, lt, sn: (c.update({"ec2:InstanceProfile": f"arn:aws:iam::{account}:instance-profile/foreign"}) or lt, sn))
    launch_case("reject missing template during Fleet worker launch", False, mutate=lambda c, t, lt, sn: (c.pop("ec2:LaunchTemplate") and lt, sn))
    launch_case("reject IMDSv1 Fleet worker launch", False, mutate=lambda c, t, lt, sn: (c.update({"ec2:MetadataHttpTokens": "optional"}) or lt, sn))
    launch_case("reject private NIC Fleet worker launch", False, mutate=lambda c, t, lt, sn: (c.update({"ec2:AssociatePublicIpAddress": False}) or lt, sn))
    launch_case("reject IMDSv1 RunInstances", False, api="RunInstances", mutate=lambda c, t, lt, sn: (c.update({"ec2:MetadataHttpTokens": "optional"}) or lt, sn))
    launch_case("reject private NIC RunInstances", False, api="RunInstances", mutate=lambda c, t, lt, sn: (c.update({"ec2:AssociatePublicIpAddress": False}) or lt, sn))
    owned = {"aws:RequestedRegion": region, **{"ec2:ResourceTag/" + k: tags[k] for k in ["ManagedBy", "Owner", "Deployment"]}}
    cases.append(("scoped termination", True, [("ec2:TerminateInstances", [instance], owned)]))
    for key in ["ManagedBy", "Owner", "Deployment"]:
        cases.append(("reject foreign termination " + key, False, [("ec2:TerminateInstances", [instance], {**owned, "ec2:ResourceTag/" + key: "foreign"})]))
    role = manifest["roles"]["instance"]["arn"]
    cases.extend([
        ("pass approved worker role", True, [("iam:PassRole", [role], {"iam:PassedToService": "ec2.amazonaws.com"})]),
        ("reject foreign role", False, [("iam:PassRole", [f"arn:aws:iam::{account}:role/foreign"], {"iam:PassedToService": "ec2.amazonaws.com"})]),
        ("reject foreign pass service", False, [("iam:PassRole", [role], {"iam:PassedToService": "lambda.amazonaws.com"})]),
    ])
    # The custom-policy API evaluates supplied IAM context, not a real EC2 call.
    # Every required authorization in the modeled chain must allow it.
    with tempfile.TemporaryDirectory(prefix="devbox-policy-simulation-") as directory:
        request_file = Path(directory) / "request.json"
        for name, expected, operations in cases:
            decisions = []
            rejected = []
            for action, resources, context in operations:
                entries = []
                for key, value in context.items():
                    value_type = "boolean" if isinstance(value, bool) else "stringList" if isinstance(value, list) else "string"
                    entries.append({"ContextKeyName": key, "ContextKeyType": value_type,
                                    "ContextKeyValues": value if isinstance(value, list) else [str(value).lower() if isinstance(value, bool) else value]})
                request_file.write_text(json.dumps({"PolicyInputList": [policy], "ActionNames": [action], "ResourceArns": resources, "ContextEntries": entries}))
                result = subprocess.run(["aws", "iam", "simulate-custom-policy", "--profile", args.aws_profile,
                                         "--cli-input-json", "file://" + str(request_file), "--output", "json", "--no-cli-pager"],
                                        capture_output=True, text=True, timeout=30, check=False)
                if result.returncode:
                    raise RuntimeError("IAM simulation unavailable; verify the selected setup profile and iam:SimulateCustomPolicy permission")
                evaluations = json.loads(result.stdout)["EvaluationResults"]
                if len(evaluations) != 1:
                    raise RuntimeError("incomplete simulation context for " + name)
                per_resource = evaluations[0].get("ResourceSpecificResults", [])
                missing = set(evaluations[0].get("MissingContextValues", []))
                for resource in per_resource:
                    missing.update(resource.get("MissingContextValues", []))
                intentionally_absent = known_context_keys - set(context)
                if missing - intentionally_absent or per_resource and len(per_resource) != len(resources):
                    raise RuntimeError("incomplete resource simulation for " + name + ": " + json.dumps(evaluations))
                decisions.append(evaluations[0]["EvalDecision"] == "allowed")
                decisions.extend(r["EvalResourceDecision"] == "allowed" for r in per_resource)
                if not per_resource and evaluations[0]["EvalDecision"] != "allowed":
                    rejected.append((action, "all supplied resources", evaluations[0]["EvalDecision"]))
                rejected.extend((action, r["EvalResourceName"], r["EvalResourceDecision"]) for r in per_resource if r["EvalResourceDecision"] != "allowed")
            if all(decisions) != expected:
                raise RuntimeError("unexpected IAM decision: " + name + ": " + json.dumps(rejected))
            print("PASS " + name, flush=True)
    print(f"Passed {len(cases)} modeled authorization cases; live EC2 enforcement remains untested.")


if __name__ == "__main__":
    main()
