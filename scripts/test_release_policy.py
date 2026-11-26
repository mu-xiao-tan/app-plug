#!/usr/bin/env python3
"""Exercise the same read-only release policy gate that runs on a tag."""

import copy
import json
from pathlib import Path
import subprocess
import sys
import unittest


GATE = Path(__file__).with_name("release_policy.py")
USER = {"type": "User", "reviewer": {"id": 42, "login": "test-reviewer"}}
TEAM = {"type": "Team", "reviewer": {"id": 42, "slug": "test-release-team"}}


class ReleasePolicyTest(unittest.TestCase):
    def setUp(self):
        self.rule = {"type": "required_reviewers", "prevent_self_review": True,
                     "reviewers": [copy.deepcopy(USER)]}
        self.env = {
            "name": "github-release",
            "deployment_branch_policy": {"protected_branches": False, "custom_branch_policies": True},
            "protection_rules": [{"type": "branch_policy"}, self.rule],
        }
        self.pages = [{"total_count": 1, "branch_policies": [{"id": 1, "type": "tag", "name": "v*.*.*"}]}]

    def check(self, kind, body, *, accepts=False, raw=False):
        result = subprocess.run(
            [sys.executable, "-IB", str(GATE), kind], input=body if raw else json.dumps(body),
            text=True, capture_output=True, check=False, timeout=5,
        )
        if accepts:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertEqual(result.returncode, 1, result.stderr)
            self.assertIn("Release policy rejected:", result.stderr)

    def test_accepts_documented_users_and_teams(self):
        for reviewers in ([USER], [TEAM], [USER, TEAM]):
            with self.subTest(reviewers=reviewers):
                self.rule["reviewers"] = reviewers
                self.check("environment", self.env, accepts=True)

    def test_rejects_missing_or_invalid_no_self_review_flag(self):
        del self.rule["prevent_self_review"]
        self.check("environment", self.env)
        for value in (False, None, 1, "true", [], {}):
            with self.subTest(value=value):
                self.rule["prevent_self_review"] = value
                self.check("environment", self.env)

    def test_rejects_absent_or_malformed_reviewer_lists(self):
        del self.rule["reviewers"]
        self.check("environment", self.env)
        for value in (None, [], "placeholder", 1, True, {"User": USER}, [USER] * 7):
            with self.subTest(value=value):
                self.rule["reviewers"] = value
                self.check("environment", self.env)

    def test_rejects_any_invalid_reviewer_even_with_a_valid_one(self):
        for bad in (None, "placeholder", {}, {"type": "Bot", "reviewer": USER["reviewer"]},
                    {"type": "User"}, {"type": "Team", "reviewer": "team"},
                    {"type": "User", "reviewer": {"id": 0, "login": "name"}},
                    {"type": "User", "reviewer": {"id": True, "login": "name"}},
                    {"type": "User", "reviewer": {"id": "42", "login": "name"}},
                    {"type": "User", "reviewer": {"id": 43, "login": " "}},
                    {"type": "Team", "reviewer": {"id": 43, "login": "not-a-team-slug"}}, USER):
            with self.subTest(bad=bad):
                self.rule["reviewers"] = [USER, bad]
                self.check("environment", self.env)

    def test_rejects_absent_ambiguous_or_malformed_protection_rules(self):
        del self.env["protection_rules"]
        self.check("environment", self.env)
        for rules in (None, [], {"required_reviewers": self.rule}, [self.rule, self.rule],
                      [self.rule, None], [self.rule, {}], [self.rule, {"type": " "}]):
            with self.subTest(rules=rules):
                self.env["protection_rules"] = rules
                self.check("environment", self.env)

    def test_rejects_wrong_environment_or_policy_types(self):
        for name, policy in (("preview", self.env["deployment_branch_policy"]),
                             ("github-release", None),
                             ("github-release", {"protected_branches": 0, "custom_branch_policies": True}),
                             ("github-release", {"protected_branches": False, "custom_branch_policies": 1}),
                             ("github-release", {"protected_branches": False})):
            with self.subTest(name=name, policy=policy):
                self.env.update(name=name, deployment_branch_policy=policy)
                self.check("environment", self.env)

    def test_accepts_only_exact_version_tag_policy(self):
        self.check("policies", self.pages, accepts=True)
        policy = self.pages[0]["branch_policies"][0]
        for pages in (None, [], self.pages[0], [None], [{"total_count": 1}],
                      [{"total_count": True, "branch_policies": [policy]}],
                      [{"total_count": 2, "branch_policies": [policy]}],
                      [{"total_count": 1, "branch_policies": [policy, policy]}],
                      [{"total_count": 1, "branch_policies": [{**policy, "type": "branch"}]}],
                      [{"total_count": 1, "branch_policies": [{**policy, "name": "v*"}]}],
                      [*self.pages, {"total_count": 1, "branch_policies": []}]):
            with self.subTest(pages=pages):
                self.check("policies", pages)

    def test_rejects_invalid_or_ambiguous_json(self):
        valid = json.dumps(self.env)
        for body in ("", "{", valid + " {}", valid.replace('"id": 42', '"id": NaN'),
                     valid.replace('"prevent_self_review": true',
                                   '"prevent_self_review": false, "prevent_self_review": true')):
            with self.subTest(body=body):
                self.check("environment", body, raw=True)


if __name__ == "__main__":
    unittest.main()
