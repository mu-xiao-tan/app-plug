#!/usr/bin/env python3
"""Validate GitHub's read-only release environment responses from stdin."""

import argparse
import json
import sys


def object_from_pairs(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("API response contains duplicate JSON fields")
        result[key] = value
    return result


def reject_constant(_value):
    raise ValueError("API response contains an invalid JSON constant")


def nonblank_string(value):
    return isinstance(value, str) and bool(value) and not any(c.isspace() for c in value)


def check_environment(env):
    if not isinstance(env, dict) or env.get("name") != "github-release":
        raise ValueError("expected the github-release environment")
    policy = env.get("deployment_branch_policy")
    if (not isinstance(policy, dict) or policy.get("protected_branches") is not False
            or policy.get("custom_branch_policies") is not True):
        raise ValueError("expected custom deployment policies without protected branches")

    rules = env.get("protection_rules")
    if not isinstance(rules, list) or not all(
        isinstance(rule, dict) and nonblank_string(rule.get("type")) for rule in rules
    ):
        raise ValueError("expected an array of typed protection rules")
    review_rules = [rule for rule in rules if rule["type"] == "required_reviewers"]
    if len(review_rules) != 1 or review_rules[0].get("prevent_self_review") is not True:
        raise ValueError("expected exactly one reviewer rule with self-review disabled")
    reviewers = review_rules[0].get("reviewers")
    if not isinstance(reviewers, list) or not 1 <= len(reviewers) <= 6:
        raise ValueError("expected a nonempty array of up to six reviewers")

    identities = set()
    for entry in reviewers:
        if not isinstance(entry, dict) or entry.get("type") not in ("User", "Team"):
            raise ValueError("expected a User or Team reviewer")
        kind = entry["type"]
        principal = entry.get("reviewer")
        if not isinstance(principal, dict):
            raise ValueError("expected a reviewer identity object")
        identity = principal.get("id")
        label = principal.get("login" if kind == "User" else "slug")
        if type(identity) is not int or identity <= 0 or not nonblank_string(label):
            raise ValueError("expected a positive reviewer ID and a nonblank user login or team slug")
        if (kind, identity) in identities:
            raise ValueError("expected distinct reviewer identities")
        identities.add((kind, identity))


def check_policies(pages):
    if not isinstance(pages, list) or len(pages) != 1 or not isinstance(pages[0], dict):
        raise ValueError("expected a single deployment policy page")
    count = pages[0].get("total_count")
    policies = pages[0].get("branch_policies")
    if (type(count) is not int or count != 1 or not isinstance(policies, list)
            or len(policies) != 1 or not isinstance(policies[0], dict)
            or policies[0].get("type") != "tag" or policies[0].get("name") != "v*.*.*"):
        raise ValueError("expected only the v*.*.* tag deployment policy")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("response", choices=("environment", "policies"))
    args = parser.parse_args()
    gate = check_environment if args.response == "environment" else check_policies
    try:
        gate(json.load(sys.stdin, object_pairs_hook=object_from_pairs, parse_constant=reject_constant))
    except (ValueError, RecursionError) as exc:
        print(f"Release policy rejected: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
